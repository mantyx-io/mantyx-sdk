# Trigger a persisted MANTYX agent (`AgentID`)

Runs an agent that already exists in your workspace, with one extra `LocalTool`
merged on top so the SDK process is reachable for that run.

## Usage

```bash
export MANTYX_API_KEY="mk_..."
export MANTYX_WORKSPACE_SLUG="acme-corp"
export MANTYX_AGENT_ID="agent_cm6abc123"

# Optional, for self-hosted MANTYX:
# export MANTYX_BASE_URL="https://api.mantyx.com"

go mod tidy
go run .
```

## What it shows

- `RunSpec.AgentID` triggers a persisted MANTYX agent. The system prompt,
  configured LLM provider, and all the agent's server-side tools (memory,
  skills, plugin tools, …) come from the workspace `Agent` row.
- The `Tools` slice is **merged on top** of the agent's tools, so you can
  add `LocalTool` refs (or extra `MantyxTool` / `MantyxPluginTool` refs)
  for this run without editing the agent.
- `SystemPrompt` is omitted — it is inherited from the agent.

The API key must be authorized for the agent (an empty `agentIds` allowlist
on the key counts as "all agents in the workspace"). Otherwise the call
returns `403`.
