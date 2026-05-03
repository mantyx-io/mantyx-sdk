---
title: Persisted agents (agentId)
description: Trigger an existing workspace agent by id and merge in local tools.
sidebar:
  order: 2
---

Pass `agentId` (TS / Python) / `AgentID` (Go) to run an agent that already lives in your workspace. The server hydrates the agent's system prompt, model, and configured tools (memory, skills, plugin tools, …) from the `Agent` row at run time. Any `tools` you pass on the call are **merged on top** — typically `local` tools the agent should be able to call back into for that single run.

## When to use this

- The agent is already configured in the dashboard with a system prompt, memory, skills, and tools, and you don't want to duplicate that wiring in code.
- You want product/non-engineering teammates to edit the agent's behaviour from the dashboard without code changes.
- You need to attach process-local tools (filesystem, internal HTTP services, native libraries) for a single run, without editing the agent's stored tool list.

## Minimal call

```ts
import { MantyxClient } from "@mantyx/sdk";

const client = new MantyxClient({ apiKey: "...", workspaceSlug: "acme" });

const result = await client.runAgent({
  agentId: "agent_cm6abc123",
  prompt: "Summarise the latest deploy logs.",
});
console.log(result.text);
```

## With extra local tools

```ts
import { defineLocalTool, MantyxClient } from "@mantyx/sdk";
import { z } from "zod";
import { readFileSync } from "node:fs";

const client = new MantyxClient({ apiKey: "...", workspaceSlug: "acme" });

const result = await client.runAgent({
  agentId: "agent_cm6abc123",
  prompt: "Pull the latest deploy logs and summarise them.",
  tools: [
    defineLocalTool({
      name: "read_local_file",
      parameters: z.object({ path: z.string() }),
      execute: ({ path }) => readFileSync(path, "utf8"),
    }),
  ],
});
```

## Behaviour notes

- `systemPrompt` becomes optional when `agentId` is set; if both are sent, the **agent's stored prompt wins**.
- `modelId` is also optional: omit it to use the agent's configured LLM provider, or pass it to override the model for this run.
- The API key must be authorized for the agent (an empty `agentIds` allowlist on the key counts as "all agents in the workspace"). Otherwise the call returns `403 forbidden`.
- An unknown / cross-workspace `agentId` returns `403`; a malformed `agentId` returns `400`.

The same `agentId` field works on `client.createSession({ ... })` for multi-turn conversations against a persisted agent — see [Sessions](/docs/agents/sessions/).
