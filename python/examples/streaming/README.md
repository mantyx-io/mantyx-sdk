# streaming

Use `client.stream_agent(...)` to consume every event from a one-shot run as it arrives. Prints assistant deltas inline and a JSON dump of every other event so you can see the full event sequence.

```bash
export MANTYX_API_KEY="mtx_live_..."
export MANTYX_WORKSPACE_SLUG="acme-corp"

uv run python main.py
```

For an async equivalent, swap `MantyxClient` for `AsyncMantyxClient` and use `async for event in client.stream_agent(...)`.
