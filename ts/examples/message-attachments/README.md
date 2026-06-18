# message-attachments

Send file inputs on one-shot runs and session turns. The example ships a small `sample.txt` and base64-encodes it as an inline `input_file` attachment.

Three modes:

| Mode        | What it demonstrates                                      |
| ----------- | --------------------------------------------------------- |
| `one-shot`  | `runAgent({ prompt, attachments })` (default)             |
| `messages`  | `runAgent({ messages })` with a multi-role transcript     |
| `session`   | `session.send(prompt, { attachments })` + event replay      |

```bash
export MANTYX_API_KEY="mk_..."
export MANTYX_WORKSPACE_SLUG="acme-corp"

npm install
npm start
npm run start:messages
npm run start:session
```

Helpers: `inputFileAttachment`, `ConversationMessage`, and `UserMessageEvent.attachments` on replay frames.
