# mixed-tools

A single agent run that combines all three tool kinds:

- **Local tool** (`save_note`) — runs in this Python process.
- **MANTYX tool** (workspace `Tool` row referenced by `MANTYX_TOOL_ID`) — resolved server-side.
- **Plugin tool** (`@plugin/tool` referenced by `MANTYX_PLUGIN_TOOL`) — resolved server-side.

```bash
export MANTYX_API_KEY="mtx_live_..."
export MANTYX_WORKSPACE_SLUG="acme-corp"
export MANTYX_TOOL_ID="tool_cm6abc123"          # optional
export MANTYX_PLUGIN_TOOL="@web/search"          # optional

uv run python main.py
```

Either of `MANTYX_TOOL_ID` and `MANTYX_PLUGIN_TOOL` is optional — the example just adds them to the tool list when present.
