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
| `scripts/changelog.mjs --check` | Node + `git-cliff` on `PATH` |

If `git-cliff` is not installed, the changelog step is **skipped** with a notice; fix drift before pushing or install git-cliff so the hook can catch it early:

```bash
brew install git-cliff    # macOS — pin to match CI: git-cliff 2.10.x (see ci.yml)
cargo install git-cliff --version 2.10.1 --locked
```
