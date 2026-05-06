# a2a-tools

Combine a **remote** Agent2Agent peer (`mantyxA2A` — MANTYX dials it
directly) with a **local** A2A peer (`defineLocalA2A` — the SDK invokes it
on MANTYX's behalf when the peer lives behind a firewall, on the user's
device, or otherwise out of the platform's reach).

The model addresses both with the same `{ message: string }` argument shape
described in `docs/agent-runs-protocol.md` §4.2, so the same prompt works
unchanged whichever flavour is configured.

```bash
export MANTYX_API_KEY="mk_..."
export MANTYX_WORKSPACE_SLUG="acme-corp"
# Optional — drop the public peer if you don't have one handy:
# export BILLING_AGENT_CARD_URL="https://billing.example.com/.well-known/agent-card.json"
# export BILLING_AGENT_TOKEN="..."

npm install
npm start "How do I expense client lunches?"
```

The example always registers the on-prem HR agent (a stub that echoes the
inbound message). Set `BILLING_AGENT_CARD_URL` to also enable the public
billing agent. Swap the local `execute` for a real A2A client call when you
plug this into your intranet.

The example depends on the SDK via a local path (`"@mantyx/sdk": "file:../.."`).
If you copy this directory out of the monorepo, replace that with the
published version before running `npm install`.
