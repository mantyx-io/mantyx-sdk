# mixed-tools

Defines an agent with three tool kinds at once:

- `mantyxTool(toolId)` — a workspace `Tool` row (HTTP / code / MCP / plugin)
  resolved server-side.
- `mantyxPluginTool("@plugin-slug/tool-name")` — a built-in MANTYX plugin tool.
- `defineLocalTool({ ... })` — a local tool that runs in your process.

```bash
export MANTYX_API_KEY="mk_..."
export MANTYX_WORKSPACE_SLUG="acme-corp"

# Optional: ids must exist in your workspace.
export MANTYX_TOOL_ID="tool_abc"
export MANTYX_PLUGIN_TOOL_NAME="@some-plugin/some_tool"

pnpm install
pnpm start
```
