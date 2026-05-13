# @mantyx/sdk

The official TypeScript SDK for the [MANTYX](https://mantyx.com) agent
runtime. Define ephemeral agents that mix server-side MANTYX tools with
locally-executed tools, run them remotely, and stream events back into your
process.

- LLM loop runs on MANTYX (BYOK or platform-hosted models).
- Server-resolved tools (`mantyx`, `mantyx_plugin`, `a2a`, `mcp`) execute
  inside MANTYX — including remote Agent2Agent peers and remote MCP servers.
- Client-resolved tools (`local`, `a2a_local`, `mcp_local`) execute inside
  *your* process; the SDK shuttles inputs and outputs over an SSE stream +
  a tool-result POST.
- Tunable provider thinking via `reasoningLevel` (string anchors or 0–100).
- One-shot runs and multi-turn sessions, both with persisted observability.
- Authenticated with a single bearer credential — either a workspace API
  key (token prefix `mantyx_`) or a MANTYX OAuth 2.0 access token
  (`mantyx_at_`). Both flow through the same `Authorization: Bearer …`
  header and are interchangeable end-to-end.

For background, see the [agent-runs protocol spec](./docs/agent-runs-protocol.md)
and the messaging-layer reference in [`docs/wire-protocol.md`](./docs/wire-protocol.md)
— the latter pins down the exact `local_tool_call` event shape and the
resolved data structures (`a2a_local` Agent Card, `mcp_local` `Tool[]`)
that this SDK ships.

## Install

```bash
npm install @mantyx/sdk zod
# or: pnpm add @mantyx/sdk zod
# or: yarn add @mantyx/sdk zod
```

Requires Node.js 18.17+ (for `fetch` and `ReadableStream`). The SDK depends
on `zod` (parameter schemas) and `@modelcontextprotocol/sdk` (the official
MCP TypeScript SDK that powers `defineLocalMcp`'s stdio + Streamable HTTP
transports). The MCP SDK is loaded lazily — apps that never use
`defineLocalMcp` don't pay its startup cost.

## Quickstart

```ts
import { z } from "zod";
import fs from "node:fs/promises";
import { MantyxClient, defineLocalTool, mantyxTool } from "@mantyx/sdk";

const client = new MantyxClient({
  // Use *either* `apiKey` (workspace API key, token prefix `mantyx_`) or
  // `accessToken` (OAuth 2.0 access token, prefix `mantyx_at_`). The
  // server resolves either kind by token-prefix, so the SDK only ships
  // one `Authorization: Bearer …` header.
  apiKey: process.env.MANTYX_API_KEY!,
  // accessToken: process.env.MANTYX_ACCESS_TOKEN!,
  workspaceSlug: process.env.MANTYX_WORKSPACE_SLUG!,
  // baseUrl: "https://app.mantyx.io", // override for self-hosted
});

const result = await client.runAgent({
  systemPrompt: "You are a helpful assistant.",
  prompt: "Read /etc/hostname and summarize what it says.",
  tools: [
    // Local tool — defined and executed in this process.
    defineLocalTool({
      name: "read_file",
      description: "Read a file from the local filesystem.",
      parameters: z.object({ path: z.string() }),
      execute: async ({ path }) => fs.readFile(path, "utf8"),
    }),
    // Reference to an existing MANTYX workspace tool.
    mantyxTool("tool_cm6abc123"),
  ],
});

console.log(result.text);
```

The SDK opens an SSE stream to MANTYX, listens for `local_tool_call` events,
runs the matching local handler, and POSTs the result back. The server keeps
running the agent loop until it produces a final reply.

## Triggering a persisted MANTYX agent

Pass `agentId` to run an agent that already exists in your workspace. The
server hydrates the agent's system prompt, model, and server-side tools
(memory, skills, plugin tools, …) from the `Agent` row at run time. Anything
you pass in `tools` is **merged on top** — typically `local` tools you want
the agent to be able to call back into for this specific run.

```ts
import { defineLocalTool, MantyxClient } from "@mantyx/sdk";
import { z } from "zod";

const client = new MantyxClient({ apiKey: "...", workspaceSlug: "acme" });

const result = await client.runAgent({
  agentId: "agent_cm6abc123", // workspace agent id
  prompt: "Pull the latest deploy logs and summarise them.",
  tools: [
    defineLocalTool({
      name: "read_local_file",
      parameters: z.object({ path: z.string() }),
      execute: ({ path }) => readFileSync(path, "utf8"),
    }),
  ],
});
console.log(result.text);
```

Notes:

- `systemPrompt` becomes optional when `agentId` is set; if both are sent,
  the agent's stored prompt wins.
- `modelId` is also optional: omit it to use the agent's configured LLM
  provider, or pass it to override the model for this run.
- The API key must be authorized for the agent (an empty `agentIds` allowlist
  on the key counts as "all agents in the workspace"). Otherwise the call
  returns `403`.

The same `agentId` field works on `client.createSession({ ... })` for
multi-turn conversations against a persisted agent.

## Agent2Agent delegation

Hand a turn off to another agent — either a remote peer MANTYX dials directly
(`mantyxA2A`) or a peer that only the SDK can reach (`defineLocalA2A`). The
model addresses both with the same `{ message: string }` argument shape, so
an agent prompt that uses one works unchanged with the other.

`defineLocalA2A` is fully URL-driven: pass the Agent Card URL and the SDK
takes care of the rest — fetching the card on the first run, shipping it
inline as part of the spec, and POSTing JSON-RPC `message/send` to the
card's `url` whenever MANTYX emits a `local_tool_call`. You don't write any
A2A code yourself.

```ts
import { MantyxClient, defineLocalA2A, mantyxA2A } from "@mantyx/sdk";

const client = new MantyxClient({ apiKey: "...", workspaceSlug: "acme" });

await client.runAgent({
  systemPrompt: "You are a helpful router. Delegate billing to billing_agent.",
  prompt: "Why was I charged twice last month?",
  tools: [
    // Public peer MANTYX dials directly.
    mantyxA2A({
      name: "billing_agent",
      description: "Delegate billing questions to the Acme billing agent.",
      agentCardUrl: "https://billing.acme.com/.well-known/agent-card.json",
      headers: { Authorization: `Bearer ${process.env.BILLING_TOKEN}` },
    }),
    // Intranet peer the SDK reaches on MANTYX's behalf — URL only.
    defineLocalA2A({
      name: "intranet_hr",
      agentCardUrl: "https://hr.intranet.acme/.well-known/agent-card.json",
      headers: { Authorization: `Bearer ${process.env.HR_TOKEN}` },
    }),
  ],
});
```

The same `headers` are sent on both the card fetch *and* every subsequent
`message/send` POST, which is typically what intranet peers want. The SDK
caches the resolved card on the tool ref for the duration of the run /
session — re-construct the ref to force a refetch.

> **Headers and secrets.** The `headers` you pass to `mantyxA2A` are forwarded
> as-is. For long-lived credentials, register the peer as a workspace
> `ExternalAgent` instead — those headers support `{{secret:NAME}}`
> placeholders. Use `mantyxA2A` for short-lived, per-run tokens minted by
> your application.

### Exposing an agent over A2A

The inverse direction also works: wrap a MANTYX agent (ephemeral spec or a
persisted `agentId`) and serve it as an Agent2Agent peer using the official
[`@a2a-js/sdk`](https://www.npmjs.com/package/@a2a-js/sdk) library. Other
agents can then discover it at `/.well-known/agent-card.json` and call
`message/send` over JSON-RPC — including MANTYX agents elsewhere in your
estate consuming this one via `mantyxA2A` or `defineLocalA2A`.

```ts
import { MantyxClient } from "@mantyx/sdk";
import { serveAgentOverA2A } from "@mantyx/sdk/a2a-server";

const client = new MantyxClient({ apiKey: "...", workspaceSlug: "acme" });

const handle = await serveAgentOverA2A({
  client,
  agent: { agentId: "agent_cm6abc123" }, // or { systemPrompt, modelId, tools }
  port: 4000,
  agentCard: {
    name: "Acme Support",
    description: "Customer support questions.",
    protocolVersion: "0.3.0",
    version: "1.0.0",
    url: "http://localhost:4000",
    skills: [{ id: "support", name: "Support", tags: ["support"] }],
    capabilities: { streaming: true, pushNotifications: false },
    defaultInputModes: ["text"],
    defaultOutputModes: ["text"],
  },
});

console.log(`A2A peer up on ${handle.url}`);
// later: await handle.close();
```

`@a2a-js/sdk` and `express` are declared as **optional peer dependencies**,
so apps that don't expose an A2A server pay zero bundle cost. Install them
on demand:

```bash
npm install @a2a-js/sdk express
```

Each unique A2A `contextId` opens a long-lived MANTYX session by default, so
multi-turn `message/send` calls share conversational history. Pass
`conversation: "stateless"` to reduce every A2A request to a one-shot
`runAgent` call.

For lower-level integration (mounting the executor in your own Express /
Fastify / Connect app), `@mantyx/sdk/a2a-server` also exports a
`MantyxAgentExecutor` class implementing `@a2a-js/sdk/server`'s
`AgentExecutor` interface.

## MCP connectors

Expose every tool published by an MCP server to the agent loop in one go,
without listing them individually.

```ts
import { MantyxClient, mantyxMcp, defineLocalMcp } from "@mantyx/sdk";

const client = new MantyxClient({ apiKey: "...", workspaceSlug: "acme" });

await client.runAgent({
  systemPrompt: "You are a developer assistant with GitHub + filesystem access.",
  prompt: "Summarize the latest 5 issues on octocat/hello-world.",
  tools: [
    // Remote MCP server (Streamable HTTP) — MANTYX lists the catalog at run
    // start and proxies every call. Tools surface as `github_<tool>`.
    mantyxMcp({
      name: "github",
      url: "https://mcp.github.com/v1",
      headers: { Authorization: `Bearer ${process.env.GH_PAT}` },
      toolFilter: ["search_issues", "get_repo"],
    }),
    // Local MCP server — fully managed by the SDK. Pass either a
    // Streamable HTTP `url` *or* an stdio `command`; the SDK opens the
    // transport, runs `Initialize` + `tools/list`, ships the resolved
    // catalog inline, and forwards every invocation to `tools/call`. The
    // model sees `<server>_<tool>` (`fs_read_file`, `fs_list_dir`, …) —
    // same shape as `mantyxMcp` above.

    // (a) Streamable HTTP MCP server.
    defineLocalMcp({
      name: "fs",
      url: "http://localhost:8080/mcp",
      headers: { Authorization: `Bearer ${process.env.FS_TOKEN}` },
    }),

    // (b) stdio MCP server — the SDK spawns the process for you.
    // defineLocalMcp({
    //   name: "fs",
    //   command: "mcp-server-filesystem",
    //   args: ["/workspace"],
    //   env: { LOG_LEVEL: "info" },
    // }),
  ],
});
```

The MCP transport is opened lazily on the first `runAgent` / first
`session.send`, kept warm for subsequent calls within the same run /
session, and closed when the run completes or `session.end()` is called.
If the MCP server can't be reached, the SDK throws before submitting the
spec — you get the failure synchronously rather than mid-conversation.

If a remote (`kind: "mcp"`) MCP server is unreachable when the run starts,
MANTYX still exposes a single `<server>_unavailable` stub so the model can
tell the user why the connector is missing.

## Reasoning effort (`reasoningLevel`)

Crank up provider thinking on reasoning models without writing
provider-specific code:

```ts
await client.runAgent({
  systemPrompt: "...",
  prompt: "Plan a multi-week migration.",
  reasoningLevel: "high", // or 80, etc.
});
```

| Form         | Values                                       | Notes |
| ------------ | -------------------------------------------- | ----- |
| String       | `"off"`, `"low"`, `"medium"`, `"high"`        | Snaps to the same anchors the web composer uses (Fast=30, Moderate=50, Smart=80; off=0). |
| Number       | integer `0`–`100`                             | `0` explicitly disables provider thinking on reasoning models. |

The server maps this onto each LLM's native dial — `reasoning.effort` for
OpenAI, `thinkingConfig` for Gemini, extended-thinking budget for Anthropic.
Non-reasoning models silently ignore it. On sessions, `reasoningLevel`
inherits from the session and can be overridden per `session.send`.

## Structured output (`outputSchema`)

Constrain the assistant's **final reply** to a JSON document matching a
JSON Schema. The wire still ships the reply as `text: string`, but that
string is guaranteed-parseable JSON. Pair with `parseRunOutput` for a
typed value with a clean error path:

```ts
import { z } from "zod";
import { MantyxClient, parseRunOutput } from "@mantyx/sdk";

const Weather = z.object({ city: z.string(), temperature_c: z.number() });
const WeatherJsonSchema = {
  type: "object",
  properties: {
    city: { type: "string" },
    temperature_c: { type: "number" },
  },
  required: ["city", "temperature_c"],
  additionalProperties: false,
} as const;

const result = await client.runAgent({
  systemPrompt: "Return the weather as JSON.",
  prompt: "What's the weather in San Francisco right now?",
  outputSchema: { name: "weather_report", schema: WeatherJsonSchema },
});

const report = parseRunOutput(result, (v) => Weather.parse(v));
//    ^? { city: string; temperature_c: number }
```

The SDK validates `name` (regex `/^[a-zA-Z0-9_-]{1,64}$/`), schema shape
(non-array JSON object), and total size (≤ 32 KB) locally so you get a
typed `MantyxError` up front instead of a server round-trip rejection.
On parse failure (rare; bad model output), `parseRunOutput` throws
`MantyxParseError` with the original `text` preserved.

`outputSchema` is independent of `reasoningLevel` — combine them for
deep-reasoning JSON outputs. On sessions it inherits from
`createSession({ outputSchema })` and can be overridden per
`session.send(prompt, { outputSchema })`. See
[`docs/wire-protocol.md` §7](./docs/wire-protocol.md) for the full
per-provider mapping.

### Structured output for local tools

`defineLocalTool` accepts the same per-tool affordances as the wire
protocol: an `outputSchema` (Zod schema or JSON Schema dict) describing
the tool's structured return value, and a `longRunning` flag that
appends a "don't double-call while pending" hint to the model-facing
description.

```ts
defineLocalTool({
  name: "kick_off_export",
  description: "Start a long-running export job.",
  parameters: z.object({ dataset: z.string() }),
  outputSchema: z.object({
    jobId: z.string(),
    status: z.enum(["pending", "done"]),
  }),
  longRunning: true,
  execute: async ({ dataset }) =>
    JSON.stringify(await enqueueExport(dataset)),
});
```

`outputSchema` is forwarded to providers with per-tool response schemas
(Gemini's `responseJsonSchema` on the FunctionDeclaration); other engines
surface it via the description. `longRunning` is a pure annotation —
MANTYX appends a stable hint and does *not* alter scheduling or
timeouts. See [`docs/tools/local`](https://docs.mantyx.com/docs/tools/local/)
for the full guide.

## Picking a model

```ts
const { models, defaultModelId } = await client.listModels();
console.log(models.map((m) => `${m.id}\t${m.label}`).join("\n"));

await client.runAgent({
  systemPrompt: "...",
  prompt: "Hi!",
  modelId: "platform:cm6abc123", // or "provider:<id>", or "<vendorModelId>"
});
```

`modelId` accepts:

- `platform:<offeringId>` — a platform-hosted model offering.
- `provider:<llmProviderId>` — your own BYOK provider's default model.
- `provider:<llmProviderId>:<vendorModelId>` — your provider, override model.
- `<vendorModelId>` — bare vendor id; only resolves when one workspace
  provider can run it.
- omitted — workspace default.

## Streaming tokens

```ts
for await (const event of client.streamAgent({
  systemPrompt: "...",
  prompt: "Tell me a story.",
})) {
  if (event.type === "assistant_delta") process.stdout.write(event.text);
  if (event.type === "result") process.stdout.write("\n");
}
```

Or use the `onAssistantDelta` callback on `runAgent`:

```ts
await client.runAgent({
  systemPrompt: "...",
  prompt: "...",
  onAssistantDelta: (delta) => process.stdout.write(delta),
});
```

## Multi-turn sessions

Sessions own the agent spec (system prompt, model, tool defs) and the full
message history. Each `send` is a run scoped to the session.

```ts
const session = await client.createSession({
  systemPrompt: "You are a friendly REPL.",
  tools: [
    defineLocalTool({
      name: "today",
      description: "Get today's date as ISO 8601.",
      parameters: z.object({}),
      execute: () => new Date().toISOString().slice(0, 10),
    }),
  ],
});

const r1 = await session.send("What day is it?");
console.log(r1.text);

const r2 = await session.send("And what about tomorrow?");
console.log(r2.text);

await session.end();
```

### Tagging runs and sessions with `metadata`

Attach a flat string→string KV to runs and sessions so your team can filter
the dashboard by it (Agent runs → "Metadata" filter):

```ts
// One-shot run
await client.runAgent({
  systemPrompt: "...",
  prompt: "...",
  metadata: { customer: "acme", env: "prod", workflow: "support_triage" },
});

// Session — every run created via `session.send` inherits these tags
const session = await client.createSession({
  systemPrompt: "...",
  metadata: { customer: "acme", env: "prod" },
});

// Per-message override; merged on top of the session's metadata
// (run-level keys win)
await session.send("trace this turn", {
  metadata: { trace_id: "trace_abc" },
});
```

Limits enforced server-side: max 16 entries; keys match `[A-Za-z0-9._-]{1,64}`;
values are strings ≤ 256 chars; serialized JSON ≤ 4 KB. Bigger payloads return
`400 invalid_request`.

Resuming a session from a different process re-binds your local tool
handlers; pass them in via `resumeSession`:

```ts
const session = await client.resumeSession(sessionId, {
  tools: [
    defineLocalTool({
      name: "today",
      description: "Get today's date as ISO 8601.",
      parameters: z.object({}),
      execute: () => new Date().toISOString().slice(0, 10),
    }),
  ],
});
```

## API reference

### `new MantyxClient(options)`

```ts
interface MantyxClientOptions {
  apiKey: string;
  workspaceSlug: string;
  baseUrl?: string;       // default: https://app.mantyx.io
  fetch?: typeof fetch;
  timeoutMs?: number;     // default: 60_000
}
```

### Methods

| Method                                        | Returns                              |
| --------------------------------------------- | ------------------------------------ |
| `listModels()`                                | `Promise<ModelCatalog>`              |
| `runAgent(spec)`                              | `Promise<RunResult>`                 |
| `streamAgent(spec)`                           | `AsyncIterable<RunEvent>`            |
| `createSession(spec)`                         | `Promise<AgentSession>`              |
| `resumeSession(sessionId, { tools? })`        | `Promise<AgentSession>`              |
| `endSession(sessionId)`                       | `Promise<void>`                      |
| `cancelRun(runId)`                            | `Promise<void>`                      |

### Tools

| Helper                     | Use case                                                                |
| -------------------------- | ----------------------------------------------------------------------- |
| `defineLocalTool(opts)`    | Define a local tool with a Zod parameter schema and handler.            |
| `defineLocalA2A(opts)`     | Local Agent2Agent peer — pass an `agentCardUrl`; the SDK fetches the card and speaks `message/send` for you. |
| `defineLocalMcp(opts)`     | Local MCP server — pass either a Streamable HTTP `url` or an stdio `command`; the SDK runs `Initialize` + `tools/list` + `tools/call` for you. |
| `mantyxTool(id)`           | Reference an existing MANTYX tool by id.                                |
| `mantyxPluginTool(name)`   | Reference an installed platform plugin tool by name.                    |
| `mantyxA2A(opts)`          | Remote Agent2Agent peer reachable from MANTYX (server-resolved).        |
| `mantyxMcp(opts)`          | Remote MCP server (Streamable HTTP) MANTYX dials and proxies for you.   |

### Errors

All thrown errors extend `MantyxError`. Common subclasses:

- `MantyxAuthError` — 401 from the server (bad / missing API key or
  OAuth access token).
- `MantyxScopeError` — 403 `insufficient_scope` from the server. The
  OAuth access token is missing one of the scopes the route demands;
  `err.requiredScopes` lists them so callers can drive a re-consent
  flow (e.g. "please re-authorise the app with `sessions:write`
  enabled"). API keys never trip this — it is OAuth-only.
- `MantyxOAuthError` — non-2xx from the OAuth token / revoke endpoint.
  Carries the RFC 6749 `oauthError` (`"invalid_grant"`, …) and the
  optional `oauthErrorDescription`. `invalid_grant` on refresh means
  the refresh token was revoked — route the user back to first sign-in.
- `MantyxNetworkError` — transport-layer failures.
- `MantyxRunError` — the agent loop terminated with an error.
- `MantyxToolError` — a local tool handler threw or timed out.

### OAuth 2.0 refresh

For long-running services, hand the SDK a `TokenSource` instead of a
static `accessToken` — the client refreshes proactively before
expiry and again on 401, retrying the original request exactly once.
Refresh tokens are **persistent and non-rotating** per
[`docs/oauth.md`](./docs/oauth.md): the caller persists the
`refreshToken` once at first sign-in (treat it as long-lived,
encrypted at rest) and the SDK re-mints access tokens from it
transparently.

```ts
import { MantyxClient, MantyxOAuthClient } from "@mantyx/sdk";

const oauth = new MantyxOAuthClient({
  clientId: process.env.MANTYX_OAUTH_CLIENT_ID!,        // mantyx_oa_…
  clientSecret: process.env.MANTYX_OAUTH_CLIENT_SECRET!, // mantyx_oas_…
});

// (1) Authorization-code: swap a `code` for the initial token pair, persist
//     the refresh token against the user record. See docs/oauth.md for the
//     full PKCE redirect dance the calling app is responsible for.
const initial = await oauth.exchangeAuthorizationCode({
  code: authCode,
  redirectUri: "https://app.example.com/cb",
  codeVerifier: storedVerifier,
});
await db.users.update(userId, { mantyxRefreshToken: initial.refreshToken });

// (2) End-user clients: build a refresh-driven TokenSource from the
//     persisted refresh token. The SDK calls it before every request and on
//     401s; concurrent requests collapse onto one refresh.
const client = new MantyxClient({
  tokenSource: oauth.refreshTokenSource({
    refreshToken: initial.refreshToken!,
    initialToken: initial,
  }),
  workspaceSlug: "acme",
});

// (3) Service-to-service: client_credentials sources never hold a refresh
//     token; they re-mint access tokens on demand.
const svcClient = new MantyxClient({
  tokenSource: oauth.clientCredentialsTokenSource({ scope: ["agents:invoke"] }),
  workspaceSlug: "acme",
});

// (4) Manual override is still supported for short-lived access tokens that
//     the caller already manages.
const oneShot = new MantyxClient({ accessToken: "mantyx_at_…", workspaceSlug: "acme" });
```

See [`docs/oauth.md`](./docs/oauth.md) for grant types, token formats,
and revocation.

## Examples

Self-contained example projects live under [`examples/`](./examples/):

- `examples/oneshot-local-tool` — minimal one-shot run with a local tool.
- `examples/session-chat` — interactive REPL on top of a session.
- `examples/mixed-tools` — combines local, MANTYX, and plugin tools.
- `examples/streaming` — token streaming to stdout.
- `examples/list-models` — model catalog + pick-and-run.
- `examples/a2a-tools` — remote (`mantyxA2A`) + local (`defineLocalA2A`) Agent2Agent peers.
- `examples/mcp-tools` — remote (`mantyxMcp`) + local (`defineLocalMcp`) MCP servers.

Each example is its own project (`package.json`, `tsconfig.json`, `README.md`)
so you can copy any one of them out of the repo and run it standalone.

## Wire protocol

This SDK is a thin client over a stable HTTP/SSE protocol. The full
specification ships with the package at
[`docs/agent-runs-protocol.md`](./docs/agent-runs-protocol.md). Anyone can
implement a compatible client in another language.

## Development

```bash
pnpm install
pnpm test          # unit + mock-server tests
pnpm typecheck
pnpm build         # emits dist/ (ESM + CJS + d.ts)
```

The SDK has zero internal `workspace:*` dependencies. `pnpm build` produces a
self-contained `dist/` ready for `npm publish`.

See [`CONTRIBUTING.md`](./CONTRIBUTING.md) for the contribution flow and
[`EXTRACT.md`](./EXTRACT.md) for the (very small) steps to lift this folder
into its own public repository.

## License

[Apache-2.0](../LICENSE)
