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

Three flavours, all carried inside the agent spec:

| `kind` | Resolved by | Notes |
| --- | --- | --- |
| `mantyx` | server | A workspace `Tool` row referenced by id. See [MANTYX tools](/docs/tools/mantyx/). |
| `mantyx_plugin` | server | A platform plugin tool referenced by `@plugin/tool` name. See [Plugin tools](/docs/tools/plugin/). |
| `local` | client | Defined and executed inside your SDK process. See [Local tools](/docs/tools/local/). |

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

## Budgeting tool turns

```ts
await client.runAgent({
  systemPrompt: "...",
  prompt: "...",
  budgets: { maxToolTurns: 8 }, // hard cap
});
```

If the model wants to call tools more than `maxToolTurns` times, the run terminates with `result.subtype = "error_max_tool_turns"`.
