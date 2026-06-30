#!/usr/bin/env bash
# Enable repo git hooks (pre-commit formatting + drift checks). Run once per clone:
#
#   ./scripts/setup-git-hooks.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

chmod +x githooks/pre-commit
git config core.hooksPath githooks

echo "Git hooks enabled: core.hooksPath=githooks"
echo "pre-commit will format staged Python files and run CI drift checks."
