#!/usr/bin/env bash
# Format sources in the monorepo. Run from the repo root:
#
#   ./scripts/format.sh
#   ./scripts/format.sh python/src/mantyx/client.py
#
# When given paths, they may be repo-root paths (python/...) or paths relative to
# python/. With no arguments, formats the entire Python tree.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

run_ruff_format() {
  (
    cd "$ROOT/python"
    if command -v uv >/dev/null 2>&1; then
      uv run ruff format "$@"
    elif python3 -m ruff --version >/dev/null 2>&1; then
      python3 -m ruff format "$@"
    else
      echo "format: ruff not found — install dev deps: cd python && pip install -e '.[dev]'" >&2
      exit 1
    fi
  )
}

if [[ $# -eq 0 ]]; then
  echo "format: ruff format (python/)"
  run_ruff_format .
  exit 0
fi

targets=()
for path in "$@"; do
  if [[ "$path" == python/* ]]; then
    targets+=("${path#python/}")
  else
    targets+=("$path")
  fi
done

echo "format: ruff format ${targets[*]}"
run_ruff_format "${targets[@]}"
