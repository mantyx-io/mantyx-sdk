# @mantyx/sdk

The official TypeScript SDK for the [MANTYX](https://mantyx.com) agent
runtime. Define ephemeral agents that mix server-side MANTYX tools with
locally-executed tools, run them remotely, and stream events back into your
process.

- LLM loop runs on MANTYX (BYOK or platform-hosted models).
- Server-side tools (`mantyx`, `mantyx_plugin`) execute inside MANTYX.
- Local tools execute inside *your* process; the SDK shuttles inputs and
  outputs over an SSE stream + a tool-result POST.
- One-shot runs and multi-turn sessions, both with persisted observability.
- Authenticated with a single workspace API key.

For background, see the [agent-runs protocol spec](./docs/agent-runs-protocol.md).

## Install

```bash
npm install @mantyx/sdk zod
# or: pnpm add @mantyx/sdk zod
# or: yarn add @mantyx/sdk zod
```

Requires Node.js 18.17+ (for `fetch` and `ReadableStream`). `zod` is the only
runtime dependency the SDK adds; the rest is the standard library and your
own modules.

## Quickstart

```ts
import { z } from "zod";
import fs from "node:fs/promises";
import { MantyxClient, defineLocalTool, mantyxTool } from "@mantyx/sdk";

const client = new MantyxClient({
  apiKey: process.env.MANTYX_API_KEY!,
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

| Helper                     | Use case                                                     |
| -------------------------- | ------------------------------------------------------------ |
| `defineLocalTool(opts)`    | Define a local tool with a Zod parameter schema and handler. |
| `mantyxTool(id)`           | Reference an existing MANTYX tool by id.                     |
| `mantyxPluginTool(name)`   | Reference an installed platform plugin tool by name.         |

### Errors

All thrown errors extend `MantyxError`. Common subclasses:

- `MantyxAuthError` — 401/403 from the server (bad API key, wrong workspace).
- `MantyxNetworkError` — transport-layer failures.
- `MantyxRunError` — the agent loop terminated with an error.
- `MantyxToolError` — a local tool handler threw or timed out.

## Examples

Self-contained example projects live under [`examples/`](./examples/):

- `examples/oneshot-local-tool` — minimal one-shot run with a local tool.
- `examples/session-chat` — interactive REPL on top of a session.
- `examples/mixed-tools` — combines local, MANTYX, and plugin tools.
- `examples/streaming` — token streaming to stdout.
- `examples/list-models` — model catalog + pick-and-run.

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
