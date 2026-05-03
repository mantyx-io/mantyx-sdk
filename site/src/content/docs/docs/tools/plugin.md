---
title: Plugin tools
description: Reference an installed platform plugin tool by name.
sidebar:
  order: 3
---

A `mantyx_plugin` tool ref points at an installed platform plugin tool by `@plugin-slug/tool-name`.

```ts
import { mantyxPluginTool } from "@mantyx/sdk";

await client.runAgent({
  systemPrompt: "You are a research assistant.",
  prompt: "Look up the latest CPI release.",
  tools: [mantyxPluginTool("@web/search")],
});
```

```python
from mantyx import mantyx_plugin_tool

client.run_agent(
    system_prompt="You are a research assistant.",
    prompt="Look up the latest CPI release.",
    tools=[mantyx_plugin_tool("@web/search")],
)
```

```go
client.RunAgent(ctx, mantyx.RunSpec{
    SystemPrompt: "You are a research assistant.",
    Prompt:       "Look up the latest CPI release.",
    Tools:        []mantyx.ToolRef{mantyx.MantyxPluginTool("@web/search")},
})
```

The plugin must be installed on the workspace; otherwise the call returns `400 invalid_plugin`. Open **Workspace → Plugins** in the dashboard to see the installed plugins and their tools.
