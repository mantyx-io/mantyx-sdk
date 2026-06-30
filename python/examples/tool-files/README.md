# tool-files

A local tool that returns a **file** alongside its textual result.
`render_bar_chart` renders a dependency-free SVG chart in this process and hands
it back to the model via `ToolResult(result=..., files=[ToolResultFile(...)])`.
MANTYX surfaces the bytes to the model on its next turn as a native file part,
so the model can "see" the image it just asked for.

```bash
export MANTYX_API_KEY="mtx_live_..."
export MANTYX_WORKSPACE_SLUG="acme-corp"

# With uv (recommended)
uv run python main.py

# Or with pip
python -m venv .venv
. .venv/bin/activate
python -m pip install -e ../..
python main.py
```

Key bits:

- `execute` returns a `ToolResult` instead of a bare string.
- `ToolResultFile.data` is base64 with **no** `data:` URL prefix; `mime_type` must be an allowed attachment type.
- Files are ignored on the error path; raise to send a tool-error instead.
- Limits are server-enforced: up to 20 files, ~5 MB combined. For bigger artifacts, upload out of band and put a URL in `result`.

Once `mantyx-sdk` is published, drop the `[tool.uv.sources]` block in
`pyproject.toml` and pin the version directly via
`dependencies = ["mantyx-sdk>=0.1.0"]`.
