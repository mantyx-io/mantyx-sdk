# list-models

Calls `client.list_models()`, pretty-prints the catalog, then runs a smoke-test agent on the default model.

```bash
export MANTYX_API_KEY="mtx_live_..."
export MANTYX_WORKSPACE_SLUG="acme-corp"

uv run python main.py
```
