#!/usr/bin/env bash
# tools/lint-no-chdir.sh — r1d-server Phase A item 2.
#
# Runs the chdir-lint AST walker (tools/cmd/chdir-lint) over the production
# package set (./internal/... and ./cmd/...). Exits non-zero if any
# unannotated call to os.Chdir, os.Getwd, filepath.Abs("") or
# os.Open("./...") is found.
#
# The lint enforces spec §10 (D-D4 audit gate, see specs/r1d-server.md).
# Every legitimate hit must carry a `// LINT-ALLOW chdir-<bucket>: <reason>`
# annotation on the line directly above; everything else must be refactored
# to thread an explicit repoRoot string through the call chain.
#
# Usage:
#   tools/lint-no-chdir.sh             # default: ./internal/... ./cmd/...
#   tools/lint-no-chdir.sh ./pkg/...   # override target patterns

set -euo pipefail

# Resolve repo root regardless of caller cwd (the lint must be runnable
# from anywhere inside the worktree, not just the top-level Makefile).
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" &> /dev/null && pwd)"
REPO_ROOT="$(cd -- "${SCRIPT_DIR}/.." &> /dev/null && pwd)"

cd "${REPO_ROOT}"

PATTERNS=("$@")
if [ ${#PATTERNS[@]} -eq 0 ]; then
  PATTERNS=("./internal/..." "./cmd/...")
fi

# `go run` keeps the binary out of the worktree (avoids the stray-binary
# noise that bit us in TASK-1). The chdir-lint tool itself is small enough
# that recompilation cost is negligible.
exec go run ./tools/cmd/chdir-lint "${PATTERNS[@]}"
