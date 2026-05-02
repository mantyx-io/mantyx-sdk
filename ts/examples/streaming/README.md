# streaming

Use `client.streamAgent()` to consume raw run events as they arrive. Prints
assistant deltas to stdout in real time and a one-line summary per non-text
event so you can see the timeline.

```bash
export MANTYX_API_KEY="mk_..."
export MANTYX_WORKSPACE_SLUG="acme-corp"

pnpm install
pnpm start
```
