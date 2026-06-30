# Git hooks

This folder is wired as Git’s hook directory so hooks can live **in the repo**
instead of only under `.git/hooks` (which is not shared).

## One-time setup per clone

```bash
./scripts/setup-git-hooks.sh
```

Or manually:

```bash
git config core.hooksPath githooks
```

Hooks run only when you commit locally; they do not affect `git push` by themselves.

## Disable

```bash
git config --unset core.hooksPath
```

## What runs

`pre-commit`:

1. **Formats staged Python files** via [`scripts/format.sh`](../scripts/format.sh) and re-stages them.
2. Runs the same drift checks as CI:

| Check | Needs |
| ----- | ----- |
| `scripts/sync-version.mjs --check` | Node |
| `scripts/sync-agent-runs-doc.mjs --check` | Node |
| `ruff check .` in `python/` | `uv` or `pip install -e ".[dev]"` |

Format the whole Python tree manually:

```bash
./scripts/format.sh
```

`CHANGELOG.md` files are regenerated only during the **Publish** release workflow — not checked on every commit.
