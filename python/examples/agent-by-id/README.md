# agent-by-id

Run a **persisted** MANTYX agent (one configured in the dashboard) by `agent_id` and merge in a local tool for this single run.

When `agent_id` is set, the server hydrates the agent's stored system prompt, model, memory, skills, and configured tool list at run time. The `tools` you pass on the call are merged on top — typically `local` tools you want the agent to be able to call back into for that run, without editing the agent's stored tool list.

```bash
export MANTYX_API_KEY="mtx_live_..."
export MANTYX_WORKSPACE_SLUG="acme-corp"
export MANTYX_AGENT_ID="agent_cm6abc123"

uv run python main.py
```

The API key must be authorized for `MANTYX_AGENT_ID` (or have an empty `agentIds` allowlist, which means "any agent in the workspace"). Otherwise the call returns `403 forbidden`.
