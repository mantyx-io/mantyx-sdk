# message-attachments

Send file inputs on one-shot runs and session turns. The example ships a small `sample.txt` and base64-encodes it as an inline `input_file` attachment.

Three modes:

| Mode        | What it demonstrates                                      |
| ----------- | --------------------------------------------------------- |
| `one-shot`  | `run_agent(prompt=..., attachments=[...])` (default)      |
| `messages`  | `run_agent(messages=[...])` with a multi-role transcript  |
| `session`   | `session.send(..., attachments=[...])` + event replay     |

```bash
export MANTYX_API_KEY="mtx_live_..."
export MANTYX_WORKSPACE_SLUG="acme-corp"

uv run python main.py one-shot
uv run python main.py messages
uv run python main.py session
```

Typed helpers used from `mantyx`: `input_file_attachment`, `ConversationMessage`, `InputFileAttachment`, `MessageAttachment`, and `RunEvent.user_message()` for replay metadata.
