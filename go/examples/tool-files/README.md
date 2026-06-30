# tool-files

A local tool that returns a **file** alongside its textual result.
`render_bar_chart` renders a dependency-free SVG chart in this process and hands
it back to the model via `mantyx.ToolResult`. MANTYX surfaces the bytes to the
model on its next turn as a native file part, so the model can "see" the image
it just asked for.

```bash
export MANTYX_API_KEY="mk_..."
export MANTYX_WORKSPACE_SLUG="acme-corp"

go run .
```

Key bits:

- `Execute` returns `(mantyx.ToolResult, error)` instead of `(string, error)`.
- `ToolResultFile.Data` is base64 with **no** `data:` URL prefix; `MimeType` must be an allowed attachment type.
- A non-`nil` `error` is forwarded as a tool-error; files on that path are ignored.
- Returning `ToolResult` skips `OutputSchema` inference — the `{result, files}` envelope is transport, not a model-facing output contract.
- Limits are server-enforced: up to 20 files, ~5 MB combined. For bigger artifacts, upload out of band and put a URL in `Result`.
