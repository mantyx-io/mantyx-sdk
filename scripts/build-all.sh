#!/usr/bin/env bash
# Build every publishable artifact in the monorepo (Go compile, TS dist, Python
# wheel/sdist, Astro site). Run from the repo root:
#
#   ./scripts/build-all.sh
#
# Prerequisites: dependencies installed in each package (see agent.md).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

echo "build-all: syncing protocol doc mirrors"
node scripts/sync-agent-runs-doc.mjs

echo "build-all: Go (compile all packages)"
(
  cd go
  go build ./...
)

echo "build-all: TypeScript (@mantyx/sdk dist)"
(
  cd ts
  if [[ ! -d node_modules ]]; then
    echo "build-all: ts/node_modules missing — run: (cd ts && npm install)" >&2
    exit 1
  fi
  npm run build
)

echo "build-all: Python (wheel + sdist)"
(
  cd python
  if command -v uv >/dev/null 2>&1; then
    uv build
  elif python3 -m build --version >/dev/null 2>&1; then
    python3 -m build
  else
    echo "build-all: need 'uv' or 'python3 -m build' (pip install build)" >&2
    exit 1
  fi
)

echo "build-all: docs site (Astro)"
(
  cd site
  if [[ ! -d node_modules ]]; then
    echo "build-all: site/node_modules missing — run: (cd site && npm ci)" >&2
    exit 1
  fi
  npm run build
)

echo "build-all: done"
