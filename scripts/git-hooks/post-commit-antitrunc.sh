#!/usr/bin/env bash
# post-commit-antitrunc.sh — scans the most recent commit body for
# false-completion phrases and writes a warning to audit/antitrunc/
# when any are found.
#
# Behaviour:
#   - Reads `git log -1 --format=%B HEAD` (commit subject + body).
#   - Greps for the FalseCompletionPhrases regex catalog.
#   - On hit: writes audit/antitrunc/post-commit-<sha>.md with the
#     match details, then exits 0 (non-blocking but observable).
#   - On clean: exits 0 silently.
#
# The hook is wired by `scripts/install-hooks.sh` (item 16) which
# sets `git config core.hooksPath scripts/git-hooks/`.
#
# This is Layer 6 of the anti-truncation defense. The warning file
# is then surfaced to the next agentloop turn via the rulecheck
# Lobe (cortex-dependent — see item 16 + cortex spec) or via the
# `r1 antitrunc verify` CLI (item 17).

set -euo pipefail

# Resolve repo root from the hook's location. core.hooksPath sets
# CWD to the repo root when the hook fires, so `git rev-parse` is
# safe.
REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
SHA="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
BODY="$(git log -1 --format=%B HEAD 2>/dev/null || echo '')"

if [ -z "$BODY" ]; then
  exit 0
fi

# Mirror of internal/antitrunc/phrases.go FalseCompletionPhrases.
# Kept in sync manually; the regex catalog tests in phrases_test.go
# exercise these patterns. Update both when adding new phrases.
declare -a PHRASES=(
  "spec [0-9]+ (done|complete|ready)"
  "all (tasks?|specs?|items?) (done|complete|finished)"
)

HITS=()
for phrase in "${PHRASES[@]}"; do
  while IFS= read -r line; do
    [ -n "$line" ] && HITS+=("$phrase :: $line")
  done < <(printf '%s\n' "$BODY" | grep -iE "$phrase" || true)
done

if [ ${#HITS[@]} -eq 0 ]; then
  exit 0
fi

OUT_DIR="$REPO_ROOT/audit/antitrunc"
mkdir -p "$OUT_DIR"
OUT="$OUT_DIR/post-commit-$SHA.md"

{
  echo "# anti-truncation post-commit warning"
  echo
  echo "- commit: \`$SHA\`"
  echo "- timestamp: $(date -u +%FT%TZ)"
  echo
  echo "## False-completion phrases matched"
  echo
  for h in "${HITS[@]}"; do
    echo "- $h"
  done
  echo
  echo "## Commit body"
  echo
  echo '```'
  echo "$BODY"
  echo '```'
  echo
  echo "## Operator note"
  echo
  echo "This warning fires when a commit body claims completion in a shape"
  echo "the layered defense has historically associated with self-truncation"
  echo "(\"spec N done\", \"all tasks complete\", etc). The hook is non-blocking."
  echo "Review the commit and confirm the claim corresponds to actual"
  echo "checked items in the plan / spec. Run \`r1 antitrunc verify\` for"
  echo "automated cross-reference."
} > "$OUT"

# Best-effort observable signal on stderr so the operator running git
# manually sees the warning even if they don't read audit/. Stays
# non-blocking (exit 0) so commits never fail.
echo "[antitrunc] post-commit warning: $OUT" >&2
exit 0
