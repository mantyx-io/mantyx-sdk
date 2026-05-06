# Example — Expose a MANTYX agent over A2A

Wraps a MANTYX agent as an [Agent2Agent](https://google-a2a.github.io/A2A/) peer using the official `a2a-sdk` Python package mounted on Starlette + uvicorn. Other A2A agents (or the inverse `define_local_a2a` / `mantyx_a2a` tools in the same SDK) can then talk to it as a regular peer.

`serve_agent_over_a2a` publishes:

- the Agent Card at `GET /.well-known/agent-card.json`
- A2A JSON-RPC at the root path
- A2A HTTP+JSON/REST at `/v1`

It maps each incoming A2A `contextId` to a long-lived MANTYX session by default, so multi-turn `message/send` calls share conversational history without any extra plumbing.

## Run

```bash
cd python/examples/a2a-expose
uv sync                              # pulls mantyx-sdk + the [a2a-server] extra
export MANTYX_API_KEY=mtx_live_...
export MANTYX_WORKSPACE_SLUG=acme-corp

# Either point at a persisted MANTYX agent…
export MANTYX_AGENT_ID=agent_cm6abc123

# …or rely on the default ephemeral system prompt baked into main.py.
# export SYSTEM_PROMPT="You are a billing assistant."

uv run main.py
```

Then probe it from another terminal:

```bash
curl http://localhost:4000/.well-known/agent-card.json | jq .

curl -X POST http://localhost:4000 \
  -H "content-type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"message/send","params":{"message":{"kind":"message","messageId":"u1","role":"user","parts":[{"kind":"text","text":"Hi! Tell me a joke."}]}}}' | jq .
```

For multi-turn, reuse the `contextId` returned in the first response.

## Environment variables

| Variable | Required | Notes |
| --- | --- | --- |
| `MANTYX_API_KEY` | yes | Workspace API key. |
| `MANTYX_WORKSPACE_SLUG` | yes | Workspace slug (e.g. `acme-corp`). |
| `MANTYX_AGENT_ID` | no | Persisted MANTYX agent id; when set, system prompt + model + tools are hydrated server-side. |
| `SYSTEM_PROMPT` | no | Override the default ephemeral system prompt. Ignored when `MANTYX_AGENT_ID` is set. |
| `MODEL_ID` | no | Override the default model. Ignored when `MANTYX_AGENT_ID` is set. |
| `PORT` | no | Defaults to `4000`. |
| `PUBLIC_URL` | no | Defaults to `http://localhost:<PORT>`. Use the public origin when deploying. |
| `AGENT_NAME` / `AGENT_DESCRIPTION` | no | Override the published Agent Card. |

## Requires Python 3.10+

The optional `[a2a-server]` extra brings in `a2a-sdk` (which itself needs Python 3.10+) plus `uvicorn`. The base `mantyx-sdk` install stays unchanged for projects that don't need to expose an agent.
