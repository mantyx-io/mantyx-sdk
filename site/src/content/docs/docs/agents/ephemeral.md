---
title: Ephemeral agents
description: Define an agent's system prompt, model, and tools at the call site.
sidebar:
  order: 1
---

An **ephemeral agent** is described by the request rather than persisted as a row in MANTYX's `Agent` table. The full spec (system prompt, model, tools) is stored as part of each session/run for observability but is not editable from the dashboard.

This is the right choice when:

- You're prototyping or scripting and don't need a stable agent across runs.
- You want maximum control over the system prompt at the call site.
- The set of tools changes per call.

## Minimal one-shot

```ts
const result = await client.runAgent({
  systemPrompt: "You are a helpful assistant.",
  prompt: "What's the capital of France?",
});
console.log(result.text);
```

## Adding tools

Seven flavours, all carried inside the agent spec. The split is along **who can reach the resource** — server-resolved tools run inside MANTYX; client-resolved tools run inside your SDK process and shuttle results back over the agent loop.

| `kind` | Resolved by | Notes |
| --- | --- | --- |
| `mantyx` | server | A workspace `Tool` row referenced by id. See [MANTYX tools](/docs/tools/mantyx/). |
| `mantyx_plugin` | server | A platform plugin tool referenced by `@plugin/tool` name. See [Plugin tools](/docs/tools/plugin/). |
| `a2a` | server | A remote [Agent2Agent](/docs/tools/a2a/) peer MANTYX dials directly. |
| `mcp` | server | A remote [MCP](/docs/tools/mcp/) server (Streamable HTTP) MANTYX lists and proxies. |
| `local` | client | Defined and executed inside your SDK process. See [Local tools](/docs/tools/local/). |
| `a2a_local` | client | An [A2A](/docs/tools/a2a/) peer only the SDK can reach (intranet, on-device). |
| `mcp_local` | client | An [MCP](/docs/tools/mcp/) server only the SDK can reach (stdio, intranet). |

```ts
import { defineLocalTool, mantyxTool, mantyxPluginTool } from "@mantyx/sdk";
import { z } from "zod";

await client.runAgent({
  systemPrompt: "You are a research assistant.",
  prompt: "Look up the latest CPI release and summarise it.",
  tools: [
    mantyxPluginTool("@web/search"),
    mantyxTool("tool_cm6abc123"),
    defineLocalTool({
      name: "save_note",
      parameters: z.object({ title: z.string(), body: z.string() }),
      execute: async ({ title, body }) => {
        // ...write to disk
        return "ok";
      },
    }),
  ],
});
```

## Picking a model

Pass `modelId` (TypeScript / Python) or `ModelID` (Go) to override the workspace default. See [Models](/docs/models/) for the supported shorthand syntax.

## Tuning thinking effort

Pass `reasoningLevel` (TypeScript) / `reasoning_level` (Python) / `ReasoningLevel` (Go) to dial provider extended thinking on reasoning models. The value is forwarded unchanged to the server, which maps it onto each LLM's native dial. Accepts a string anchor (`"off"`, `"low"`, `"medium"`, `"high"`) or an integer in `[0, 100]` — see [Reasoning level](/docs/reasoning/) for the full table.

```ts
await client.runAgent({
  systemPrompt: "...",
  prompt: "Plan a multi-week migration.",
  reasoningLevel: "high",
});
```

## Budgeting tool turns

```ts
await client.runAgent({
  systemPrompt: "...",
  prompt: "...",
  budgets: { maxToolTurns: 8 }, // hard cap
});
```

If the model wants to call tools more than `maxToolTurns` times, the run terminates with `result.subtype = "error_max_tool_turns"`.

## Run guards (loop detection & tool budgets)

Every run has two opt-in guards that intervene when the agent loop misbehaves: **loop detection** soft-nudges the model when it keeps repeating the same `(toolName, args)` batch and forces a clean finalise turn if it keeps looping, and **tool budgets** cap how many times each tool may execute over the lifetime of the run. Both come with sensible defaults; tune them per-call (or disable them) when you need to.

```ts
await client.runAgent({
  systemPrompt: "...",
  prompt: "Iterate freely until you converge.",
  loopDetection: { consecutiveThreshold: 2, hardCutoffThreshold: 4 }, // tighter
  toolBudgets:   { recall: { maxCalls: 8 } },                         // raise default
});
```

See [Run guards](/docs/run-guards/) for the full inventory, defaults, and per-SDK syntax.
