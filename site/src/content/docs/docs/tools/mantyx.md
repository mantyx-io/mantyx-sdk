---
title: MANTYX tools
description: Reference an existing workspace Tool by id; the server runs it.
sidebar:
  order: 2
---

A `mantyx` tool ref points at a workspace `Tool` row by id. The MANTYX server resolves it at run time and executes it server-side — you never see the call shape inside your process.

```ts
import { mantyxTool } from "@mantyx/sdk";

await client.runAgent({
  systemPrompt: "...",
  prompt: "...",
  tools: [mantyxTool("tool_cm6abc123")],
});
```

```python
from mantyx import mantyx_tool

client.run_agent(
    system_prompt="...",
    prompt="...",
    tools=[mantyx_tool("tool_cm6abc123")],
)
```

```go
client.RunAgent(ctx, mantyx.RunSpec{
    SystemPrompt: "...",
    Prompt:       "...",
    Tools:        []mantyx.ToolRef{mantyx.MantyxTool("tool_cm6abc123")},
})
```

## When to use this

- The tool is configured in the dashboard (HTTP / Code / Plugin) and you want the LLM to be able to call it.
- The tool needs MANTYX-side credentials, secrets, or memory access.
- You want non-engineering teammates to edit the tool body without code changes.

## Discovering tool ids

Open **Workspace → Tools** in the dashboard. The id is shown next to each tool and follows the `tool_<cuid>` shape.

If you're calling a persisted MANTYX agent (via [`agentId`](/docs/agents/persisted/)), you usually don't need to pass `mantyx` tool refs at all — the server hydrates them from the agent's configuration.
