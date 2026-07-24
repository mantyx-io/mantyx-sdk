# message-attachments

Send file inputs on one-shot runs and session turns. The example ships a small `sample.txt` and base64-encodes it as an inline `input_file` attachment.

The same helper accepts audio attachments. Use `audio/mp4` for mobile M4A recordings; MP3, WAV, and WebM audio use `audio/mpeg`, `audio/wav`, and `audio/webm`.

Three modes:

| Mode        | What it demonstrates                                      |
| ----------- | --------------------------------------------------------- |
| `one-shot`  | `RunSpec{ Prompt, Attachments }` (default)                |
| `messages`  | `RunSpec{ Messages }` with a multi-role transcript        |
| `session`   | `session.Send` with `WithAttachments` + event replay        |

```bash
export MANTYX_API_KEY="mk_..."
export MANTYX_WORKSPACE_SLUG="acme-corp"

go run . one-shot
go run . messages
go run . session
```

Helpers: `mantyx.InputFileAttachment`, `mantyx.Message.Attachments`, and `WithAttachments` for session turns.
