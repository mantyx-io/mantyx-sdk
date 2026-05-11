# Agent Runs — wire protocol

This document specifies the public wire protocol that the MANTYX agent-runs API
speaks with SDKs. It is the source of truth for anyone implementing a new
client (Python, Rust, Java…) and is shipped with each first-party SDK so the
SDK repository can stand on its own when it is extracted from this monorepo.

Companion documents:

- [`docs/wire-protocol.md`](./wire-protocol.md) — the messaging-layer
  reference: every SSE event payload, the SDK-side dispatcher pattern, and
  the resolved data structures (`a2a_local` Agent Card, `mcp_local`
  `Tool[]`) the SDK is expected to ship.
- [`docs/agent-runs.md`](./agent-runs.md) — server-side overview, internals,
  deployment notes.

## 1. Concepts

**Ephemeral agent.** A run-time agent that is *defined by the request* rather
than persisted as a row in MANTYX's `Agent` table. The full spec (system
prompt, model, tools) is stored as part of each session/run for observability
but is not editable from the dashboard.

**Tool refs.** Seven flavours, all carried inside the agent spec's `tools`
array:

| `kind`           | Resolved by | Notes |
| ---------------- | ----------- | ----- |
| `mantyx`         | server      | A workspace `Tool` row referenced by id (HTTP / Code / Plugin). |
| `mantyx_plugin`  | server      | A platform plugin tool referenced by name. |
| `local`          | client      | A custom tool defined and executed in the SDK's process. Carries `parameters` (input JSON Schema) plus optional `outputSchema` (return-value JSON Schema) and `longRunning` flag — see §4.1.1. |
| `a2a`            | server      | A *remote* Agent2Agent peer MANTYX can reach; invoked via `message/send` and the reply is surfaced as the tool result. |
| `a2a_local`      | client      | An A2A peer MANTYX **cannot** reach. SDK resolves the [Agent Card](https://google.github.io/A2A/specification/#agent-card) locally and ships it inline; MANTYX uses it for the model description and routes calls back to the SDK over SSE. |
| `mcp`            | server      | A *remote* MCP server (Streamable HTTP). At run start MANTYX lists the catalog and exposes every tool as `<server>_<tool>` (subject to `toolFilter`). |
| `mcp_local`      | client      | An MCP server MANTYX **cannot** reach. SDK runs `Initialize` + `tools/list` locally and ships the resolved `Tool[]` (with `inputSchema`); MANTYX exposes them to the model with the SDK-declared names and routes calls back over SSE. |

The split is deliberate:

- **Server-resolved** (`mantyx`, `mantyx_plugin`, `a2a`, `mcp`) — MANTYX has
  network access to the resource. The worker runs the tool itself and the
  SDK only sees an informational `tool_result` event in the SSE stream. For
  MCP/A2A this also means MANTYX does discovery (`listTools`, agent-card
  fetch).
- **Client-resolved / "local"** (`local`, `a2a_local`, `mcp_local`) —
  MANTYX has *no* access to the resource. The SDK does **all** of the
  work: connection, discovery, listing, expansion, arg validation, auth,
  execution, retries. MANTYX is a thin LLM-routing layer that emits a
  `local_tool_call` event and blocks until the SDK POSTs back to
  `.../tool-results`. The event payload carries a `kind` discriminator
  (`"local"` implied when absent, `"a2a_local"` and `"mcp_local"` explicit)
  so SDKs can dispatch to the right local handler.

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
  "reasoningLevel": "medium",           // optional, see §4.4
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
      },
      "outputSchema": {                 // optional — JSON Schema for the return value
        "type": "object",
        "properties": {
          "bytes": { "type": "string", "description": "UTF-8 file contents" }
        },
        "required": ["bytes"]
      },
      "longRunning": false              // optional — default false
    },
    {
      "kind": "a2a",
      "name": "billing_agent",
      "description": "Delegate billing questions to the Acme billing agent.",
      "agentCardUrl": "https://billing.acme.com/.well-known/agent-card.json",
      "headers": { "Authorization": "Bearer ${BILLING_TOKEN}" },
      "contextId": "ctx_abc"            // optional A2A context to thread turns
    },
    {
      "kind": "a2a_local",
      "name": "intranet_hr_agent",
      "agentCard": {                    // SDK-resolved A2A Agent Card content
        "protocolVersion": "0.3.0",
        "name": "Acme HR",
        "description": "Answers questions about HR policies and benefits.",
        "url": "https://hr.intranet.acme/a2a",
        "version": "1.4.0",
        "capabilities": { "streaming": false },
        "skills": [
          {
            "id": "pto_lookup",
            "name": "PTO lookup",
            "description": "Find a teammate's remaining PTO days for the year."
          },
          {
            "id": "benefits_qa",
            "name": "Benefits Q&A",
            "description": "Answer questions about insurance, 401k, and parental leave."
          }
        ]
      }
    },
    {
      "kind": "mcp",
      "name": "github",                 // → tools become github_<tool>
      "url": "https://mcp.github.com/v1",
      "headers": { "Authorization": "Bearer ${GH_PAT}" },
      "toolFilter": ["search_repos", "read_file"]   // optional allowlist
    },
    {
      "kind": "mcp_local",
      "name": "fs",                     // SDK-side server label only — NOT a prefix
      "serverInfo": {                   // optional; from MCP Initialize
        "name": "mcp-server-filesystem",
        "version": "0.4.1"
      },
      "tools": [                        // verbatim MCP tools/list response
        {
          "name": "fs_read_file",       // model-facing name, exactly as declared
          "description": "Read a file from the user's workstation",
          "inputSchema": {              // MCP's term — JSON Schema
            "type": "object",
            "properties": { "path": { "type": "string" } },
            "required": ["path"]
          }
        }
      ]
    }
  ],
  "budgets": { "maxToolTurns": 32 },    // optional safety cap
  "outputSchema": {                     // optional, see §4.5
    "name": "weather_report",
    "schema": {
      "type": "object",
      "properties": {
        "city": { "type": "string" },
        "temperature_c": { "type": "number" }
      },
      "required": ["city", "temperature_c"]
    }
  },
  "loopDetection": {                    // optional, see §4.6
    "consecutiveThreshold": 3,
    "hardCutoffThreshold": 6
  },
  "toolBudgets": {                      // optional, see §4.7
    "recall":                { "maxCalls": 4 },
    "hive_consult_ontology": { "maxCalls": 4 },
    "scary_tool":            { "maxCalls": 0 }
  },
  "metadata": {                         // optional, see §4.8
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

### 4.1.1 `kind: "local"` — generic local tools

The minimal client-resolved tool: the SDK declares the contract and runs
the handler in its own process. MANTYX never executes the body — it
emits a `local_tool_call` event when the model picks the tool and waits
for the SDK to POST a tool-result.

| Field          | Required | Notes |
| -------------- | -------- | ----- |
| `kind`         | yes      | Discriminator literal `"local"`. |
| `name`         | yes      | Model-facing tool name. Must match `/^[a-zA-Z0-9_]{1,64}$/`. |
| `description`  | no       | Free-form. Empty when omitted (acceptable, but reduces tool-selection accuracy). |
| `parameters`   | no       | JSON Schema for the tool's input. Must be a `type: "object"` schema with `properties`; non-object roots are coerced to an empty object schema server-side. Forwarded **verbatim** to the LLM provider so nested constraints (`array.items`, `enum`, `anyOf`, numeric formats, …) survive. Args that fail server-side validation produce a structured `tool_input_invalid` tool result the model can recover from instead of crashing the call. |
| `outputSchema` | no       | JSON Schema for the structured value the tool returns. When present, forwarded to providers that accept per-tool response schemas (Gemini's `responseJsonSchema` on the FunctionDeclaration); other engines surface it through the description and rely on host-side validation. Helps the model emit follow-up arguments that round-trip cleanly. Must be an object schema; non-object roots are dropped server-side. |
| `longRunning`  | no       | When `true`, MANTYX appends a stable hint to the model-facing description so every provider treats the tool as long-running:<br>*"NOTE: This is a long-running operation. Do not call this tool again if it has already returned an intermediate or pending status."*<br>Useful for tools that return `pending` and rely on SDK-side polling — without the hint the model routinely fires repeat calls and burns turns. Pure declarative — MANTYX does not change scheduling. |

The `outputSchema` and `longRunning` fields are **additive** since wire
protocol v1: SDKs that don't ship them keep working unchanged. Providers
without per-tool response-schema support (OpenAI, Anthropic, Bedrock,
Grok) accept the new fields silently — the schema is treated as a
description hint and host-side validation still runs.

### 4.2 A2A tool refs

A2A delegation lets the agent hand a task to another
[Agent2Agent](https://google.github.io/A2A/) peer. The wire protocol exposes
two kinds depending on **who can reach the peer**:

- `kind: "a2a"` — *remote* (server-resolved). MANTYX dials `agentCardUrl`
  directly. Pick this when the peer is on the public internet or in the
  same VPC as MANTYX.
- `kind: "a2a_local"` — *local* (client-resolved). The SDK invokes the peer
  on its side and posts back the reply. Pick this when the peer lives on an
  intranet, behind a VPN, or on the user's device — anywhere MANTYX can't
  reach but the SDK can.

Both kinds present the **same** `{ "message": string }` argument shape to
the model, so an agent prompt that uses one transparently works with the
other. (This also matches MANTYX's internal `delegate_to_<name>` tools, so
models trained on one pattern carry across.)

#### `kind: "a2a"` — remote A2A

MANTYX resolves the tool server-side: when the model calls it, the worker
POSTs the model's `message` argument to `agentCardUrl` over A2A's standard
`message/send` RPC (Google ADK JSON-RPC root, A2A `/rpc`, `/message:send`,
and `/message/send` endpoints are probed in order) and forwards the remote
agent's text reply back as the tool result.

| Field           | Required | Notes |
| --------------- | -------- | ----- |
| `kind`          | yes      | Discriminator literal `"a2a"`. |
| `name`          | yes      | Tool name surfaced to the model — must match `/^[a-zA-Z0-9_]{1,64}$/`. |
| `description`   | no       | Model-facing description. Defaults to `"Delegate a task to the <name> agent over A2A. Pass the full task as a single message."`. Mention the remote agent's purpose so the model picks it for the right turn. |
| `agentCardUrl`  | yes      | URL of the remote Agent Card (`/.well-known/agent-card.json`) or the JSON-RPC root the peer accepts. |
| `headers`       | no       | Flat string→string HTTP headers sent on every A2A request — typically `Authorization`. Each value is capped at 8 KB. |
| `contextId`     | no       | A2A `contextId` to thread multiple delegations into the same remote conversation. Omit for fresh per-call context. |

> **Secret handling.** `headers` are forwarded **as-is** by the SDK API. If
> you need long-lived credentials (refresh tokens, rotating API keys),
> register the peer as a workspace `ExternalAgent` instead — those headers
> support `{{secret:NAME}}` resolution against the workspace secrets store
> (see `runtime/a2a-client.ts`). The wire-protocol `a2a` ref is best for
> short-lived per-run tokens minted by your application.

#### `kind: "a2a_local"` — local A2A

> **MANTYX does no A2A work for this kind.** It does not fetch the agent
> card, validate transport, manage credentials, or speak `message/send`.
> The SDK owns the entire A2A relationship; MANTYX merely translates the
> model's `delegate_to_<name>` call into a `local_tool_call` event and
> waits for the SDK to POST back the reply text.

Per-run lifecycle:

1. **Resolution (SDK).** Before submitting the spec, the SDK obtains the
   peer's [A2A Agent Card](https://google.github.io/A2A/specification/#agent-card)
   — typically by fetching `/.well-known/agent-card.json` from the local
   peer, or by reading it from a config file / registry / inline constant.
2. **Submission (SDK → MANTYX).** SDK posts the spec with the resolved
   card embedded as `agentCard`. MANTYX uses the card's `name`,
   `description`, and `skills[]` to compose the model-facing tool
   description so the LLM understands what the peer can do.
3. **Tool call (MANTYX → SDK).** When the model calls the tool, MANTYX
   emits a `local_tool_call` event with `kind: "a2a_local"`,
   `args: { message: string }`, and the **full `agentCard`** echoed back
   so the SDK can route to the right local A2A handler (matching by URL,
   name, skill set, or any other field).
4. **Execution (SDK).** SDK invokes the A2A peer (its own client, its own
   credentials, its own retries) and POSTs the reply text to
   `POST /agent-runs/:runId/tool-results`.
5. **Continuation (MANTYX).** MANTYX feeds the reply back into the model
   loop as the tool result.

| Field           | Required | Notes |
| --------------- | -------- | ----- |
| `kind`          | yes      | Discriminator literal `"a2a_local"`. |
| `name`          | yes      | Tool name surfaced to the model — must match `/^[a-zA-Z0-9_]{1,64}$/`. |
| `description`   | no       | Model-facing description override. When omitted, MANTYX synthesizes one from `agentCard.name`, `agentCard.description`, and the first 12 skills. |
| `agentCard`     | yes      | The resolved A2A Agent Card (JSON content). Schema follows the [A2A Agent Card spec](https://google.github.io/A2A/specification/#agent-card) — passthrough for unknown fields, so any spec-compliant card works. See the *Agent Card shape* table below for the fields MANTYX actually reads. |

**Agent Card shape** (only the fields MANTYX inspects; everything else is
forwarded verbatim back to the SDK):

| Card field            | Used by MANTYX | Notes |
| --------------------- | -------------- | ----- |
| `protocolVersion`     | echo only      | A2A protocol version (e.g. `"0.3.0"`). |
| `name`                | description    | Used when synthesizing the tool description (`"Delegate a task to the <name> agent ..."`). |
| `description`         | description    | One-paragraph summary of what the peer does — surfaced to the model. |
| `url`                 | echo only      | Peer's A2A endpoint. Forwarded back to the SDK in the `local_tool_call` event so the SDK can dispatch by URL. Never fetched server-side. |
| `version`             | echo only      | Peer agent version. |
| `provider`            | echo only      | Vendor info. |
| `capabilities`        | echo only      | A2A capability flags (streaming, push notifications, …). |
| `defaultInputModes`   | echo only      | Modalities the peer accepts. |
| `defaultOutputModes`  | echo only      | Modalities the peer returns. |
| `skills[]`            | description    | First 12 skills (`name`, `description`) are bulleted into the tool description so the model knows what to ask for. |
| `securitySchemes`, `security` | echo only | Forwarded to the SDK; MANTYX does no auth. |
| *anything else*       | echo only      | Passthrough — survives round-trip unchanged. |

Local A2A respects the same `localToolTimeoutMs` budget (default 5 minutes)
as `kind: "local"`. Tool-result POSTs after timeout return `409 run_terminal`.

### 4.3 MCP tool refs

[Model Context Protocol](https://modelcontextprotocol.io/) connectors
expose every tool published by an MCP server to the agent loop in one go.
Like A2A, the protocol distinguishes by **where the server lives**:

- `kind: "mcp"` — *remote* MCP (Streamable HTTP). MANTYX has network access
  to the server, dials it, lists the catalog at run start, and proxies each
  call server-side. **MANTYX prefixes every discovered tool name with the
  ref's `name`** (e.g. `github_search_repos`) so multiple MCP servers
  can coexist without colliding.
- `kind: "mcp_local"` — *local* MCP (stdio, on-device, intranet). MANTYX
  has **no** access to the server; the SDK does discovery, validation, and
  execution. The SDK declares the tool catalog with **the exact names it
  wants the model to see** — MANTYX does not auto-prefix.

#### `kind: "mcp"` — remote MCP

| Field          | Required | Notes |
| -------------- | -------- | ----- |
| `kind`         | yes      | Discriminator literal `"mcp"`. |
| `name`         | yes      | Server label — MANTYX prefixes every discovered tool name as `<name>_<tool>`. Must match `/^[a-zA-Z0-9_]{1,64}$/`. |
| `url`          | yes      | Streamable HTTP MCP endpoint. |
| `headers`      | no       | Flat string→string HTTP headers (e.g. `Authorization`). Each value capped at 8 KB. |
| `toolFilter`   | no       | Allowlist of MCP tool names (un-prefixed, as the server returns them). When set, tools not in the list are silently dropped. When omitted, every published tool is exposed. |

If the MCP server is unreachable when the run starts, MANTYX still exposes
a single stub tool named `<server>_unavailable` so the model can report the
failure to the user instead of silently going without the catalog.

#### `kind: "mcp_local"` — local MCP

> **MANTYX does no MCP work for this kind.** It does not speak
> `Initialize`, `tools/list`, or `tools/call`, does not validate args,
> and does not interpret result content blocks. The SDK owns the entire
> MCP relationship — including discovery — and gives MANTYX the resolved
> tool catalog so the model can be told what's available. MANTYX is
> purely a transport.

Per-run lifecycle:

1. **Discovery (SDK).** Before submitting the spec, the SDK connects to
   its local MCP server, speaks `Initialize` (capturing the `Implementation`
   block as optional `serverInfo`), then calls `tools/list`. The
   resulting `Tool[]` array is shipped **verbatim** as `tools[]`.
2. **Submission (SDK → MANTYX).** SDK posts the spec with the resolved
   catalog. Field names match the MCP spec exactly — `inputSchema`, not
   `parameters` — so a TypeScript SDK can pass through what its MCP client
   already decoded. The `tools[].name` values are exactly what the model
   will see; MANTYX does **not** auto-prefix or rename anything. Sanitize
   them to `[a-zA-Z0-9_]{1,64}` yourself (if you want `fs/read_file` to
   surface as `fs_read_file`, declare it that way).
3. **Tool call (MANTYX → SDK).** When the model calls a tool, MANTYX emits
   a `local_tool_call` event with `kind: "mcp_local"` and these extra
   hints so the SDK can dispatch to the right MCP client:

   ```jsonc
   {
     "seq": 9,
     "type": "local_tool_call",
     "data": {
       "toolUseId": "tu_x",
       "name": "fs_read_file",       // SDK-declared name; same string the model called
       "args": { "path": "/etc/hosts" },
       "kind": "mcp_local",
       "mcpServer": "fs",            // the SDK-side label from the ref's `name`
       "mcpToolName": "fs_read_file", // duplicates `name` for the SDK's convenience
       "mcpServerInfo": {            // present iff the ref carried `serverInfo`
         "name": "mcp-server-filesystem",
         "version": "0.4.1"
       }
     }
   }
   ```

4. **Execution (SDK).** SDK validates args against the locally-known
   `inputSchema`, speaks MCP `tools/call`, flattens the response content
   blocks (typically the joined `text` blocks), and POSTs the result back
   to `.../tool-results`.
5. **Refresh (optional).** To pick up new tools mid-session, send the
   updated `mcp_local` ref inside `POST /agent-sessions/:id/messages`'s
   `tools` field; the catalog snapshot lives on the run, not the session.

| Field          | Required | Notes |
| -------------- | -------- | ----- |
| `kind`         | yes      | Discriminator literal `"mcp_local"`. |
| `name`         | yes      | SDK-side server label (e.g. `"fs"`, `"jira"`). Echoed back unchanged as `mcpServer` on every `local_tool_call`. **Not used to prefix tool names.** Match `/^[a-zA-Z0-9_]{1,64}$/`. |
| `serverInfo`   | no       | The MCP `Implementation` block the SDK got from `Initialize` (`{ name, version? }`, plus any extra fields the server returned). Forwarded to the SDK in `local_tool_call.mcpServerInfo` for observability; not used to drive behavior. |
| `tools`        | yes      | Verbatim MCP `tools/list` output (1–64 entries). Each item is the standard MCP `Tool` shape: `{ name, description?, inputSchema?, annotations?, … }`. `name` is the model-facing tool name (SDK owns naming). `inputSchema` is the MCP-spec JSON Schema for the tool's arguments — used to constrain the LLM's tool call. Empty `inputSchema` means a no-arg tool. |

Older SDKs that ignore the `kind` discriminator still see a normal
`local_tool_call` and can match on `name` alone.

### 4.4 `reasoningLevel` (provider thinking strength)

`reasoningLevel` controls how much extended-thinking / reasoning effort the
model spends per turn. MANTYX maps the same value onto every supported
provider:

- **OpenAI Responses** — `reasoning.effort` on reasoning models (o-series,
  GPT-5.x, …; ignored on non-reasoning models and on xAI Grok).
- **Gemini 3+** — `thinkingConfig.thinkingLevel`; pre-Gemini-3 models
  consume the equivalent `thinkingBudget` token count.
- **Anthropic / Bedrock-Anthropic** — extended thinking with a budget that
  scales with strength (≈512 tokens at `low` → ≈8000 at `high`).

Two equivalent input shapes are accepted:

| Form        | Values                                | Notes |
| ----------- | ------------------------------------- | ----- |
| **String**  | `"off"`, `"low"`, `"medium"`, `"high"` | Snaps to the same anchors the web composer uses (Fast=30, Moderate=50, Smart=80; off=0). |
| **Number**  | integer `0`–`100`                     | Pass-through to `RunAgentOptions.reasoningLevel`. `0` explicitly disables provider thinking even on reasoning models. |

When omitted, MANTYX falls back to the agent's default — for ephemeral
specs, that means thinking is off; for `agentId`-backed specs, it follows
the persisted `Agent` configuration.

For session-scoped runs the inheritance rules are:

- `POST /agent-sessions { reasoningLevel }` — sets the session-default
  applied to every subsequent message run.
- `POST /agent-sessions/:id/messages { reasoningLevel }` — optional
  per-message override; applies to that one run only and does not mutate
  the session's stored value.

### 4.5 `outputSchema` (structured final reply)

`outputSchema` constrains the model's **final assistant text** to a JSON
document conforming to a JSON Schema. Useful when the SDK needs to feed the
reply directly into downstream code without LLM-flavoured prose to parse out.

```jsonc
"outputSchema": {
  "name":   "weather_report",       // optional; default "output"
  "schema": {                       // required, root must be a JSON object
    "type": "object",
    "properties": {
      "city":          { "type": "string" },
      "temperature_c": { "type": "number" }
    },
    "required": ["city", "temperature_c"]
  }
}
```

| Field    | Required | Notes |
| -------- | -------- | ----- |
| `name`   | no       | Stable identifier passed to providers (OpenAI `text.format.name`, Anthropic synthetic-tool name). Defaults to `"output"`. Must match `/^[a-zA-Z0-9_-]{1,64}$/`. |
| `schema` | yes      | JSON Schema describing the final assistant text. Root must be a JSON **object** (most providers reject array / scalar roots in structured-output mode). The schema is passed through verbatim — MANTYX does not validate its contents; the provider does. |

Validation (server-side, `400 invalid_request` on violation):

| Constraint                          | Limit |
| ----------------------------------- | ----- |
| Serialized JSON size of `outputSchema` | ≤ 32 KB |
| `name` regex                        | `/^[a-zA-Z0-9_-]{1,64}$/` |
| `schema` shape                      | non-`null`, non-array JSON object |

**Per-provider behaviour** (mirrors the SDK's `RunAgentOptions.finalResponseSchema`):

| Provider                       | How the schema is enforced |
| ------------------------------ | -------------------------- |
| OpenAI Responses (o-series, GPT-5.x, …) | `text.format = { type: "json_schema", strict: true, name, schema }` on every turn (works alongside tool calls). |
| Gemini 3+ (any turn)           | `responseMimeType: "application/json"` + `responseJsonSchema` on every `completeTurn`. Gemini 3 accepts the schema alongside `functionDeclarations`. |
| Gemini ≤ 2.5 (no-tools turn)   | `responseMimeType: "application/json"` + `responseJsonSchema`. |
| Gemini ≤ 2.5 (with tools)      | Synthetic `set_model_response` function declaration is injected; its `parametersJsonSchema` is the supplied schema. The system instruction is augmented to direct the model to call this tool with the final answer. The engine intercepts the call, hides it from the SDK, and surfaces the call's arguments as the assistant text (JSON-stringified). Sidesteps the API rejection ("Function calling with a response mime type: 'application/json' is unsupported") without round-tripping a 4xx. |
| Anthropic / Bedrock-Anthropic  | Synthetic `final_report` tool whose `input_schema` is the supplied schema; `tool_choice` is forced on the no-tools finishing turn. The tool's input is surfaced as the assistant text. |
| xAI Grok, others               | Ignored (the model returns plain text). |

The synthetic-tool paths (Gemini 2.5 + tools, Anthropic) are entirely
internal: the SDK never receives a `local_tool_call` for
`set_model_response` or `final_report`, and these names never appear in
the tools array the SDK declared. The terminal `result` event still
carries the reply as `data.text: string`.

The terminal `result` event still carries the reply as
`data.text: string` — the SDK is expected to `JSON.parse` and validate
against its own source-of-truth schema (Zod, Pydantic, …) so it keeps
control of error handling on malformed-but-rare provider outputs.

**Inheritance for sessions:**

- `POST /agent-sessions { outputSchema }` — sets the session-default,
  applied to every subsequent message run.
- `POST /agent-sessions/:id/messages { outputSchema }` — optional
  per-message override; applies to that one run only and does not mutate
  the session's stored value.

`outputSchema` works for both ephemeral runs (`systemPrompt`-defined) and
`agentId`-backed runs — the runner applies the schema to whatever
`AgentSpec` it built for the run. When the field is omitted, runs return
unconstrained plain text as before.

### 4.6 `loopDetection` (steering nudge + hard cutoff)

`loopDetection` is the wire-protocol projection of the SDK's
`RunAgentOptions.loopDetection`. The pipeline tracks a canonical
order-invariant `(toolName, args)` signature for every assistant turn that
makes one or more tool calls; when the same signature repeats consecutively,
the guard fires.

- **`consecutiveThreshold` rounds in a row** (default `3`) — the pipeline
  skips the duplicate batch with a synthetic "you've made this exact call
  before" tool result and prepends a user-style **steering nudge**
  ("either deliver a final answer or change strategy"). The model gets the
  nudge before its next turn and either finalises or pivots.
- **`hardCutoffThreshold` rounds in a row** (default `6`) — the pipeline
  forces a tools-disabled finalise turn (`maxToolTurnsExceeded: "finalize"`
  semantics) so the run lands cleanly instead of churning forever.

```jsonc
"loopDetection": {
  "consecutiveThreshold": 3,        // optional, default 3 — fires the steering nudge
  "hardCutoffThreshold":  6         // optional, default 6 — forces finalisation
}
```

The wire shape also accepts the literal `false`:

```jsonc
"loopDetection": false              // explicitly disable the guard for this run
```

| Field                  | Type            | Required | Notes |
| ---------------------- | --------------- | -------- | ----- |
| `consecutiveThreshold` | integer ≥ 2     | no       | Defaults to **3** when the field is omitted. Must be `>= 2` (one identical batch is just a single tool call, not a loop). |
| `hardCutoffThreshold`  | integer ≥ 3     | no       | Defaults to **6** when the field is omitted. Must be `> consecutiveThreshold`; otherwise the soft nudge would never get a chance to land. |
| (top-level `false`)    | literal `false` | no       | Disables the guard entirely for this run. The pipeline still enforces `budgets.maxToolTurns`. |

Validation (server-side, `400 invalid_request` on violation):

| Constraint                                         | Limit |
| -------------------------------------------------- | ----- |
| `consecutiveThreshold` / `hardCutoffThreshold` upper bound | `100` |
| `hardCutoffThreshold` strictly greater than `consecutiveThreshold` | enforced |

**Defaults.** When `loopDetection` is omitted entirely, MANTYX applies the
runtime defaults from `runtime/default-run-guards.ts`:
`{ consecutiveThreshold: 3, hardCutoffThreshold: 6 }`. This is the same
configuration used by every in-process runner (chat, schedule, inbound) so
SDK-driven runs and platform-driven runs behave identically.

**Inheritance for sessions.**

- `POST /agent-sessions { loopDetection }` — sets the session-default,
  applied to every subsequent message run.
- `POST /agent-sessions/:id/messages { loopDetection }` — optional
  per-message override; applies to that one run only and does not mutate
  the session's stored value.

**Observability.** Each intervention emits a SSE `loop_detected` event
(see §7) so SDK clients can render `looping — nudged` / `looping — gave up`
status notes. The actual mechanism (skip + nudge or forced finalise) is
fully handled server-side; the SDK only needs to surface the event.

### 4.7 `toolBudgets` (per-tool call caps)

`toolBudgets` caps how many times a specific tool may execute over the
**lifetime of the run** (across every LLM turn). Calls under the cap run
normally; calls past the cap are **intercepted before execution** and
returned to the model as a synthetic "budget exceeded — pivot or finalize"
tool result.

```jsonc
"toolBudgets": {
  "recall":                { "maxCalls": 4 },
  "hive_consult_ontology": { "maxCalls": 4 },
  "traverse":              { "maxCalls": 3 },
  "scary_tool":            { "maxCalls": 0 }   // disables the tool for this run
}
```

| Field      | Type        | Required | Notes |
| ---------- | ----------- | -------- | ----- |
| `<key>`    | string      | yes      | Logical tool name as the model sees it (the same name on `ResolvedTool.name`; the SDK + pipeline handle sanitisation). 1–120 characters. |
| `maxCalls` | integer ≥ 0 | yes      | Hard cap on executed calls per run. `0` disables the tool entirely (every attempt returns the synthetic body on the first try). Budgets are **per-tool, not pooled**: `hive_search_deals: { maxCalls: 5 }` and `hive_search_meetings: { maxCalls: 5 }` give the agent five of each, not five between them. |

Validation (server-side, `400 invalid_request` on violation):

| Constraint            | Limit |
| --------------------- | ----- |
| Max entries           | `32` |
| `<key>` length        | `1..120` chars |
| `maxCalls` upper bound | `1000` (functionally unlimited; the SDK's `maxToolTurns: 100` fires first) |

**Defaults.** When `toolBudgets` is omitted, MANTYX layers the runtime
defaults from `runtime/default-run-guards.ts` on top of the spec. The
default research-tool surface is:

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

**Inheritance for sessions.**

- `POST /agent-sessions { toolBudgets }` — sets the session-default,
  applied to every subsequent message run.
- `POST /agent-sessions/:id/messages { toolBudgets }` — optional
  per-message override; applies to that one run only and does not mutate
  the session's stored value.

**Observability.** Each interception emits a SSE `tool_budget_exceeded`
event (see §7) so SDK clients can render `memory budget exhausted` /
`research cap reached` status notes. The synthetic tool-result is emitted
on the normal `tool_result` channel just like any other server-resolved
result, so the run timeline stays linear.

**Tools NOT capped by default.** `hive_list_*` and `hive_get_*` are
intentionally not in the default budget map — agents legitimately call
them once per entity-of-interest, which can easily exceed any small cap
during normal multi-entity reads. The loop-detection guard catches the
pathological "same `(name, args)` batch over and over" case for that
family without needing per-tool caps.

### 4.8 `metadata` (developer-supplied KV for filtering)

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

// streamed reasoning / extended-thinking tokens (only when reasoningLevel > 0
// AND the active provider exposes thought parts: Anthropic extended thinking,
// Gemini `includeThoughts`, OpenAI `reasoning_content` on reasoning models).
{ "seq": 2, "type": "thinking_delta", "data": { "text": "First, I should…" } }

// completed assistant message (text + optional tool calls about to execute).
// `turn` is the 0-based tool-turn index this message closes.
// `finishReason` is the canonical lowercase stop reason normalized across
// providers (`"end_turn"`, `"tool_use"`, `"max_tokens"`, `"refusal"`,
// `"malformed_function_call"`, …); `null` / omitted when the provider did
// not report one. `toolCalls` is omitted when the model called no tools.
{ "seq": 3, "type": "assistant_message",
  "data": {
    "text": "...",
    "turn": 0,
    "finishReason": "tool_use",
    "toolCalls": [
      { "id": "call_abc", "name": "search", "input": { /* JSON-Schema-matching args */ } }
    ]
  } }

// server-side tool call/result (informational; SDK does not act on these)
{ "seq": 4, "type": "tool_call",   "data": { "toolUseId": "...", "name": "...", "input": {...} } }
{ "seq": 5, "type": "tool_result", "data": { "toolUseId": "...", "name": "...", "ok": true, "summary": "..." } }

// LOCAL tool call — SDK MUST POST a tool-result for the same toolUseId.
// `kind` carries the discriminator so the SDK can dispatch to the right
// local handler (generic registry, A2A client, or MCP client). Older SDKs
// that ignore `kind` still match on `name`.
{ "seq": 6, "type": "local_tool_call", "data": { "toolUseId": "tu_x", "name": "read_file", "args": { "path": "/etc/hosts" } } }
{ "seq": 6, "type": "local_tool_call", "data": { "toolUseId": "tu_y", "name": "intranet_hr_agent", "args": { "message": "When does PTO reset?" }, "kind": "a2a_local", "agentCard": { "name": "Acme HR", "url": "https://hr.intranet.acme/a2a", "skills": [ { "id": "pto_lookup", "name": "PTO lookup" } ] } } }
{ "seq": 6, "type": "local_tool_call", "data": { "toolUseId": "tu_z", "name": "fs_read_file", "args": { "path": "/etc/hosts" }, "kind": "mcp_local", "mcpServer": "fs", "mcpToolName": "fs_read_file", "mcpServerInfo": { "name": "mcp-server-filesystem", "version": "0.4.1" } } }

// echo of the SDK's POSTed tool-result, persisted for replay
{ "seq": 7, "type": "local_tool_result_in", "data": { "toolUseId": "tu_x", "output": "127.0.0.1 ..." } }

// loop-detection guard fired (see §4.6). Soft nudge: hardCutoff=false. Hard cutoff: hardCutoff=true.
// `tools` is the (toolName, …) batch the model just repeated; the synthetic skip + nudge are
// emitted on the normal tool_result + assistant_delta channels — this event is observability only.
{ "seq": 7, "type": "loop_detected", "data": { "consecutiveCount": 3, "hardCutoff": false, "tools": ["recall"] } }

// per-tool budget exceeded (see §4.7). The pipeline already surfaced the synthetic
// "budget exceeded — pivot or finalize" body on the normal tool_result channel; this event
// is observability so SDK clients can render "memory budget exhausted" status notes.
{ "seq": 7, "type": "tool_budget_exceeded", "data": { "tool": "recall", "maxCalls": 4, "callIndex": 5 } }

// terminal event — exactly one of `result`, `error`, or `cancelled` lands per run.
{ "seq": 8, "type": "result",    "data": { "subtype": "success", "text": "Final reply" } }
{ "seq": 8, "type": "result",    "data": { "subtype": "error_local_tool_timeout", "error": "..." } }
{ "seq": 8, "type": "error",     "data": {
    "error":        "Model output was truncated (stop_reason=max_tokens). …",
    "code":         "truncation",
    "errorClass":   "truncation",
    "finishReason": "max_tokens",
    "partialText":  "{\n  \"answer\":… (truncated JSON) …",
    "retryable":    false
} }
{ "seq": 8, "type": "cancelled", "data": {} }
```

A run terminates with exactly one of `result`, `error`, or `cancelled`. The
connection is closed by the server immediately after sending the terminal
event. Clients should not assume any particular ordering between the
human-readable `event:` field and the parsed `type` inside `data` — they
are always equal, but implementations should rely on `data.type` because
some HTTP middleware strips the `event:` line.

**`error` event payload fields.** The runner enriches the `error` event
with structured triage attributes when the failure carried a salvage
path (typically truncation, upstream deadline, or max-budget-with-text):

| Field          | Type     | Required | Notes |
| -------------- | -------- | -------- | ----- |
| `error`        | string   | yes      | Human-readable message (also persisted on the run row's `error` column). |
| `code`         | string   | yes      | Legacy alias for `errorClass`. Equals `errorClass` when present; otherwise a small lowercase token (`"error"`, `"invalid_spec"`, `"worker_error"`, …) the SDK can switch on. |
| `errorClass`   | string   | no       | Canonical category. One of `"rate_limit"`, `"overloaded"`, `"server"`, `"context_window"` (input too big), `"truncation"` (output budget exhausted), `"invalid_request"`, `"auth"`, `"timeout"`, `"local_timeout"`, `"upstream_deadline"`, `"unknown"`. New categories may land additively. |
| `finishReason` | string \| null | no | Canonical lowercase stop reason normalized across providers (`"max_tokens"`, `"refusal"`, `"malformed_function_call"`, …). When present, mirrors the value on the last `assistant_message`. |
| `partialText`  | string   | no       | **Best-effort raw bytes** the model emitted before the failure. For `outputSchema` runs this is likely **incomplete JSON** that will fail `JSON.parse` — see §4.5 / `docs/wire-protocol.md` §7. Also persisted on the run row's `finalText` column so the Calls UI can render it alongside a truncation banner. |
| `retryable`    | boolean  | no       | Coarse retry hint inherited from the pipeline's error classifier. Informational; the SDK still owns the actual retry decision. |

**Truncation contract.** When the model is mid-output and Gemini /
Anthropic / OpenAI hit the output budget, MANTYX does **not** discard
the bytes that already streamed. Instead:

1. The last `assistant_message` for the turn carries the partial text
   plus `finishReason: "max_tokens"`.
2. The terminal SSE event is an `error` (not `result`) with
   `errorClass: "truncation"` and `data.partialText` set to the same
   bytes.
3. The run row exposed by `GET /agent-runs/:runId` has
   `{ status: "failed", finalText: "<partial text>",
   error: "Model output was truncated …", failureReason: { errorClass:
   "truncation", finishReason: "max_tokens" } }`.

`partialText` is a **best-effort raw byte sequence** — for `outputSchema`
runs it will almost always fail `JSON.parse` because the JSON object was
not closed. SDKs should treat it as diagnostic data, never as a
schema-conformant reply. Surfacing it (as a "truncated reply — JSON
likely incomplete" status note) is the recommended pattern; silently
falling back to it as the answer is not.

**Run snapshot fields.** `GET /agent-runs/:runId` returns the run row
with these triage-relevant columns:

| Field           | Notes |
| --------------- | ----- |
| `status`        | `"queued" \| "running" \| "succeeded" \| "failed" \| "cancelled"`. |
| `finalText`     | Final assistant text on success; same string as terminal `data.partialText` when `failureReason.errorClass === "truncation"`. Otherwise `null`. |
| `error`         | Human-readable error message (matches terminal `error.data.error`). `null` on success / cancellation. |
| `failureReason` | JSON object `{ errorClass, finishReason }` on `status === "failed"` runs that carried a salvage payload. Future-proof for additional triage fields. `null` otherwise. |

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

**Run-level error categories.** When a run terminates via the SSE `error`
event (§7), the payload carries an `errorClass` triage category in
addition to the human-readable `error` message. SDKs typically expose
this as a typed field on their run-error type (TS `MantyxRunError.errorClass`,
Python `MantyxRunError.error_class`, Go `RunError.ErrorClass`). The
canonical set:

| `errorClass`        | Typical cause | Has `partialText`? |
| ------------------- | ------------- | ------------------ |
| `rate_limit`        | Provider rate-limited the request (HTTP 429-equivalent). | No |
| `overloaded`        | Provider returned a transient "overloaded" / 5xx. | No |
| `server`            | Generic upstream provider error. | No |
| `context_window`    | Input exceeded the model's context window. | No |
| `truncation`        | Output budget exhausted mid-reply (`finishReason: "max_tokens"`). | **Yes** |
| `invalid_request`   | Provider rejected the spec / params. | No |
| `auth`              | BYOK credentials invalid for this run. | No |
| `timeout`           | Generic upstream timeout (provider-side). | No |
| `local_timeout`     | SDK didn't POST a `tool-result` within `localToolTimeoutMs`. | No |
| `upstream_deadline` | MANTYX worker deadline exceeded waiting on the provider. | Sometimes |
| `unknown`           | Anything else — fallback so SDKs always have a category. | No |

The category set is **additive over the wire**: new categories may
appear without bumping the protocol version, so SDKs should default to
`unknown` (or simply pass the raw string through to callers) for
unrecognized values rather than crashing.

## 11. Suggested client architecture

A reference SDK should:

1. Hold the API key + workspace slug and a small `fetch` (or stdlib HTTP)
   client.
2. Maintain three local-callback registries (or one tagged-union registry),
   keyed by tool `name`:
   - **Generic local tools** (`kind: "local"`) — caller-supplied handler
     functions, dispatched by `name`. Accept developer-supplied input and
     output schemas (Zod, Pydantic, JSON Schema, …) and serialize to JSON
     Schema before submission as `parameters` / `outputSchema`. Surface a
     `longRunning` knob on the tool builder so callers can opt into the
     model-side "don't double-call" hint without hand-editing the
     description.
   - **Local A2A peers** (`kind: "a2a_local"`) — caller-supplied A2A
     clients. Resolve the peer's Agent Card *first* (e.g. `fetch
     "<peer>/.well-known/agent-card.json"` or read from a local registry),
     attach it to the spec as `agentCard`, and in the dispatcher look the
     client up by `agentCard.url` (or any other field you indexed on)
     when the `local_tool_call` arrives.
   - **Local MCP servers** (`kind: "mcp_local"`) — caller-supplied MCP
     client connections. Speak `Initialize` and `tools/list` once at
     setup, ship the verbatim `tools[]` (with `inputSchema`) plus
     optional `serverInfo`, and dispatch incoming calls by the `mcpServer`
     field in the event payload.

   `mantyx`, `mantyx_plugin`, `a2a`, and `mcp` refs are server-resolved —
   no SDK-side registry needed.
3. On `runAgent` / `session.send`:
   - Accept `reasoningLevel` from the caller and pass it through unchanged
     (string `"off" | "low" | "medium" | "high"` *or* number `0–100`); do
     **not** translate to a vendor-specific knob — the server owns that
     mapping so all SDKs stay aligned with the web composer.
   - POST the run/message, get `{ runId, streamUrl }`.
   - Open the SSE stream with `Last-Event-ID` if reconnecting.
   - On `local_tool_call`, dispatch by the event's `kind` discriminator
     (defaulting to `"local"` when omitted): generic registry / local A2A
     client / local MCP client. Validate args against the tool's schema,
     run it, POST the result back to `.../tool-results`.
   - Treat `thinking_delta` events as opt-in callback fodder; many UIs hide
     them by default. Their presence depends on `reasoningLevel > 0` and
     on the active model exposing thought parts.
   - Accept `loopDetection` and `toolBudgets` from the caller and pass
     them through unchanged (see §4.6 / §4.7). Both fields are *additive*:
     omitting them keeps MANTYX's runtime defaults; passing
     `loopDetection: false` opts out; passing `toolBudgets: {}` clears the
     defaults; passing entries layers caller overrides on top of the
     defaults.
   - Treat `loop_detected` and `tool_budget_exceeded` SSE events as
     observability-only — the server already substituted the synthetic
     tool-results / steering nudges, so the SDK's job is just to surface
     the event to the caller (status banner, log line, telemetry). Do
     **not** abort the run on these events; the run continues through
     `result` / `error` / `cancelled` as usual.
   - On terminal `result` with `subtype === "success"`, resolve the call
     with the final `text`. On a terminal `error` event, raise a typed
     run-error that carries the new triage attributes (`errorClass`,
     `finishReason`, `partialText`, `retryable`) so callers can render
     "truncated reply — JSON likely incomplete" banners and short-circuit
     retry policies. Treat `partialText` as **diagnostic** data — never
     auto-fall-back to it as the final answer.
4. Re-emit assistant deltas/events as a stream/iterator for callers who care
   about live output.
5. Treat the protocol as the contract. Implementation details such as Valkey
   pub/sub or pgvector are server-side only.
6. **Execution model (server-side, informational).** Runs are executed
   out-of-process by the inbound worker off a dedicated
   `mantyx:agent-runs` RabbitMQ queue; the API only persists the run row and
   enqueues. Nothing about this is observable on the wire — clients still see
   `202 { runId, streamUrl }` followed by the same SSE vocabulary — but it
   means the `local_tool_call` ↔ `tool-results` round-trip is valid across
   any API or worker replica, and transient broker failures surface as a
   terminal `error` event on the stream, not as a 5xx on the initial POST.

The npm package [`@mantyx/sdk`](https://www.npmjs.com/package/@mantyx/sdk) and the Go module
[`github.com/mantyx/mantyx-go-sdk`](https://github.com/mantyx/mantyx-go-sdk) are reference implementations of this protocol
(maintained in the official **mantyx-sdk** repositories).
