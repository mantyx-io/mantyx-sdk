# Example — Expose a MANTYX agent over A2A

Wraps a MANTYX agent as an [Agent2Agent](https://google-a2a.github.io/A2A/) peer using the official `@a2a-js/sdk` library mounted on Express. Other A2A agents — or the inverse `defineLocalA2A` / `mantyxA2A` tools in the same SDK — can then talk to it as a regular peer.

The `serveAgentOverA2A` helper publishes:

- the Agent Card at `/.well-known/agent-card.json`
- JSON-RPC at the root path
- HTTP+JSON/REST at `/v1`

It maps each incoming A2A `contextId` to a long-lived MANTYX session by default, so multi-turn `message/send` calls share conversational history without any extra plumbing.

## Run

```bash
cd ts/examples/a2a-expose
npm install                      # pulls @a2a-js/sdk + express
export MANTYX_API_KEY=mtx_live_...
export MANTYX_WORKSPACE_SLUG=acme-corp

# Either point at a persisted MANTYX agent…
export MANTYX_AGENT_ID=agent_cm6abc123

# …or rely on the default ephemeral system prompt baked into index.ts.
# To customize it: export SYSTEM_PROMPT="You are a billing assistant."

npm start
```

Then, from another terminal:

```bash
curl http://localhost:4000/.well-known/agent-card.json | jq .

curl -X POST http://localhost:4000 \
  -H "content-type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"message/send","params":{"message":{"kind":"message","messageId":"u1","role":"user","parts":[{"kind":"text","text":"Hi! Tell me a joke."}]}}}' | jq .
```

For multi-turn, reuse the `contextId` returned in the first response:

```bash
curl -X POST http://localhost:4000 \
  -H "content-type: application/json" \
  -d '{"jsonrpc":"2.0","id":2,"method":"message/send","params":{"message":{"kind":"message","messageId":"u2","role":"user","parts":[{"kind":"text","text":"Make it about birds."}],"contextId":"<contextId from previous reply>"}}}' | jq .
```

Streaming clients can hit the same endpoint with `method: "message/stream"` to receive `TaskStatusUpdateEvent`s with token deltas.

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
