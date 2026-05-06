# a2a-tools

Combine a **remote** Agent2Agent peer (`mantyx_a2a` — MANTYX dials it
directly) with a **local** A2A peer (`define_local_a2a` — the SDK fetches
the Agent Card from `agent_card_url` and dials the peer over A2A's
`message/send` JSON-RPC on MANTYX's behalf, for peers that live behind a
firewall, on the user's device, or otherwise out of the platform's reach).

The model addresses both with the same `{ "message": str }` argument shape
described in `docs/agent-runs-protocol.md` §4.2, so the same prompt works
unchanged whichever flavour is configured.

```bash
export MANTYX_API_KEY="mtx_live_..."
export MANTYX_WORKSPACE_SLUG="acme-corp"

# Required — the SDK auto-fetches the card on the first run.
export HR_AGENT_CARD_URL="https://hr.intranet/.well-known/agent-card.json"
# export HR_AGENT_TOKEN="..."

# Optional — register the public billing peer too:
# export BILLING_AGENT_CARD_URL="https://billing.example.com/.well-known/agent-card.json"
# export BILLING_AGENT_TOKEN="..."

uv run python main.py "How do I expense client lunches?"
```

`define_local_a2a` is **URL-only**: the SDK fetches the card the first
time you call `run_agent` / `session.send`, ships it inline with the
spec, and speaks A2A's `message/send` against `agent_card.url` for every
`local_tool_call` event MANTYX emits for this peer.
