# `mantyx-sdk` examples

Each subdirectory is a **self-contained project** with its own `pyproject.toml` and `README.md`. You can copy any one of them out of the monorepo and run it as a standalone Python project.

| Folder                | What it shows                                                                |
| --------------------- | ---------------------------------------------------------------------------- |
| `oneshot-local-tool/` | A one-shot run with one local tool (`read_file`).                            |
| `agent-by-id/`        | Trigger a persisted MANTYX agent by `agent_id` and merge in a local tool.    |
| `session-chat/`       | Multi-turn `AgentSession` driving an interactive REPL.                       |
| `mixed-tools/`        | Combine `mantyx_tool`, `mantyx_plugin_tool`, and a local tool in one agent.  |
| `streaming/`          | Use `client.stream_agent()` to print assistant deltas to stdout.             |
| `list-models/`        | Call `client.list_models()`, pretty-print, then run an agent on the first.   |

All examples read configuration from environment variables:

```bash
export MANTYX_API_KEY="mtx_live_..."
export MANTYX_WORKSPACE_SLUG="acme-corp"
# Optional:
export MANTYX_BASE_URL="https://api.mantyx.com"     # self-hosted? override here
```

To run any example:

```bash
cd python/examples/oneshot-local-tool

# Recommended: run with `uv` (no virtualenv setup needed).
uv run python main.py

# Or with pip + a virtualenv:
python -m venv .venv
. .venv/bin/activate
python -m pip install -e .
python main.py
```

When developing inside the monorepo, `pip install -e .` resolves the `mantyx-sdk` dependency against the source under `python/`. Once `mantyx-sdk` is published to PyPI you can drop `path = "../.."` from each example's `pyproject.toml` (it's commented in each one).
