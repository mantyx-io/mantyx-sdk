# Agent guide — local verification

Run these checks from the **repo root** before opening a PR or finishing a change.
They mirror [`.github/workflows/ci.yml`](.github/workflows/ci.yml).

## Quick pass (CI-equivalent)

Prerequisites: Node 20+, Go 1.24+, Python 3.12+ with `uv` or `pip`, and dependencies installed in each package (`npm install` in `ts/` and `site/`, `pip install -e ".[dev]"` in `python/`).

```bash
# Drift checks (also run by the pre-commit hook)
node scripts/sync-version.mjs --check
node scripts/sync-agent-runs-doc.mjs --check

# Go
(cd go && go test ./... -race -count=1)

# TypeScript
(cd ts && npm run typecheck && npm test && npm run build)

# Python (CI lint/type-check runs on 3.12; tests run on 3.10–3.13)
(cd python && ruff check . && ruff format --check . && mypy src && pytest -q)

# Site (syncs docs into Starlight, then builds)
(cd site && npm run build)
```

## After editing `docs/`

Canonical protocol docs live under `docs/`. Mirror them into the SDK trees and the site before verifying:

```bash
node scripts/sync-agent-runs-doc.mjs    # → go/docs, python/docs, ts/docs
node site/scripts/sync-shared.mjs         # → site/src/content/docs/docs/
```

Then re-run the checks above (at minimum the two `--check` scripts and any SDK/site tests you touched).

## Per-package reference

| Area | Directory | What CI runs |
| ---- | --------- | ------------ |
| Version pins | repo root | `node scripts/sync-version.mjs --check` |
| Protocol mirrors | repo root | `node scripts/sync-agent-runs-doc.mjs --check` |
| Go SDK | `go/` | `go test ./... -race -count=1` |
| TypeScript SDK | `ts/` | `npm run typecheck`, `npm test`, `npm run build` |
| Python SDK | `python/` | `ruff check .`, `ruff format --check .`, `mypy src`, `pytest -q` |
| Docs site | `site/` | `npm run build` (runs `sync-shared` via `prebuild`) |

## Optional: pre-commit hook

Enable once per clone to run drift checks and Python lint/format automatically on commit:

```bash
git config core.hooksPath githooks
```

The hook runs:

- `scripts/sync-version.mjs --check`
- `scripts/sync-agent-runs-doc.mjs --check`
- `ruff check .` and `ruff format --check .` in `python/` (via `uv run` when available)

To fix formatting locally before committing:

```bash
cd python && uv run ruff format .
```

See [`githooks/README.md`](githooks/README.md) for details.
