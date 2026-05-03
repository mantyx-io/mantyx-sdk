# oneshot-local-tool

A one-shot agent run that ships a single local tool (`read_file`). The MANTYX runtime calls the local tool from your process, then continues the agent loop on the server with the tool's output.

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

Once `mantyx-sdk` is published, drop the `[tool.uv.sources]` block (or the equivalent in your tool) in `pyproject.toml` and pin the version directly via `dependencies = ["mantyx-sdk>=0.1.0"]`.
