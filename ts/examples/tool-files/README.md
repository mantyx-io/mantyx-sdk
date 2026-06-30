# tool-files

A local tool that returns a **file** alongside its textual result. `render_bar_chart`
renders a dependency-free SVG chart in your process and hands it back to the
model via a `LocalToolResult` (`{ result, files }`). MANTYX surfaces the bytes
to the model on its next turn as a native file part, so the model can "see" the
image it just asked for.

```bash
export MANTYX_API_KEY="mk_..."
export MANTYX_WORKSPACE_SLUG="acme-corp"

npm install
npm start
```

Key bits:

- `execute` returns `{ result, files: [{ filename, mimeType, data }] }` instead of a bare string.
- `data` is base64 with **no** `data:` URL prefix.
- Files are ignored on the error path; throw to send a tool-error instead.
- Limits are server-enforced: up to 20 files, allowed MIME types, ~5 MB combined. For bigger artifacts, upload out of band and put a URL in `result`.

The example depends on the SDK via a local path (`"@mantyx/sdk": "file:../.."`).
If you copy this directory out of the monorepo, replace that with the published
version before running `npm install`.
