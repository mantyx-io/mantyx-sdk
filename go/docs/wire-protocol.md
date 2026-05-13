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

> **Authentication.** Every example below uses
> `Authorization: Bearer <api-key>` for brevity. The same header also
> accepts a MANTYX OAuth 2.0 access token (`mantyx_at_…`) — the server
> resolves either kind by token-prefix, so SDKs only need a single
> credential code path. OAuth tokens additionally enforce per-route
> **scopes** (`runs:read`, `runs:write`, `sessions:read`, `sessions:write`,
> `models:read`, `mantyx.identity:read`); see §2 of
> `agent-runs-protocol.md` for the per-endpoint scope table and
> [`docs/oauth.md`](./oauth.md) for the registration / Authorization Code
> + PKCE flow.

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
  "loopDetection": {                   // optional; see §8
    "consecutiveThreshold": 3,
    "hardCutoffThreshold":  6
  },
  "toolBudgets": {                     // optional; see §8
    "recall":                { "maxCalls": 4 },
    "hive_consult_ontology": { "maxCalls": 4 }
  },
  "metadata": { "customer": "acme" }   // optional, free-form k/v
}
```

### 2.2 Sessions

Same body shape, posted to `POST /agent-sessions/:id/messages`. The session
keeps the conversation history; per-message `tools`, `reasoningLevel`,
`outputSchema`, `loopDetection`, and `toolBudgets` *replace* the session's
defaults for that single run only — the next run falls back to whatever
the session was created with.

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
| `local`          | client     | `{ name, description?, parameters?, outputSchema?, longRunning? }` — `parameters` is **JSON Schema** (object schema with `properties`/`required`); forwarded verbatim to the LLM provider and validated against incoming tool-call args before execution. `outputSchema` (optional) is JSON Schema for the tool's structured return value, surfaced to providers that accept per-tool response schemas. `longRunning` (optional, default `false`) annotates the model-facing description with a "don't double-call while pending" hint so every provider treats the tool as long-running. |
| `a2a`            | server     | `{ name, agentCardUrl, headers?, contextId?, description? }`. |
| `a2a_local`      | client     | `{ name, agentCard }` — **resolved A2A Agent Card JSON content**. |
| `mcp`            | server     | `{ name, url, headers?, toolFilter? }`. |
| `mcp_local`      | client     | `{ name, serverInfo?, tools[] }` — **resolved MCP `Tool[]`**. |

The remainder of this document focuses on `local`, `a2a_local`, and
`mcp_local`, because they're the ones that carry SDK-defined structured
content. For the wire shapes of the four server-resolved kinds, see
`agent-runs-protocol.md` §4.

### 3.1 `kind: "local"` — generic local tools

The minimal client-resolved tool: the SDK declares a name + JSON Schema
and implements the handler in its own process. Useful for any tool MANTYX
shouldn't (or can't) execute itself — file system access, on-device APIs,
caller-specific business logic.

**Wire shape:**

```jsonc
{
  "kind": "local",
  "name": "send_email",                 // model-facing; /^[a-zA-Z0-9_]{1,64}$/
  "description": "Send a transactional email.",
  "parameters": {                       // OPTIONAL; JSON Schema for args
    "type": "object",
    "properties": {
      "to":      { "type": "string", "format": "email" },
      "subject": { "type": "string" },
      "body":    { "type": "string" }
    },
    "required": ["to", "subject", "body"],
    "additionalProperties": false
  },
  "outputSchema": {                     // OPTIONAL; JSON Schema for the return value
    "type": "object",
    "properties": { "id": { "type": "string" } },
    "required": ["id"],
    "additionalProperties": false
  },
  "longRunning": false                  // OPTIONAL; default false
}
```

**Field reference:**

| Field          | Required | Notes |
| -------------- | -------- | ----- |
| `kind`         | yes      | Discriminator literal `"local"`. |
| `name`         | yes      | Model-facing tool name. Must match `/^[a-zA-Z0-9_]{1,64}$/`. |
| `description`  | no       | Free-form. When omitted the model sees an empty description (acceptable but reduces tool selection accuracy). |
| `parameters`   | no       | JSON Schema for the tool's input. Must be an object schema (`type: "object"` with `properties`); other shapes are coerced to an empty object schema server-side. Nested constraints (`array.items`, `enum`, `anyOf`, …) are preserved end-to-end. Args that fail server-side validation produce a structured `tool_input_invalid` tool result the model can recover from instead of crashing the call. |
| `outputSchema` | no       | JSON Schema for the structured value the tool returns. Forwarded to providers with per-tool response schemas (Gemini's `responseJsonSchema` on the FunctionDeclaration); other engines surface it through the description and rely on host-side validation. The model uses it to plan follow-up arguments more reliably. Must be an object schema; non-object roots are dropped server-side (engines reject non-object roots in this position). |
| `longRunning`  | no       | When `true`, MANTYX appends a stable hint to the description:<br>*"NOTE: This is a long-running operation. Do not call this tool again if it has already returned an intermediate or pending status."*<br>Useful for tools where a single call may yield a `pending` / status response and the SDK polls on its own; without the hint, models routinely fire repeat calls and waste turns. Pure declarative — MANTYX does not change scheduling. |

**Tool call dispatch.** When the model calls a `local` tool, the SSE
stream emits `local_tool_call` with `kind: "local"` (or omitted, for
backward compatibility). The SDK runs the handler and POSTs back to
`.../tool-results`. See §4.3.1 for the event shape.

### 3.2 `a2a_local` — SDK-resolved Agent Card

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

### 3.3 `mcp_local` — SDK-resolved Tool catalog

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
| `tools[].inputSchema`    | LLM tool-call schema | Forwarded **verbatim** to the LLM provider as the tool's JSON Schema, then validated against incoming tool-call args (Ajv) before execution. Nested constraints (`array.items`, `enum`, `anyOf`, …) are preserved end-to-end. Empty / missing schema → no-arg tool. Args that violate the schema produce a structured `tool_input_invalid` tool result the model can recover from instead of crashing the tool. |
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
| `loop_detected`         | M → SDK   | 0–2× per run (soft nudge + optional hard cutoff) | Observability for the loop-detection guard (see §8). The server already substituted the synthetic skip + steering nudge — SDK clients render a status note (`looping — nudged` / `looping — gave up`) and otherwise leave the run alone. |
| `tool_budget_exceeded`  | M → SDK   | Per intercepted tool call | Observability for per-tool call budgets (see §8). The synthetic `tool_result` carrying the "budget exceeded — pivot or finalize" body lands on the normal tool-result channel; this event is purely so SDK clients can surface a UI banner. |
| `assistant_message`     | M → SDK   | 1× per turn | Final assistant message for the turn (concatenated, persistence-ready). |
| `result`                | M → SDK   | 1× terminal | Successful completion. Carries the final assistant text and run summary. |
| `error`                 | M → SDK   | 1× terminal | Failure. Carries `error` (message), `code` / `errorClass` (category), `finishReason`, and an optional `partialText` salvage payload. See §4.7. |
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
  "data": {
    "text": "Here's what I found...",
    "turn": 0,
    "finishReason": "tool_use",       // optional; canonical lowercase token
    "toolCalls": [                    // optional; absent when the turn was text-only
      { "id": "call_abc", "name": "search", "input": { /* JSON Schema-matching args */ } }
    ]
  }
}
```

| Field            | Type     | Required | Notes |
| ---------------- | -------- | -------- | ----- |
| `text`           | string   | yes      | Full assistant text for this turn (concatenation of every preceding `assistant_delta` for this turn, plus any non-streaming snapshot the engine appended at close). May be empty when the turn was tool-only. |
| `turn`           | integer  | yes      | 0-based tool-turn index this assistant message closes. Useful for SDK clients pairing the message with the subsequent `tool_result` rows. |
| `finishReason`   | string\|null | no   | Canonical lowercase stop reason normalized across providers (`"end_turn"`, `"tool_use"`, `"max_tokens"`, `"refusal"`, `"malformed_function_call"`, …). Pulled from the engine's per-turn `stopReason` after normalization — Gemini's `MAX_TOKENS` lands as `"max_tokens"`, OpenAI's `length` lands as `"max_tokens"`, etc. `null` / omitted when the provider did not report one. |
| `toolCalls`      | array    | no       | Tool calls the model emitted on this turn (id, sanitized pipeline-side name, JSON-matching `input`). Omitted when the model did not call any tools. |

**Emission frequency.** Exactly **one** `assistant_message` per completed
assistant turn — including the last turn before a terminal `error`. SDK
clients should treat this as the canonical "the model said something" anchor
and avoid stitching a turn out of `assistant_delta` chunks themselves
(deltas may be split arbitrarily for transport).

**Truncation behaviour.** When the run terminates with `error` (e.g.
Gemini `MAX_TOKENS` while emitting `outputSchema` JSON), the last
`assistant_message` preceding the `error` carries the partial text plus
`finishReason: "max_tokens"`. The terminal `error` event then carries the
*same* text on `data.partialText` so reconnect / replay sees both pieces
without depending on event ordering.

### 4.5 `loop_detected`

```jsonc
// soft nudge — pipeline injected a "finalize OR change strategy" user message
{ "seq": 13, "type": "loop_detected",
  "data": { "consecutiveCount": 3, "hardCutoff": false, "tools": ["recall"] } }

// hard cutoff — pipeline forced a tools-disabled finalise turn
{ "seq": 27, "type": "loop_detected",
  "data": { "consecutiveCount": 6, "hardCutoff": true,  "tools": ["recall"] } }
```

| Field              | Type    | Notes |
| ------------------ | ------- | ----- |
| `consecutiveCount` | integer | Length of the identical-batch streak that just tripped the threshold (`>= consecutiveThreshold`). |
| `hardCutoff`       | boolean | `false` for the soft nudge round; `true` once the pipeline forces finalisation. The SDK may see one of each in a single run. |
| `tools`            | array   | Names of the tool calls in the looping batch (no args — those are persisted on the matching `tool_result` events). |

Observability only: the synthetic skip + steering nudge are emitted on the
normal `tool_result` and assistant-message channels by the time this event
fires. SDK clients should render a status note (`looping — nudged` /
`looping — gave up`) and otherwise leave the run alone — the run still
continues to its terminal `result` / `error` / `cancelled`.

See §8 for the wire-spec field that controls thresholds.

### 4.6 `tool_budget_exceeded`

```jsonc
{ "seq": 14, "type": "tool_budget_exceeded",
  "data": { "tool": "recall", "maxCalls": 4, "callIndex": 5 } }
```

| Field       | Type    | Notes |
| ----------- | ------- | ----- |
| `tool`      | string  | Logical tool name as the model saw it (matches the key in `spec.toolBudgets`). |
| `maxCalls`  | integer | Configured cap. |
| `callIndex` | integer | 1-based count of attempts to call this tool over the run lifetime; always strictly greater than `maxCalls`. |

Observability only: the synthetic "budget exceeded — pivot or finalize"
tool-result lands on the normal `tool_result` channel before this event
fires, so the model already has the directive to pivot. SDK clients use
this event to render UI banners (`memory budget exhausted`, etc.) without
re-parsing tool-result bodies.

See §8 for the wire-spec field that defines budgets.

### 4.7 Terminal events

```jsonc
{ "seq": 14, "type": "result",    "data": { "ok": true,  "text": "..." } }
{ "seq": 14, "type": "error",     "data": {
    "error": "Model output was truncated (stop_reason=max_tokens). …",
    "code":         "truncation",     // mirrors `errorClass`; legacy alias
    "errorClass":   "truncation",     // canonical category (see below)
    "finishReason": "max_tokens",     // canonical lowercase stop reason
    "partialText":  "{\n  \"answer\":… (truncated JSON) …",
    "retryable":    false              // optional; per-class retry hint
} }
{ "seq": 14, "type": "cancelled", "data": { "reason": "user" } }
```

After one of these arrives, no further events will be emitted; close the
SSE stream.

**`error` event payload fields.** The runner enriches the `error` event
with structured triage attributes when the failure carried a salvage path
(typically truncation, upstream deadline, or max-budget-with-text):

| Field          | Type     | Required | Notes |
| -------------- | -------- | -------- | ----- |
| `error`        | string   | yes      | Human-readable message (also persisted on `EphemeralAgentRun.error`). |
| `code`         | string   | yes      | Legacy alias for `errorClass`. Equals `errorClass` when present; otherwise a small lowercase token (`"error"`, `"invalid_spec"`, `"worker_error"`, …) the SDK can switch on. |
| `errorClass`   | string   | no       | Canonical category. One of `"rate_limit"`, `"overloaded"`, `"server"`, `"context_window"` (input too big), `"truncation"` (output budget exhausted), `"invalid_request"`, `"auth"`, `"timeout"`, `"local_timeout"`, `"upstream_deadline"`, `"unknown"`. New categories may land additively. |
| `finishReason` | string\|null | no   | Canonical lowercase stop reason normalized across providers (`"max_tokens"`, `"refusal"`, `"malformed_function_call"`, …). When present, mirrors the value on the last `assistant_message`. |
| `partialText`  | string   | no       | **Best-effort raw bytes** the model emitted before the failure. For `outputSchema` runs this is likely **incomplete JSON** that will fail `JSON.parse` — see §7 below. Also persisted on `EphemeralAgentRun.finalText` so the Calls UI can render it alongside a truncation banner. |
| `retryable`    | boolean  | no       | Coarse retry hint inherited from the pipeline's error classifier. Informational; the SDK still owns the actual retry decision. |

When `errorClass` is `"truncation"`, the `EphemeralAgentRun` row that the
SDK can re-fetch via `GET /agent-runs/:runId` will have:

| Field           | Value |
| --------------- | ----- |
| `status`        | `"failed"` |
| `finalText`     | Same string as `data.partialText` (so SDKs can ignore the SSE stream and still recover the salvage). |
| `error`         | Same string as `data.error`. |
| `failureReason` | `{ "errorClass": "truncation", "finishReason": "max_tokens" }` (JSON object, future-proof for additional triage fields). |

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
| Gemini 3+ (any turn)           | `responseMimeType: "application/json"` + `responseJsonSchema` on every `completeTurn`. Gemini 3 accepts the schema alongside `functionDeclarations`. |
| Gemini ≤ 2.5 with no tools     | Same as Gemini 3+: `responseMimeType: "application/json"` + `responseJsonSchema`. |
| Gemini ≤ 2.5 **with tools**    | Synthetic `set_model_response` function declaration is injected; its `parametersJsonSchema` is the supplied schema. The system instruction is augmented to direct the model to call this tool with the final answer. The engine intercepts the call, hides it from the SDK, and surfaces the call's arguments as the assistant text (JSON-stringified). Sidesteps the API rejection ("Function calling with a response mime type: 'application/json' is unsupported") without round-tripping a 4xx. |
| Anthropic / Bedrock-Anthropic  | Synthetic `final_report` tool whose `input_schema` is the supplied schema; `tool_choice` is forced on the no-tools finishing turn. The tool's input is surfaced as the assistant text. |
| xAI Grok, others               | Ignored — the model returns plain text. |

The synthetic-tool paths (Gemini 2.5 + tools, Anthropic) are entirely
internal: the SDK still receives `data.text: string` on the terminal
`result` event and never sees a `local_tool_call` for `set_model_response`
or `final_report`. They never appear in the tools array the SDK declared.

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

**Truncation contract.** When the model is mid-JSON and Gemini /
Anthropic / OpenAI hit the output budget, MANTYX does **not** discard the
bytes that already streamed. Instead:

1. The last `assistant_message` for the turn (§4.4) carries the partial
   text plus `finishReason: "max_tokens"`.
2. The terminal SSE event is an `error` (not `result`) with
   `errorClass: "truncation"` and `data.partialText` set to the same
   bytes (§4.7).
3. The run row exposes the salvage on
   `GET /agent-runs/:runId` as `{ status: "failed", finalText: "<partial JSON>",
   error: "Model output was truncated …", failureReason: { errorClass:
   "truncation", finishReason: "max_tokens" } }`.

`partialText` is a **best-effort raw byte sequence** — for `outputSchema`
runs it will almost always fail `JSON.parse` because the JSON object was
not closed. SDKs should treat it as diagnostic data, never as a
schema-conformant reply. Surfacing it (as a "truncated reply — JSON
likely incomplete" status note) is the recommended pattern; silently
falling back to it as the answer is not.

`outputSchema` works for both ephemeral runs (`systemPrompt`-defined) and
`agentId`-backed runs — the runner applies the schema to whichever
`AgentSpec` it built. `outputSchema` is independent of `reasoningLevel`:
the model can think extensively *and* emit JSON.

---

## 8. Run guards (`loopDetection`, `toolBudgets`)

Two opt-in (default-on) fields on the spec body govern how MANTYX guards
against tight tool loops and runaway research-tool usage. Both are
**additive over the wire** — older SDKs that don't ship them keep working,
and the runtime defaults still apply server-side.

### 8.1 `loopDetection`

The pipeline tracks an order-invariant canonical signature for every
assistant turn that emits one or more tool calls. When the same signature
repeats consecutively the guard intervenes:

| Trigger                                            | Server action |
| -------------------------------------------------- | ------------- |
| `consecutiveThreshold` identical batches in a row | Skip the duplicate batch with a synthetic "you've made this exact call before" tool result, prepend a user-style **steering nudge** ("either deliver a final answer or change strategy") before the next model turn. |
| `hardCutoffThreshold` identical batches in a row  | Force a tools-disabled finalise turn (same path as `budgets.maxToolTurnsExceeded: "finalize"`) so the run lands cleanly. |

```jsonc
"loopDetection": {
  "consecutiveThreshold": 3,    // optional, default 3 — fires the steering nudge
  "hardCutoffThreshold":  6     // optional, default 6 — forces finalisation
}

// or:
"loopDetection": false          // explicitly disable for this run
```

| Field                  | Type            | Notes |
| ---------------------- | --------------- | ----- |
| `consecutiveThreshold` | integer ≥ 2     | Default `3`. Single batch = single tool call, not a loop, so the floor is `2`. |
| `hardCutoffThreshold`  | integer ≥ 3     | Default `6`. Must be **strictly greater** than `consecutiveThreshold` (otherwise the soft nudge never gets a chance). |
| (top-level `false`)    | literal `false` | Disables the guard. `budgets.maxToolTurns` still applies. |

Validation (server-side, `400 invalid_request` on violation): both
thresholds capped at `100`; `hardCutoffThreshold` must exceed
`consecutiveThreshold`.

The runtime default — applied when the field is omitted — is
`{ consecutiveThreshold: 3, hardCutoffThreshold: 6 }`. SDK-driven runs and
platform-driven runs inherit identical defaults.

### 8.2 `toolBudgets`

Per-tool call caps enforced over the **lifetime of the run** (across every
LLM turn). Calls under the cap run normally; calls past the cap are
intercepted **before execution** and the model receives a synthetic
"budget exceeded — pivot or finalize" tool result. The model stays in the
loop and either changes strategy or finalises.

```jsonc
"toolBudgets": {
  "recall":                { "maxCalls": 4 },
  "hive_consult_ontology": { "maxCalls": 4 },
  "traverse":              { "maxCalls": 3 },
  "scary_tool":            { "maxCalls": 0 }     // disables the tool for this run
}
```

| Field      | Type        | Notes |
| ---------- | ----------- | ----- |
| `<key>`    | string (1–120 chars) | Logical tool name as the model sees it (`ResolvedTool.name`). The SDK + pipeline handle internal sanitisation. |
| `maxCalls` | integer ≥ 0 | Hard cap. `0` disables the tool entirely (the first attempt returns the synthetic body). |

Budgets are **per-tool, not pooled** — `hive_search_deals: { maxCalls: 5 }`
and `hive_search_meetings: { maxCalls: 5 }` give the agent five of each,
not five between them.

Validation (server-side, `400 invalid_request` on violation):

| Constraint            | Limit |
| --------------------- | ----- |
| Max entries           | `32`  |
| `<key>` length        | `1..120` |
| `maxCalls` upper bound | `1000` (functionally unlimited; `maxToolTurns: 100` fires first) |

**Default budgets** (applied when the field is omitted; caller-provided
entries are layered on top so per-run overrides win):

| Tool                                                                                             | Default `maxCalls` |
| ------------------------------------------------------------------------------------------------ | ------------------ |
| `recall` (workspace memory hybrid search)                                                        | `4` |
| `traverse` (memory graph BFS)                                                                    | `3` |
| `hive_consult_ontology` (per-hive ontology read; same name across all three hives)               | `4` |
| `hive_search_deals` / `_meetings` / `_companies` / `_people` (Sales Hive general search)         | `5` |
| `hive_search_tickets` / `_conversations` / `_accounts` (Customer Hive general search)            | `5` |
| `hive_search_releases` / `_issues` (Product Hive general search)                                 | `5` |

Pass `"toolBudgets": {}` to start from a clean slate (no defaults applied
on top — useful for runs that intentionally want unbounded research). When
both the caller and the runtime defaults specify a budget for the same
tool, **the caller's value wins**.

### 8.3 Observability

Each intervention emits a SSE event so SDK clients can render UI status
banners without re-parsing tool-result bodies:

- `loop_detected` — fired on the soft nudge and again on the hard cutoff
  if reached. See §4.5.
- `tool_budget_exceeded` — fired each time a call is intercepted. See §4.6.

Both events are observability-only: the server has already substituted
the synthetic tool-result / steering nudge by the time the SDK sees the
event. The run continues to its terminal `result` / `error` / `cancelled`
as usual.

### 8.4 Session inheritance

Like `reasoningLevel` and `outputSchema`, both fields support
session-default + per-message override:

- `POST /agent-sessions { loopDetection, toolBudgets }` — sets the
  session-default applied to every subsequent message run.
- `POST /agent-sessions/:id/messages { loopDetection, toolBudgets }` —
  optional per-message override. Applies to that one run only and does
  not mutate the session's stored value.

---

## 9. Cancellation

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

## 10. Reconnects & at-least-once delivery

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

## 11. Full worked example: `a2a_local` round-trip

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

## 12. Full worked example: `mcp_local` round-trip

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

## 13. Compliance checklist for SDK implementers

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
- [ ] Accept `loopDetection` and `toolBudgets` from the caller and pass
      them through unchanged (see §8). Both are *additive* — omitting
      them keeps the runtime defaults; passing `loopDetection: false` opts
      out; passing `toolBudgets: {}` clears the defaults; passing entries
      layers caller overrides on top of the defaults. Do **not** translate
      to vendor-specific knobs.
- [ ] Treat `loop_detected` and `tool_budget_exceeded` SSE events as
      observability-only (see §4.5 / §4.6). Surface them as status notes
      / log lines / telemetry — the server already substituted the
      synthetic tool-results / steering nudges, so the SDK should keep
      consuming the stream until the terminal event lands.
- [ ] Maintain three local-callback registries (or one tagged-union
      registry), keyed by `name`:
      - generic local tools (`kind: "local"`),
      - local A2A peers (`kind: "a2a_local"`, indexed by some Agent Card
        field — typically `agentCard.url`),
      - local MCP servers (`kind: "mcp_local"`, indexed by the SDK-side
        server label that matches `local_tool_call.mcpServer`).
- [ ] For `kind: "local"`, accept developer-supplied `parameters` (Zod /
      JSON Schema) and serialize to JSON Schema before submission. When the
      caller declares an output schema, forward it as `outputSchema` (same
      JSON Schema shape) so providers with per-tool response schemas can
      enforce it. Surface a `longRunning` flag on the tool builder so the
      caller can opt into the model-side "don't double-call" hint without
      hand-editing the description.
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

## 14. See also

- [`agent-runs-protocol.md`](./agent-runs-protocol.md) — HTTP routes, auth,
  full body shapes, sessions, error codes.
- [A2A spec](https://google.github.io/A2A/specification/) — canonical
  Agent Card schema.
- [MCP spec](https://spec.modelcontextprotocol.io/) — canonical `Tool` and
  `Implementation` shapes.
