# mixed-tools

Defines an agent with three tool kinds at once:

- `MantyxTool(id)` — a workspace `Tool` row resolved server-side.
- `MantyxPluginTool(name)` — a built-in MANTYX plugin tool.
- `LocalTool(LocalToolSpec{...})` — a local tool that runs in this Go process.

```bash
export MANTYX_API_KEY="mk_..."
export MANTYX_WORKSPACE_SLUG="acme-corp"

# Optional: ids must exist in your workspace.
export MANTYX_TOOL_ID="tool_abc"
export MANTYX_PLUGIN_TOOL_NAME="@some-plugin/some_tool"

go run .
```
