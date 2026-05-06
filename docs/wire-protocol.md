# MANTYX Wire Protocol — messaging & data structures

This is the SDK-builder reference for the **messaging layer** that sits on top
of the HTTP / SSE endpoints documented in
[`agent-runs-protocol.md`](./agent-runs-protocol.md). It catalogs every
message shape MANTYX and a client SDK exchange during an agent run, in the
order they flow on the wire, and pins down the resolved data structures the
SDK is expected to ship for client-resolved (`*_local`) tools.

If you're just looking for HTTP routes, auth, body shapes, or session
semantics, start with `agent-runs-protocol.md`. If you're writing or
maintaining an SDK and want to know *exactly* what a `local_tool_call` event
looks like for `mcp_local`, you're in the right place.

> **Stability.** Field names listed in *bold* are part of the documented
> stable surface. Any other fields are passed through verbatim and survive
> round-trips, but their semantics are not contractually guaranteed. The
> server uses Zod with `passthrough` for all `*_local` resolved-content
> blobs (Agent Card, MCP `Tool[]`, server `Implementation`) so future spec
> additions flow through without a server-side schema bump.

---

## 0. Glossary

| Term                | Meaning |
| ------------------- | ------- |
| **MANTYX**          | The agent operating system server (this repo). Owns LLM orchestration, tool execution for server-resolved tools, persistence. |
| **SDK**             | Anything calling the public agent-runs API — typically `@mantyx/ts-sdk`, but also other-language SDKs and direct HTTP clients. |
| **Agent run**       | A single LLM execution. Streams events; ends with a terminal `result` / `error` / `cancelled`. |
| **Spec**            | The JSON object describing what the run does — model, prompt, tools, budgets, optional `reasoningLevel`. Sent in the `POST /agent-runs` (or `.../messages`) body. |
| **Tool ref**        | One entry in `spec.tools[]`. A discriminated union keyed by `kind`. |
| **Server-resolved** | A tool MANTYX executes itself (`mantyx`, `mantyx_plugin`, `a2a`, `mcp`). The SDK only sees informational `tool_result` events. |
| **Client-resolved** | A tool the SDK executes (`local`, `a2a_local`, `mcp_local`). MANTYX emits `local_tool_call`, the SDK does the work, the SDK posts back to `.../tool-results`. |
| **Resolution**      | The act of turning an external resource (A2A peer, MCP server) into a self-contained JSON document the model can reason about. For `*_local` kinds, resolution is the **SDK's** responsibility. |

---

## 1. The full message lifecycle

```text
SDK                                          MANTYX
 │                                              │
 │ ── (resolve A2A cards / MCP catalogs ──────▶ │  (offline, SDK-side)
 │     locally; cache as needed)                │
 │                                              │
 │ ── POST /agent-runs ─────────────────────▶   │
 │    body: spec (model, prompt, tools, …)      │
 │                                              │
 │ ◀────────────────────── 201 { runId,         │
 │                              streamUrl }     │
 │                                              │
 │ ── GET streamUrl  (text/event-stream) ────▶  │
 │                                              │
 │            ┌─────────────────────────────────┤   ───┐
 │ ◀ SSE event│ assistant_delta                 │      │
 │ ◀ SSE event│ thinking_delta (iff reasoning)  │      │ model loop
 │ ◀ SSE event│ tool_result (server-resolved)   │      │
 │ ◀ SSE event│ local_tool_call ◀──┐            │      │
 │            └────────────────────┼────────────┤   ───┘
 │                                 │            │
 │ ── POST .../tool-results ───────┘──────────▶ │
 │    { toolUseId, result | error }             │
 │                                              │
 │ ◀ SSE event  result (terminal)               │
 │                                              │
 │  (close stream)                              │
```

The lifecycle has three logical stages:

1. **Setup (SDK-only).** For any `*_local` tool the SDK plans to expose, it
   pre-resolves the resource locally. For `a2a_local` that means fetching
   or constructing the peer's [Agent Card](https://google.github.io/A2A/specification/#agent-card)
   JSON. For `mcp_local` that means speaking MCP `Initialize` + `tools/list`
   and capturing the result. All resolved content is shipped inline inside
   the spec; nothing is fetched server-side.
2. **Run (MANTYX-driven).** The SDK opens the SSE stream and listens. MANTYX
   runs the LLM loop, executing server-resolved tools itself and emitting
   `local_tool_call` for client-resolved ones. The SDK answers each
   `local_tool_call` by posting back a tool result.
3. **Termination.** A terminal `result` (success), `error` (failure), or
   `cancelled` event closes the run. The SSE stream is then safe to close.

---

## 2. Spec submission (SDK → MANTYX)

`POST /api/v1/workspaces/{slug}/agent-runs` accepts the spec body. Only the
new wire-protocol additions are documented in detail here; for the full
spec body shape (system prompt, tool budgets, sessions, `agentId`
short-circuit, etc.) see `agent-runs-protocol.md` §4.

### 2.1 Spec body (top-level shape)

```jsonc
{
  "modelId": "openai:gpt-5.5",
  "systemPrompt": "...",
  "prompt": "...",                     // OR "messages": [...]
  "tools": [ /* tool refs — see §3 */ ],
  "reasoningLevel": "medium",          // optional; see §6
  "budgets": { "maxToolTurns": 32 },
  "outputSchema": {                    // optional; see §7
    "name": "weather_report",          //   defaults to "output"
    "schema": { /* JSON Schema */ }
  },
  "metadata": { "customer": "acme" }   // optional, free-form k/v
}
```

### 2.2 Sessions

Same body shape, posted to `POST /agent-sessions/:id/messages`. The session
keeps the conversation history; per-message `tools`, `reasoningLevel`, and
`outputSchema` *replace* the session's defaults for that single run only —
the next run falls back to whatever the session was created with.

---

## 3. Tool ref taxonomy

Every entry in `spec.tools[]` is one of the seven shapes below. The
*resolution column* is the contract that drives everything else: **server**
means MANTYX runs the tool itself and the SDK only ever sees a
`tool_result` event; **client** means MANTYX is a transport and the SDK
must answer `local_tool_call` events.

| Kind             | Resolution | Wire-payload contract |
| ---------------- | ---------- | --------------------- |
| `mantyx`         | server     | `{ id }` reference to a workspace `Tool` row. |
| `mantyx_plugin`  | server     | `{ name }` reference to a platform plugin tool. |
| `local`          | client     | `{ name, description?, parameters? }` — JSON Schema. |
| `a2a`            | server     | `{ name, agentCardUrl, headers?, contextId?, description? }`. |
| `a2a_local`      | client     | `{ name, agentCard }` — **resolved A2A Agent Card JSON content**. |
| `mcp`            | server     | `{ name, url, headers?, toolFilter? }`. |
| `mcp_local`      | client     | `{ name, serverInfo?, tools[] }` — **resolved MCP `Tool[]`**. |

The remainder of this document focuses on `a2a_local` and `mcp_local`,
because they're the ones that carry SDK-resolved structured content. For
the wire shapes of the other five kinds, see `agent-runs-protocol.md` §4.

### 3.1 `a2a_local` — SDK-resolved Agent Card

The defining feature of `a2a_local` is that the SDK ships a fully-resolved
[A2A Agent Card](https://google.github.io/A2A/specification/#agent-card) as
the `agentCard` field. MANTYX never reaches out to discover it.

**Wire shape:**

```jsonc
{
  "kind": "a2a_local",
  "name": "intranet_hr_agent",          // model-facing; /^[a-zA-Z0-9_]{1,64}$/
  "description": "...",                 // OPTIONAL; overrides the synthesized one
  "agentCard": {                        // REQUIRED; A2A Agent Card content
    "protocolVersion": "0.3.0",
    "name": "Acme HR",
    "description": "Answers questions about HR policies and benefits.",
    "url": "https://hr.intranet.acme/a2a",
    "version": "1.4.0",
    "provider": { "organization": "Acme Co.", "url": "https://acme.example/" },
    "documentationUrl": "https://hr.intranet.acme/docs",
    "iconUrl": "https://hr.intranet.acme/icon.png",
    "capabilities": { "streaming": false, "pushNotifications": false },
    "defaultInputModes": ["text/plain"],
    "defaultOutputModes": ["text/plain"],
    "skills": [
      {
        "id": "pto_lookup",
        "name": "PTO lookup",
        "description": "Find a teammate's remaining PTO days for the year.",
        "tags": ["hr", "pto"],
        "examples": ["How many PTO days does Alice have left?"]
      }
    ],
    "securitySchemes": { /* spec-shaped, never read by MANTYX */ },
    "security": [ /* spec-shaped, never read by MANTYX */ ]
    /* …any other A2A spec field passes through unchanged. */
  }
}
```

**Where the SDK obtains `agentCard`:**

- *Well-known URL.* Most peers expose the card at
  `<peer>/.well-known/agent-card.json`. The SDK can simply
  `fetch` it (with whatever auth applies on the local network).
- *Static config.* For peers that don't publish a card, hand-craft one — the
  spec only requires a couple of fields and the rest is all metadata.
- *Registry / cache.* Cache cards locally and refresh periodically. MANTYX
  treats every spec submission as a fresh snapshot, so new cards take
  effect on the next run / message.

**What MANTYX does with `agentCard`:**

| Field                    | Used for | Notes |
| ------------------------ | -------- | ----- |
| `name`, `description`    | Tool description for the model | Used to compose `"Delegate a task to <name>: <description>"` if no `description` override is supplied at the ref level. |
| `skills[]` (first 12)    | Tool description for the model | Bulleted into the description so the model can choose a peer based on capability. |
| All other fields         | Echo only | Forwarded back to the SDK in every `local_tool_call` event so the SDK can dispatch by `url`, by `provider.organization`, by `protocolVersion`, or whatever it indexed on. |

### 3.2 `mcp_local` — SDK-resolved Tool catalog

The defining feature of `mcp_local` is that the SDK ships the **verbatim
output of MCP `tools/list`** as `tools[]`, with field names matching the
MCP spec (`inputSchema`, not `parameters`). Optionally, the SDK can also
ship the `Implementation` block from MCP `Initialize` as `serverInfo`.

**Wire shape:**

```jsonc
{
  "kind": "mcp_local",
  "name": "fs",                         // SDK-side server label; not a name prefix
  "serverInfo": {                       // OPTIONAL; from MCP Initialize
    "name": "mcp-server-filesystem",
    "version": "0.4.1"
    /* …any other Implementation field passes through unchanged. */
  },
  "tools": [                            // REQUIRED; verbatim MCP tools/list output
    {
      "name": "fs_read_file",           // model-facing; /^[a-zA-Z0-9_]{1,64}$/; SDK owns naming
      "description": "Read a file under /workspace.",
      "inputSchema": {                  // MCP's term for the JSON Schema
        "type": "object",
        "properties": { "path": { "type": "string" } },
        "required": ["path"]
      },
      "annotations": {                  // OPTIONAL; spec-defined hints
        "readOnlyHint": true,
        "openWorldHint": false
      }
      /* …any other MCP Tool field passes through unchanged. */
    }
  ]
}
```

**Where the SDK obtains `tools[]`:**

```ts
// pseudo-code, MCP-SDK-flavoured
const client = new McpClient(stdio("./fs-server"));
const init = await client.initialize();        // → { name, version, … }
const list = await client.listTools();         // → { tools: [...] }

// drop straight into the spec
const ref = {
  kind: "mcp_local" as const,
  name: "fs",
  serverInfo: init,
  tools: list.tools,
};
```

**What MANTYX does with the catalog:**

| Field                    | Used for | Notes |
| ------------------------ | -------- | ----- |
| `tools[].name`           | Model-facing tool name | Used as-is. MANTYX does **not** prefix with the ref's `name`. The SDK is responsible for any naming convention (e.g. emit `fs_read_file` instead of `read_file` if you have multiple servers). |
| `tools[].description`    | Model-facing description | Used as-is. |
| `tools[].inputSchema`    | LLM tool-call schema | Converted to Zod via `jsonSchemaToZod` to constrain the model's tool call. Empty schema → no-arg tool. |
| `tools[].annotations`    | Echo only | Forwarded to the SDK in `local_tool_call` events (as part of the call envelope) for observability. |
| `serverInfo`             | Echo only | Forwarded to the SDK in `local_tool_call.mcpServerInfo`. |

> **Naming convention reminder.** Because MANTYX doesn't prefix names for
> `mcp_local`, two refs that both expose a tool called `read_file` will
> collide. Either give the second one a different `name` in the catalog or
> drop it via SDK-side filtering. (For `mcp` — *remote* MCP — MANTYX does
> auto-prefix with the ref's `name`, so collisions are impossible.)

---

## 4. SSE event vocabulary

The SSE stream is opened with `GET /agent-runs/:runId/stream`. Standard
SSE rules apply: each frame is `data: <json>\n\n`, with an `id: <seq>` line
so reconnects can use `Last-Event-ID`.

Every event payload has the same envelope:

```jsonc
{ "seq": 7, "type": "<event-type>", "data": { /* type-specific */ } }
```

The vocabulary (`EphemeralEventType` in `bus.ts`):

| Type                    | Direction | Frequency | Purpose |
| ----------------------- | --------- | --------- | ------- |
| `assistant_delta`       | M → SDK   | Many      | Streamed assistant text token / chunk. |
| `thinking_delta`        | M → SDK   | Many (iff `reasoningLevel > 0`) | Streamed extended-thinking text (provider redacts when policy requires). |
| `tool_result`           | M → SDK   | Per server-resolved tool call | Informational — tells the SDK that MANTYX ran a server-resolved tool (`mantyx`, `mantyx_plugin`, `a2a`, `mcp`) and got a result. The SDK does not need to act on it. |
| `local_tool_call`       | M → SDK   | Per client-resolved tool call | **Action required.** SDK must POST a tool-result. |
| `local_tool_result_in`  | M → SDK   | Per client-resolved tool call | Informational mirror of the tool-result the SDK just posted, persisted for observability. Re-emitted to late subscribers so they can replay the conversation. |
| `assistant_message`     | M → SDK   | 1× per turn | Final assistant message for the turn (concatenated, persistence-ready). |
| `result`                | M → SDK   | 1× terminal | Successful completion. Carries the final assistant text and run summary. |
| `error`                 | M → SDK   | 1× terminal | Failure. Carries `error` (machine code) + `message`. |
| `cancelled`             | M → SDK   | 1× terminal | Cancellation. Run was aborted via `POST /cancel`. |

`result`, `error`, and `cancelled` are the **terminal** events — the SDK
should close the SSE stream after one of them arrives.

### 4.1 `assistant_delta` / `thinking_delta`

```jsonc
{ "seq": 3, "type": "assistant_delta", "data": { "text": "Hello" } }
{ "seq": 4, "type": "thinking_delta",  "data": { "text": "Considering options..." } }
```

`thinking_delta` only fires when `reasoningLevel > 0` and the provider
exposes reasoning (OpenAI o-series / GPT-5.x, Anthropic extended thinking,
Gemini ≥ 3 with `thinkingConfig.includeThoughts`). Treat it as opaque
progress text — it's not part of the canonical assistant response.

### 4.2 `tool_result` (server-resolved tools)

```jsonc
{
  "seq": 5,
  "type": "tool_result",
  "data": {
    "toolUseId": "tu_a",
    "name": "github_search_repos",
    "result": "..."                     // truncated for display; never JSON-parsed by SDK
  }
}
```

Purely informational. The SDK does not respond.

### 4.3 `local_tool_call` (client-resolved tools)

This is the workhorse event for SDK-implemented tools. Payload shape varies
slightly by `kind`, but the envelope is always:

```jsonc
{
  "seq": <int>,
  "type": "local_tool_call",
  "data": {
    "toolUseId": "<opaque-id>",         // round-trip back in the tool-result POST
    "name": "<model-facing tool name>",
    "args": { /* model-supplied args */ },
    "kind": "<local | a2a_local | mcp_local>",
    /* …kind-specific extras below… */
  }
}
```

Older SDKs that ignore the `kind` discriminator can still match on `name`
and dispatch correctly — the `kind` field is additive metadata.

#### 4.3.1 `kind: "local"` — generic local tools

No extras. Dispatch by `name`.

```jsonc
{
  "seq": 6,
  "type": "local_tool_call",
  "data": {
    "toolUseId": "tu_x",
    "name": "compute_total",
    "args": { "amount": 42, "currency": "USD" },
    "kind": "local"                     // OR omitted (legacy)
  }
}
```

#### 4.3.2 `kind: "a2a_local"` — local A2A delegations

Carries the **full Agent Card** echoed back from the spec, so the SDK can
dispatch to the right A2A client when it manages multiple peers.

```jsonc
{
  "seq": 7,
  "type": "local_tool_call",
  "data": {
    "toolUseId": "tu_y",
    "name": "intranet_hr_agent",
    "args": { "message": "When does PTO reset?" },
    "kind": "a2a_local",
    "agentCard": {                      // full Agent Card from the spec
      "name": "Acme HR",
      "url": "https://hr.intranet.acme/a2a",
      "skills": [ /* ... */ ]
      /* ...all other fields the SDK shipped... */
    }
  }
}
```

`args.message` is *always* `{ "message": string }` for `a2a_local` — the
LLM's task is reduced to "what do I want to ask the peer in plain text?"
so the SDK doesn't have to re-derive an A2A `message` envelope from a
tool-specific schema.

#### 4.3.3 `kind: "mcp_local"` — local MCP tool calls

Carries dispatch hints so the SDK can route to the right MCP client without
parsing the tool name back into pieces.

```jsonc
{
  "seq": 8,
  "type": "local_tool_call",
  "data": {
    "toolUseId": "tu_z",
    "name": "fs_read_file",             // identical to what the SDK declared
    "args": { "path": "/etc/hosts" },
    "kind": "mcp_local",
    "mcpServer": "fs",                  // ref's `name` — SDK's MCP-client key
    "mcpToolName": "fs_read_file",      // duplicates `name` for the SDK's convenience
    "mcpServerInfo": {                  // present iff the spec carried `serverInfo`
      "name": "mcp-server-filesystem",
      "version": "0.4.1"
    }
  }
}
```

The SDK's typical dispatch path is:

```ts
const client = mcpClients.get(call.mcpServer);  // by SDK label
if (!client) throw new Error(`unknown MCP server ${call.mcpServer}`);
const result = await client.callTool({
  name: call.mcpToolName,
  arguments: call.args,
});
const text = result.content
  .filter((b) => b.type === "text")
  .map((b) => b.text)
  .join("\n");
await fetch(`${baseUrl}/agent-runs/${runId}/tool-results`, {
  method: "POST",
  headers: { "Content-Type": "application/json", Authorization: `Bearer ${apiKey}` },
  body: JSON.stringify({ toolUseId: call.toolUseId, result: text }),
});
```

### 4.4 `assistant_message`

```jsonc
{
  "seq": 12,
  "type": "assistant_message",
  "data": { "text": "Here's what I found...", "toolCalls": [/* … */] }
}
```

Emitted once per assistant turn after deltas finish. Useful when the SDK
wants the persisted form of the turn rather than a delta concatenation.

### 4.5 Terminal events

```jsonc
{ "seq": 14, "type": "result",    "data": { "ok": true,  "text": "..." } }
{ "seq": 14, "type": "error",     "data": { "error": "model_failure", "message": "..." } }
{ "seq": 14, "type": "cancelled", "data": { "reason": "user" } }
```

After one of these arrives, no further events will be emitted; close the
SSE stream.

---

## 5. SDK → MANTYX: tool-result POST

When the SDK sees a `local_tool_call`, it owes MANTYX exactly one
tool-result POST (success or failure):

```http
POST /api/v1/workspaces/{slug}/agent-runs/{runId}/tool-results
Content-Type: application/json
Authorization: Bearer <api-key>

{
  "toolUseId": "tu_z",                  // copied from local_tool_call
  "result":    "<file contents>"        // OR "error": "..." (mutually exclusive)
}
```

| Field        | Type    | Required | Notes |
| ------------ | ------- | -------- | ----- |
| `toolUseId`  | string  | yes      | Must match a pending `local_tool_call`'s id. |
| `result`     | string  | one-of   | Successful textual result (≤ 2 MB). For MCP tools, flatten content blocks to text. For A2A delegations, the peer's reply text. |
| `error`      | string  | one-of   | Human-readable failure message (≤ 8 KB). Surfaced to the model so it can recover. |

Server response codes:

| Code | When |
| ---- | ---- |
| `204` | Accepted; the runner was woken and will resume the model loop. |
| `400` | Body failed Zod validation (missing `toolUseId`, both/neither of `result`/`error`, etc.). |
| `404` | `unknown_tool_use` — `toolUseId` doesn't match any pending call (already answered or unknown id). |
| `409` | `run_terminal` — the run already finished (success, failure, cancel, or local-tool timeout). The result is dropped. |

The runner enforces a per-call `localToolTimeoutMs` (default 5 minutes).
After timeout the model loop unblocks with a synthetic
"Timed out waiting for local tool result" error — which is also why a
`409 run_terminal` for a tool-result POST is a normal occurrence.

---

## 6. `reasoningLevel`

`spec.reasoningLevel` controls the LLM's extended-thinking effort. Two
input shapes are accepted; both map to a numeric `0–100` internally.

| Form        | Values                                | Notes |
| ----------- | ------------------------------------- | ----- |
| **String**  | `"off"`, `"low"`, `"medium"`, `"high"` | Snaps to `0`, `30`, `50`, `80` (matches the web composer). |
| **Number**  | integer `0`–`100`                     | Pass-through. `0` explicitly disables provider thinking. |

Per provider:

| Provider                   | Knob driven by `reasoningLevel` |
| -------------------------- | ------------------------------- |
| OpenAI Responses (o-series, GPT-5.x) | `reasoning.effort` |
| Gemini ≥ 3                 | `thinkingConfig.thinkingLevel` |
| Gemini ≤ 2.5               | `thinkingConfig.thinkingBudget` (token budget; scaled) |
| Anthropic / Bedrock-Anthropic | extended thinking budget (≈ 512 tokens at `low` → ≈ 8 000 at `high`) |
| xAI Grok, others           | ignored |

When `reasoningLevel > 0` and the provider supports it, the SSE stream
will include `thinking_delta` events alongside `assistant_delta`.

---

## 7. `outputSchema` (structured final reply)

`outputSchema` constrains the final assistant message to a JSON document
conforming to a JSON Schema. When set, the run's terminal `result` event
still carries the reply as `data.text: string`, but that string is
guaranteed-parseable JSON matching the supplied schema.

```jsonc
"outputSchema": {
  "name":   "weather_report",          // optional; default "output"; /^[a-zA-Z0-9_-]{1,64}$/
  "schema": { /* JSON Schema */ }      // required, root must be a JSON object
}
```

| Field    | Type   | Required | Notes |
| -------- | ------ | -------- | ----- |
| `name`   | string | no       | Stable identifier passed to providers (OpenAI `text.format.name`, Anthropic synthetic-tool name). Defaults to `"output"`. |
| `schema` | object | yes      | JSON Schema for the assistant text. Root must be a JSON object — most providers reject array/scalar roots in structured-output mode. Passed through verbatim; MANTYX does not validate the schema's contents. |

Per provider:

| Provider                       | How the schema is enforced |
| ------------------------------ | -------------------------- |
| OpenAI Responses (o-series, GPT-5.x, …) | `text.format = { type: "json_schema", strict: true, name, schema }` on every `completeTurn` (compatible with tool calls). |
| Gemini ≥ 2.5                   | `responseMimeType: "application/json"` + `responseJsonSchema` on no-tools turns (Gemini rejects schemas alongside `functionDeclarations`). |
| Anthropic / Bedrock-Anthropic  | Synthetic `final_report` tool whose `input_schema` is the supplied schema; `tool_choice` is forced on the no-tools finishing turn. The tool's input is surfaced as the assistant text. |
| xAI Grok, others               | Ignored — the model returns plain text. |

Validation (server-side, `400 invalid_request` on violation):

| Constraint                                | Limit |
| ----------------------------------------- | ----- |
| Serialized JSON size of `outputSchema`    | ≤ 32 KB |
| `name` regex                              | `/^[a-zA-Z0-9_-]{1,64}$/` |
| `schema` shape                            | non-`null`, non-array JSON object |

**SDK guidance.** Even though the server enforces JSON shape via the
provider, transient model errors (refusal text, truncation under
`max_tokens` pressure, exotic Unicode normalisation) can still produce
a string that fails to `JSON.parse` in rare cases. Reference SDKs should:

1. Pass the schema through unchanged from the developer's API.
2. `JSON.parse` the terminal `result.data.text`.
3. Re-validate against their source-of-truth Zod / Pydantic / JSON Schema
   validator and surface a typed parse error instead of crashing.

`outputSchema` works for both ephemeral runs (`systemPrompt`-defined) and
`agentId`-backed runs — the runner applies the schema to whichever
`AgentSpec` it built. `outputSchema` is independent of `reasoningLevel`:
the model can think extensively *and* emit JSON.

---

## 8. Cancellation

```http
POST /api/v1/workspaces/{slug}/agent-runs/{runId}/cancel
Authorization: Bearer <api-key>
```

Best-effort: publishes a Valkey signal the runner observes between LLM
turns. The runner aborts cleanly and emits a terminal `cancelled` event.
In-flight `local_tool_call`s are still fulfilled (or time out) before the
final event lands, so SDKs should keep the stream open until they see a
terminal event.

---

## 9. Reconnects & at-least-once delivery

- Every event has a monotonically-increasing `seq` per run, persisted to
  `EphemeralAgentRunEvent`. Reopen with `Last-Event-ID: <seq>` to resume.
- The Valkey pub/sub is best-effort; the persisted log is the source of
  truth. The server occasionally polls the DB during long waits (see
  `bus.ts → waitForLocalToolResult`) so missed publishes still wake the
  runner.
- `local_tool_result_in` is persisted in addition to the live publish, so
  late-joining viewers can replay the SDK's response.
- Tool-result POSTs are idempotent on `toolUseId`: a second POST for the
  same `toolUseId` returns `404 unknown_tool_use` (or `409` if the run
  already ended), it does **not** double-execute the tool.

---

## 10. Full worked example: `a2a_local` round-trip

```ts
import { fetch } from "undici";

// ── 1. Resolve the Agent Card locally ───────────────────────────────────
const cardResp = await fetch("https://hr.intranet.acme/.well-known/agent-card.json", {
  headers: { Authorization: `Bearer ${INTRANET_TOKEN}` },
});
const agentCard = await cardResp.json();   // ← whole document, passed through

// ── 2. Submit the spec ──────────────────────────────────────────────────
const create = await fetch(`${MANTYX}/api/v1/workspaces/${slug}/agent-runs`, {
  method: "POST",
  headers: { "Content-Type": "application/json", Authorization: `Bearer ${apiKey}` },
  body: JSON.stringify({
    modelId: "openai:gpt-5.5",
    systemPrompt: "You can delegate HR questions to the Acme HR agent.",
    prompt: "How many PTO days does Alice have left this year?",
    reasoningLevel: "low",
    tools: [
      { kind: "a2a_local", name: "intranet_hr_agent", agentCard },
    ],
  }),
});
const { runId, streamUrl } = await create.json();

// ── 3. Open the SSE stream and dispatch local_tool_calls ────────────────
const stream = await fetch(streamUrl, {
  headers: { Authorization: `Bearer ${apiKey}`, Accept: "text/event-stream" },
});

for await (const ev of parseSSE(stream)) {
  if (ev.type !== "local_tool_call") continue;
  if (ev.data.kind !== "a2a_local") continue;

  const peer = a2aClients.get(ev.data.agentCard.url);  // ← dispatch by URL
  const reply = await peer.send({ message: ev.data.args.message });

  await fetch(`${MANTYX}/api/v1/workspaces/${slug}/agent-runs/${runId}/tool-results`, {
    method: "POST",
    headers: { "Content-Type": "application/json", Authorization: `Bearer ${apiKey}` },
    body: JSON.stringify({ toolUseId: ev.data.toolUseId, result: reply.text }),
  });
}
```

---

## 11. Full worked example: `mcp_local` round-trip

```ts
// ── 1. Connect + resolve catalog locally ────────────────────────────────
const mcp = new McpClient(stdio("./mcp-server-filesystem"));
const initImpl = await mcp.initialize();         // → { name, version, ... }
const { tools } = await mcp.listTools();         // → MCP Tool[]

// ── 2. Submit the spec ──────────────────────────────────────────────────
const create = await fetch(`${MANTYX}/api/v1/workspaces/${slug}/agent-runs`, {
  method: "POST",
  headers: { "Content-Type": "application/json", Authorization: `Bearer ${apiKey}` },
  body: JSON.stringify({
    modelId: "openai:gpt-5.5",
    prompt: "Tell me what's at /etc/hosts.",
    tools: [
      {
        kind: "mcp_local",
        name: "fs",
        serverInfo: initImpl,
        tools,                                    // ← verbatim from listTools()
      },
    ],
  }),
});
const { runId, streamUrl } = await create.json();

// ── 3. Open SSE and dispatch ────────────────────────────────────────────
for await (const ev of parseSSE(streamFromUrl(streamUrl, apiKey))) {
  if (ev.type !== "local_tool_call") continue;
  if (ev.data.kind !== "mcp_local") continue;

  const result = await mcp.callTool({
    name: ev.data.mcpToolName,                   // identical to ev.data.name
    arguments: ev.data.args,
  });
  const text = result.content
    .filter((b) => b.type === "text")
    .map((b) => b.text)
    .join("\n");

  await fetch(`${MANTYX}/api/v1/workspaces/${slug}/agent-runs/${runId}/tool-results`, {
    method: "POST",
    headers: { "Content-Type": "application/json", Authorization: `Bearer ${apiKey}` },
    body: JSON.stringify({ toolUseId: ev.data.toolUseId, result: text }),
  });
}
```

---

## 12. Compliance checklist for SDK implementers

A reference SDK should:

- [ ] Accept `reasoningLevel` from the caller in either string or number
      form and pass it through unchanged. Do not translate it to a
      vendor-specific knob — the server owns that mapping.
- [ ] Accept `outputSchema` from the caller as `{ name?, schema }` and pass
      it through unchanged. After the run terminates, `JSON.parse` the
      `result.data.text` and re-validate against the caller's
      source-of-truth schema (Zod / Pydantic / etc.) — the server enforces
      JSON shape via the provider, but transient model errors can still
      produce strings that fail to parse in rare cases.
- [ ] Maintain three local-callback registries (or one tagged-union
      registry), keyed by `name`:
      - generic local tools (`kind: "local"`),
      - local A2A peers (`kind: "a2a_local"`, indexed by some Agent Card
        field — typically `agentCard.url`),
      - local MCP servers (`kind: "mcp_local"`, indexed by the SDK-side
        server label that matches `local_tool_call.mcpServer`).
- [ ] For `a2a_local`, **resolve the Agent Card locally** and ship it as
      `agentCard`. Don't expect MANTYX to fetch anything.
- [ ] For `mcp_local`, **speak `Initialize` + `tools/list` locally** and
      ship the verbatim result as `serverInfo` + `tools[]`. Don't expect
      MANTYX to discover anything.
- [ ] On `local_tool_call`, dispatch by the event's `kind` discriminator
      (defaulting to `"local"` when omitted). Validate args against the
      tool's schema, run it, POST the result back to `.../tool-results`.
- [ ] On the terminal `result` / `error` / `cancelled` event, close the
      SSE stream.
- [ ] Idempotency: only POST one tool-result per `toolUseId`. Treat
      `409 run_terminal` as a normal late-arrival outcome (the runner
      timed out).
- [ ] Reconnects: send `Last-Event-ID: <last seq>` to resume, and rely on
      the persisted event log to backfill missed events.

---

## 13. See also

- [`agent-runs-protocol.md`](./agent-runs-protocol.md) — HTTP routes, auth,
  full body shapes, sessions, error codes.
- [A2A spec](https://google.github.io/A2A/specification/) — canonical
  Agent Card schema.
- [MCP spec](https://spec.modelcontextprotocol.io/) — canonical `Tool` and
  `Implementation` shapes.
