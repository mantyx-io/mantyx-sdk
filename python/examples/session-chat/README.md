# session-chat

An interactive REPL on top of `AgentSession`. Each user line is sent as a new turn; the server owns the message history.

```bash
export MANTYX_API_KEY="mtx_live_..."
export MANTYX_WORKSPACE_SLUG="acme-corp"

uv run python main.py
# you> what day is it?
# agent> Today is 2026-05-03.
# you> and tomorrow?
# agent> Tomorrow is 2026-05-04.
# you> ^D
# session ended.
```
