# Agent Runs — wire protocol

This document specifies the public wire protocol that the MANTYX agent-runs API
speaks with SDKs. It is the source of truth for anyone implementing a new
client (Python, Rust, Java…) and is shipped with each first-party SDK so the
SDK repository can stand on its own when it is extracted from this monorepo.

The companion document for this protocol — server-side overview, internals,
deployment notes — is [`docs/agent-runs.md`](./agent-runs.md).

## 1. Concepts

**Ephemeral agent.** A run-time agent that is *defined by the request* rather
than persisted as a row in MANTYX's `Agent` table. The full spec (system
prompt, model, tools) is stored as part of each session/run for observability
but is not editable from the dashboard.

**Tool refs.** Three flavours, all carried inside the agent spec:

| `kind`           | Resolved by | Notes |
| ---------------- | ----------- | ----- |
| `mantyx`         | server      | A workspace `Tool` row referenced by id (HTTP / Code / Plugin). |
| `mantyx_plugin`  | server      | A platform plugin tool referenced by name. |
| `local`          | client      | Defined and executed in the SDK's process. |

When the model calls a `local` tool, MANTYX pauses the agent loop, emits a
`local_tool_call` event over SSE and waits for the SDK to POST a tool-result
back via HTTP.

**One-shot run vs. session.** A run is an LLM execution. Runs may be:

- *one-shot* (`POST /agent-runs`) — fire-and-stream, no persistent state apart
  from observability.
- *session-scoped* (`POST /agent-sessions/:id/messages`) — the run inherits the
  session's full message history, and the new user/assistant turns are
  appended back to the session on success.

## 2. Authentication

All SDK-facing endpoints sit under

```
/api/v1/workspaces/{workspaceSlug}/...
```

and are authenticated with a workspace API key with usage `developer_api`:

```
Authorization: Bearer <api-key>
# or, equivalently:
X-API-Key: <api-key>
```

The workspace slug in the URL must match the key's tenant. Mismatches return
`404 not_found`. Missing/invalid keys return `401 unauthorized`. Rate limits
follow the workspace's existing developer-API sliding-window policy.

## 3. Models

```
GET /api/v1/workspaces/{workspaceSlug}/models
```

Returns models the API key's workspace can run, including BYOK providers and
platform-hosted offerings visible to the workspace's tier.

```jsonc
{
  "models": [
    {
      "id": "platform:cm6abc123",
      "label": "Anthropic Claude Sonnet 4.5 (platform)",
      "provider": "anthropic",
      "vendorModelId": "claude-sonnet-4-5",
      "source": "platform_offering",
      "contextWindowTokens": 200000,
      "pricing": { "inputPer1MUsd": 3.0, "outputPer1MUsd": 15.0, "cacheReadPer1MUsd": 0.3 }
    },
    {
      "id": "provider:cm6def456",
      "label": "OpenAI (workspace BYOK) — gpt-5.5",
      "provider": "openai",
      "vendorModelId": "gpt-5.5",
      "source": "workspace_provider",
      "contextWindowTokens": 200000,
      "pricing": null
    }
  ],
  "defaultModelId": "platform:cm6abc123"
}
```

The `id` is the canonical value the SDK passes back as `RunSpec.modelId` /
`SessionSpec.modelId`. The server accepts three additional shorthand forms:

- `provider:<id>:<vendor>` — pin a specific BYOK provider but override the
  vendor model id.
- `<vendorModelId>` — bare vendor id; only succeeds if exactly one workspace
  provider can run it.
- `undefined`/omitted — falls back to the workspace default provider's
  default model.

Invalid `modelId` values return `400 invalid_model` with a candidate list
in the body when applicable.

## 4. Agent spec

The agent spec is the body shape used by `POST /agent-runs` and `POST
/agent-sessions`:

```jsonc
{
  "name": "ephemeral",                  // optional, observability only
  "agentId": "agent_cm6abc123",         // optional — see §4.1
  "systemPrompt": "You are helpful.",   // required unless agentId is set
  "modelId": "platform:cm6abc123",      // optional, see §3
  "tools": [
    { "kind": "mantyx", "id": "tool_cm6..." },
    { "kind": "mantyx_plugin", "name": "web_search" },
    {
      "kind": "local",
      "name": "read_file",
      "description": "Read a file from the user's machine",
      "parameters": {                   // JSON Schema for the args object
        "type": "object",
        "properties": { "path": { "type": "string" } },
        "required": ["path"],
        "additionalProperties": false
      }
    }
  ],
  "budgets": { "maxToolTurns": 32 },    // optional safety cap
  "metadata": {                         // optional, see §4.2
    "customer": "acme",
    "env": "prod"
  }
}
```

`POST /agent-runs` additionally accepts `prompt` *or* `messages` (an array of
`{role, content}`). Sending both is a `400 invalid_request`.

### 4.1 Triggering a persisted MANTYX agent (`agentId`)

Set `agentId` to the `id` of a workspace `Agent` to run that agent instead of
defining an ephemeral one inline. When `agentId` is set:

- `systemPrompt` becomes optional. If omitted, the server uses the agent's
  stored system prompt at run time.
- `modelId` becomes optional. If omitted, the server uses the agent's
  configured LLM provider (or the workspace automation provider if the agent
  has *Use workspace default model* turned on).
- The agent's own tools are loaded from its workspace configuration —
  including memory, skills, and plugin tools — and your `tools` array is
  **merged on top**. This is typically used to attach `local` tools so the
  agent can call back into your process for this run, without needing to
  edit the agent's stored tool list.
- The API key must be authorized for this `agentId`. Keys created with an
  empty `agentIds` allowlist (= "all agents") work for any agent in the
  workspace; otherwise the agent must be in the key's allowlist or the call
  returns `403 forbidden`.
- An unknown / cross-workspace `agentId` returns `403` (the API key check
  fires first); a malformed `agentId` returns `400`.

Both `agentId` and `systemPrompt` may be supplied. The agent's stored prompt
wins; the inline `systemPrompt` is ignored.

### 4.2 `metadata` (developer-supplied KV for filtering)

`metadata` is a flat string→string KV that is **persisted alongside the run /
session** and surfaced in the MANTYX dashboard. Use it to tag runs with your
own application identifiers (`customer`, `env`, `workflow`, `trace_id`, …) so
your team can filter the observability UI without reverse-engineering the
prompt.

Validation (server-side, `400 invalid_request` on violation):

| Constraint                | Limit                              |
| ------------------------- | ---------------------------------- |
| Max entries               | 16                                 |
| Key pattern               | `^[A-Za-z0-9._-]{1,64}$`           |
| Value type / length       | string ≤ 256 chars                 |
| Serialized JSON size      | ≤ 4 KB                             |

For session-scoped runs the inheritance rules are:

- `POST /agent-sessions { metadata }` — sets the session's metadata; this is
  inherited by every run created through `POST /agent-sessions/:id/messages`.
- `POST /agent-sessions/:id/messages { metadata }` — optional per-message
  override. The server snapshots `session.metadata` ⊕ override (run-level
  keys win) onto the run row at creation time. Later edits to the session
  metadata do not retroactively rewrite past runs.

Metadata is returned on every read: `GET /agent-runs/:id`,
`GET /agent-sessions/:id`, and the admin list/detail endpoints. Filtering on
the admin list endpoints uses repeated `?metadata=key:value` query params,
AND-combined; see `docs/agent-runs.md` §"Web UI" for details.

## 5. One-shot runs

```
POST   /api/v1/workspaces/{slug}/agent-runs
GET    /api/v1/workspaces/{slug}/agent-runs/{runId}
GET    /api/v1/workspaces/{slug}/agent-runs/{runId}/stream
POST   /api/v1/workspaces/{slug}/agent-runs/{runId}/tool-results
POST   /api/v1/workspaces/{slug}/agent-runs/{runId}/cancel
```

`POST /agent-runs` returns `202 Accepted` immediately:

```json
{ "runId": "run_abc", "streamUrl": "/api/v1/workspaces/acme/agent-runs/run_abc/stream" }
```

`GET .../stream` is the canonical event channel; see §7.

`GET /agent-runs/{runId}` returns the run snapshot (status, final text, error,
spec) without subscribing to live events. Useful for polling long runs.

## 6. Sessions

```
POST   /api/v1/workspaces/{slug}/agent-sessions
GET    /api/v1/workspaces/{slug}/agent-sessions/{sessionId}
POST   /api/v1/workspaces/{slug}/agent-sessions/{sessionId}/messages
DELETE /api/v1/workspaces/{slug}/agent-sessions/{sessionId}
```

`POST /agent-sessions` creates a session with the agent spec. The body shape
is the same as the one-shot agent spec (no `prompt`, no `messages`).
Returns:

```json
{ "sessionId": "ses_abc" }
```

`POST /agent-sessions/{id}/messages` queues a new run scoped to the session
and returns `{ runId, streamUrl }` just like a one-shot run. Body:

```jsonc
{
  "prompt": "What's in /etc/hosts?",
  "tools": [/* optional refresh of tool definitions */]
}
```

The server prepends the session's prior messages, runs the model, and on
success appends the new user/assistant turns back to the session row. Local
tool **handlers** are *not* persisted: the session stores definitions
(name, schema, description) so that a restarted SDK can re-bind handlers and
keep going.

`DELETE` flips the session to `ended` and cancels any in-flight run.

## 7. SSE stream

`GET .../agent-runs/{runId}/stream` returns `text/event-stream`. Reconnects
support both:

- the standard `Last-Event-ID` request header (set automatically by browser
  `EventSource`), and
- a `?lastSeq=<int>` query param (preferred for bare HTTP clients).

The server replays missed events from the `EphemeralAgentRunEvent` table in
order before resuming the live tail.

Each frame:

```
id: 17
event: <type>
data: <utf-8 JSON>

```

`<type>` and `<data>` shapes:

```jsonc
// running message
{ "seq": 1, "type": "started", "data": {} }

// streamed assistant tokens (zero or more per turn)
{ "seq": 2, "type": "assistant_delta", "data": { "text": "Hello" } }

// completed assistant message (text + any tool calls about to execute)
{ "seq": 3, "type": "assistant_message", "data": { "text": "...", "toolCalls": [...] } }

// server-side tool call/result (informational; SDK does not act on these)
{ "seq": 4, "type": "tool_call",   "data": { "toolUseId": "...", "name": "...", "input": {...} } }
{ "seq": 5, "type": "tool_result", "data": { "toolUseId": "...", "name": "...", "ok": true, "summary": "..." } }

// LOCAL tool call — SDK MUST POST a tool-result for the same toolUseId
{ "seq": 6, "type": "local_tool_call", "data": { "toolUseId": "tu_x", "name": "read_file", "input": { "path": "/etc/hosts" } } }

// echo of the SDK's POSTed tool-result, persisted for replay
{ "seq": 7, "type": "local_tool_result_in", "data": { "toolUseId": "tu_x", "output": "127.0.0.1 ..." } }

// terminal event
{ "seq": 8, "type": "result",    "data": { "subtype": "success", "text": "Final reply" } }
{ "seq": 8, "type": "result",    "data": { "subtype": "error_local_tool_timeout", "error": "..." } }
{ "seq": 8, "type": "cancelled", "data": {} }
```

A run terminates with exactly one of `result` or `cancelled`. The connection
is closed by the server immediately after sending the terminal event. Clients
should not assume any particular ordering between the human-readable `event:`
field and the parsed `type` inside `data` — they are always equal, but
implementations should rely on `data.type` because some HTTP middleware
strips the `event:` line.

## 8. Local tool result

```
POST /api/v1/workspaces/{slug}/agent-runs/{runId}/tool-results
Content-Type: application/json

{
  "toolUseId": "tu_x",
  "result": "127.0.0.1 localhost"     // OR
  "error":  "ENOENT: no such file"
}
```

`200 OK` on accept. `404 unknown_tool_use` if the `toolUseId` is unknown,
already satisfied, or the run has terminated. `409 run_terminal` if the run
has already produced a `result` event.

`result` MUST be a string; SDKs serialize structured outputs as JSON before
posting. Errors are surfaced to the model as a tool-error response.

## 9. Cancellation

```
POST /api/v1/workspaces/{slug}/agent-runs/{runId}/cancel
```

Idempotent. The run will produce a terminal `cancelled` event on the SSE
stream. In-flight `tool-results` posted after cancellation are accepted with
`200 OK` but ignored.

## 10. Errors

All non-2xx responses use this body shape:

```jsonc
{
  "error": "invalid_model",            // machine-readable code
  "message": "Model 'foo' is ambiguous; pick one of: provider:cm6...",
  "candidates": [/* sometimes present */]
}
```

Common codes:

| Code                   | HTTP | Notes |
| ---------------------- | ---: | ----- |
| `unauthorized`         | 401  | Missing/invalid API key |
| `not_found`            | 404  | Workspace, run, or session unknown |
| `invalid_request`      | 400  | Body failed Zod validation |
| `invalid_model`        | 400  | `modelId` couldn't be resolved |
| `unknown_tool_use`     | 404  | Tool-result for an unknown `toolUseId` |
| `run_terminal`         | 409  | Tool-result after run finished |
| `rate_limited`         | 429  | Per-API-key sliding window |

## 11. Suggested client architecture

A reference SDK should:

1. Hold the API key + workspace slug and a small `fetch` (or stdlib HTTP)
   client.
2. Maintain a registry of local tool handlers, keyed by `name`.
3. On `runAgent` / `session.send`:
   - POST the run/message, get `{ runId, streamUrl }`.
   - Open the SSE stream with `Last-Event-ID` if reconnecting.
   - On `local_tool_call`, look up the handler, validate args against the
     tool's schema, run it, POST the result back to `.../tool-results`.
   - On terminal `result`, resolve the call. On `error` subtype, throw.
4. Re-emit assistant deltas/events as a stream/iterator for callers who care
   about live output.
5. Treat the protocol as the contract. Implementation details such as Valkey
   pub/sub or pgvector are server-side only.

The TypeScript SDK in [`packages/mantyx-sdk/ts/`](../packages/mantyx-sdk/ts/) and the Go SDK in
[`packages/mantyx-sdk/go/`](../packages/mantyx-sdk/go/) are reference implementations of this protocol.
