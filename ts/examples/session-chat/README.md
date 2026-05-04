# session-chat

Interactive multi-turn REPL on top of `client.createSession`. Each line you
type is sent as a user turn; the assistant's reply is streamed back.

```bash
export MANTYX_API_KEY="mk_..."
export MANTYX_WORKSPACE_SLUG="acme-corp"

npm install
npm start
> hello
> what was the last thing I asked?
```

Press `Ctrl+D` (EOF) to end the session — the SDK calls `session.end()` so the
server marks the row terminal.
