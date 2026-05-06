# a2a-tools

Combine a **remote** Agent2Agent peer (`mantyx.MantyxA2A` — MANTYX dials it
directly) with a **local** A2A peer (`mantyx.LocalA2A` — the SDK fetches the
Agent Card from `AgentCardURL` and dials the peer over A2A's `message/send`
JSON-RPC on MANTYX's behalf, for peers that live behind a firewall, on the
user's device, or otherwise out of the platform's reach).

The model addresses both with the same `{"message": string}` argument shape
described in `docs/agent-runs-protocol.md` §4.2, so the same prompt works
unchanged whichever flavour is configured.

```bash
export MANTYX_API_KEY="mk_..."
export MANTYX_WORKSPACE_SLUG="acme-corp"

# Required — the SDK auto-fetches the card on the first run.
export HR_AGENT_CARD_URL="https://hr.intranet/.well-known/agent-card.json"
# export HR_AGENT_TOKEN="..."

# Optional — register the public billing peer too:
# export BILLING_AGENT_CARD_URL="https://billing.example.com/.well-known/agent-card.json"
# export BILLING_AGENT_TOKEN="..."

go run . "How do I expense client lunches?"
```

`mantyx.LocalA2A` is **URL-only**: the SDK fetches the card the first
time you call `RunAgent` / `Session.Send`, ships it inline with the
spec, and speaks A2A's `message/send` against `agentCard.url` for every
`local_tool_call` event MANTYX emits for this peer.

This module's `go.mod` has a `replace` directive pointing back at `../..`
so it builds against the in-tree SDK. When you copy it out of the repo,
remove that `replace` and run `go mod tidy`.
