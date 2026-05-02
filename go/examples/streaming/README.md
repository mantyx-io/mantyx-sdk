# streaming

Use `Client.StreamAgent()` to consume run events as a Go channel. Prints
assistant deltas to stdout in real time and a one-line summary per non-text
event.

```bash
export MANTYX_API_KEY="mk_..."
export MANTYX_WORKSPACE_SLUG="acme-corp"

go run .
```
