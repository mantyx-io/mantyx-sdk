---
title: Messages & file attachments
description: Multi-role conversation input and file uploads on agent runs and session turns.
sidebar:
  order: 4
---

Runs accept either a single `prompt` string or a multi-role `messages` array. File inputs attach to the **last user message** in that array (max 20 per turn). See [Agent-runs protocol §4.0.1](/docs/protocol/#401-multi-role-messages-and-file-inputs) for validation rules (MIME allowlist, 5 MB inline cap, HTTPS-only URLs).

Runnable examples live under `examples/message-attachments` for [TypeScript](https://github.com/mantyx-io/mantyx-sdk/tree/main/ts/examples/message-attachments), [Go](https://github.com/mantyx-io/mantyx-sdk/tree/main/go/examples/message-attachments), and [Python](https://github.com/mantyx-io/mantyx-sdk/tree/main/python/examples/message-attachments). See also the [Examples](/docs/examples/) index.

## Supported file types

- **Audio:** M4A (`audio/mp4`), MP3 (`audio/mpeg`), WAV (`audio/wav`), and WebM audio (`audio/webm`).
- **Images:** JPEG, PNG, WebP, and GIF.
- **Documents:** PDF, DOCX, TXT, Markdown, CSV/TSV, JSON, XML, and HTML. Other `text/*` files are also accepted.

M4A recordings from mobile devices must use `audio/mp4`:

```ts
const recording = fs.readFileSync("recording.m4a").toString("base64");

await client.runAgent({
  systemPrompt: "You analyze voice notes.",
  prompt: "Summarize this recording.",
  attachments: [
    inputFileAttachment({
      mimeType: "audio/mp4",
      filename: "recording.m4a",
      data: recording,
    }),
  ],
});
```

Accepted files are forwarded to the selected model provider. Choose a model that supports the file modality you attach.

## Single prompt with files

Pass `attachments` alongside `prompt` — the SDK builds the wire `messages` shape for you.

```ts
import {
  MantyxClient,
  inputFileAttachment,
  inputFileUrlAttachment,
} from "@mantyx/sdk";
import fs from "node:fs";

const pdf = fs.readFileSync("report.pdf").toString("base64");

const result = await client.runAgent({
  systemPrompt: "You summarize uploaded documents.",
  prompt: "What's in this PDF?",
  attachments: [
    inputFileAttachment({
      mimeType: "application/pdf",
      filename: "report.pdf",
      data: pdf,
    }),
    inputFileUrlAttachment({
      url: "https://example.com/logo.png",
      mimeType: "image/png",
    }),
  ],
});
```

```python
import base64
from mantyx import MantyxClient, input_file_attachment, input_file_url_attachment

pdf = base64.b64encode(open("report.pdf", "rb").read()).decode()

result = client.run_agent(
    system_prompt="You summarize uploaded documents.",
    prompt="What's in this PDF?",
    attachments=[
        input_file_attachment(
            mime_type="application/pdf",
            filename="report.pdf",
            data=pdf,
        ),
        input_file_url_attachment(
            url="https://example.com/logo.png",
            mime_type="image/png",
        ),
    ],
)
```

```go
import (
    "encoding/base64"
    "os"

    mantyx "github.com/mantyx-ai/mantyx-sdk/go"
)

raw, _ := os.ReadFile("report.pdf")
pdf := base64.StdEncoding.EncodeToString(raw)

result, err := client.RunAgent(ctx, mantyx.RunSpec{
    SystemPrompt: "You summarize uploaded documents.",
    Prompt:       "What's in this PDF?",
    Attachments: []map[string]any{
        mantyx.InputFileAttachment("application/pdf", "report.pdf", pdf),
        mantyx.InputFileURLAttachment("https://example.com/logo.png", "image/png", ""),
    },
})
```

## Multi-role `messages`

Send prior turns inline (one-shot runs) or as new turns on a [session](/docs/agents/sessions/). The last non-system message must be `role: "user"`.

```ts
await client.runAgent({
  modelId: "openai:gpt-5.5",
  messages: [
    { role: "system", content: "You are a terse assistant." },
    { role: "user", content: "Earlier question" },
    { role: "assistant", content: "Earlier answer" },
    {
      role: "user",
      content: "What's in this file?",
      attachments: [
        inputFileAttachment({
          mimeType: "application/pdf",
          filename: "report.pdf",
          data: pdf,
        }),
      ],
    },
  ],
});
```

```python
client.run_agent(
    model_id="openai:gpt-5.5",
    messages=[
        {"role": "system", "content": "You are a terse assistant."},
        {"role": "user", "content": "Earlier question"},
        {"role": "assistant", "content": "Earlier answer"},
        {
            "role": "user",
            "content": "What's in this file?",
            "attachments": [
                input_file_attachment(
                    mime_type="application/pdf",
                    filename="report.pdf",
                    data=pdf,
                ),
            ],
        },
    ],
)
```

## Session turns

`session.send` accepts the same shapes: a prompt string (with optional `attachments`), or a `messages` array.

```ts
await session.send("Summarize this deck.", {
  attachments: [inputFileAttachment({ mimeType: "application/pdf", filename: "deck.pdf", data: pdf })],
});

// or pass a full messages array:
await session.send([
  { role: "user", content: "Follow-up on the deck", attachments: [...] },
]);
```

```python
session.send(
    "Summarize this deck.",
    attachments=[input_file_attachment(mime_type="application/pdf", filename="deck.pdf", data=pdf)],
)
```

```go
_, err := session.Send(ctx, "Summarize this deck.",
    mantyx.WithAttachments(mantyx.InputFileAttachment("application/pdf", "deck.pdf", pdf)),
)
```

## Replay metadata

When you restore a session via `getSessionEvents` / `session.events`, `user_message` frames may include an `attachments` array with **metadata only** (no bytes): `{ type: "input_file", mimeType, filename, size }` or `{ type: "input_file_url", url, mimeType?, filename? }`. Use this to render file chips in a UI without re-fetching the original upload.
