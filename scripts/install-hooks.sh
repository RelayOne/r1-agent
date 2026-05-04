#!/usr/bin/env bash
# install-hooks.sh — wires scripts/git-hooks/ as the active git hooks
# directory for this repo.
#
# Usage:
#   bash scripts/install-hooks.sh         # install
#   bash scripts/install-hooks.sh --check # report current state, no change
#   bash scripts/install-hooks.sh --uninstall # restore default .git/hooks
#
# What it does:
#   - Sets `git config core.hooksPath scripts/git-hooks/` so git uses
#     the in-repo hook directory (instead of .git/hooks/).
#   - Verifies every hook script is executable; chmods if not.
#
# Why in-repo hooks (instead of cp .git/hooks/...):
#   - Hooks live in the worktree where contributors can review them.
#   - Hooks are version-controlled — anyone reverting the repo to an
#     older commit also reverts the hooks (correctness).
#   - The post-commit-antitrunc.sh hook is part of the layered
#     anti-truncation defense and must be present on every dev
#     machine; opt-in install via this script makes that explicit.

set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
HOOKS_DIR_RELATIVE="scripts/git-hooks"
HOOKS_DIR_ABSOLUTE="$REPO_ROOT/$HOOKS_DIR_RELATIVE"

cmd="${1:---install}"

case "$cmd" in
  --install|install|"")
    if [ ! -d "$HOOKS_DIR_ABSOLUTE" ]; then
      echo "error: $HOOKS_DIR_RELATIVE not found in repo root" >&2
      exit 1
    fi
    # Make every script executable (idempotent).
    find "$HOOKS_DIR_ABSOLUTE" -maxdepth 1 -type f -name '*.sh' -exec chmod +x {} +
    git -C "$REPO_ROOT" config core.hooksPath "$HOOKS_DIR_RELATIVE"
    echo "installed: core.hooksPath = $HOOKS_DIR_RELATIVE"
    echo "hooks present:"
    find "$HOOKS_DIR_ABSOLUTE" -maxdepth 1 -type f -name '*.sh' -printf '  - %f\n'
    echo
    echo "the post-commit-antitrunc hook will now scan commit bodies"
    echo "for false-completion phrases. warnings are written to"
    echo "audit/antitrunc/ — non-blocking."
    ;;
  --check|check)
    cur=$(git -C "$REPO_ROOT" config --get core.hooksPath || echo "<unset>")
    echo "core.hooksPath = $cur"
    if [ "$cur" = "$HOOKS_DIR_RELATIVE" ]; then
      echo "status: anti-truncation hooks ACTIVE"
      exit 0
    fi
    echo "status: anti-truncation hooks NOT active"
    echo "run: bash scripts/install-hooks.sh"
    exit 1
    ;;
  --uninstall|uninstall)
    git -C "$REPO_ROOT" config --unset core.hooksPath || true
    echo "uninstalled: core.hooksPath unset (git falls back to .git/hooks/)"
    ;;
  -h|--help|help)
    sed -n '1,25p' "$0"
    ;;
  *)
    echo "unknown command: $cmd" >&2
    echo "try: $0 --help" >&2
    exit 2
    ;;
esac
