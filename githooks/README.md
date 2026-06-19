# Git hooks

This folder is wired as Git’s hook directory so hooks can live **in the repo**
instead of only under `.git/hooks` (which is not shared).

## One-time setup per clone

```bash
git config core.hooksPath githooks
```

Hooks run only when you commit locally; they do not affect `git push` by themselves.

## Disable

```bash
git config --unset core.hooksPath
```

## What runs

`pre-commit` runs the same checks as CI where possible:

| Check | Needs |
| ----- | ----- |
| `scripts/sync-version.mjs --check` | Node |
| `scripts/sync-agent-runs-doc.mjs --check` | Node |
| `ruff check .` + `ruff format --check .` in `python/` | `uv` or `pip install -e ".[dev]"` |

Fix formatting before committing:

```bash
cd python && uv run ruff format .
```

`CHANGELOG.md` files are regenerated only during the **Publish** release workflow — not checked on every commit.
