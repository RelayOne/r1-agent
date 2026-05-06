#!/bin/bash
# Apply TASK-30 from specs/cortex-core.md — adds 4 lines to CLAUDE.md's
# package map for the cortex/lobes/antitrunc/lanes packages that shipped
# in specs 1, 2, 3, and 9 (BUILD_ORDER 1/2/3/9).
#
# This was BLOCKED from inside Claude Code because .claude/hooks/protect-hooks.sh
# guards CLAUDE.md as human-controlled. Run this from a terminal (not via
# `! bash …` inside a Claude session — that hits the same Bash guard).
#
# Idempotent: re-running is a no-op once the lines are present.

set -euo pipefail

cd "$(dirname "$0")/.."

CLAUDE_MD="$(pwd)/CLAUDE.md"

if [ ! -f "$CLAUDE_MD" ]; then
  echo "ERROR: $CLAUDE_MD not found. Run from the r1-agent repo." >&2
  exit 1
fi

# Idempotency check — bail if cortex/ line is already present.
if grep -qE '^cortex/[[:space:]]+Parallel-cognition substrate' "$CLAUDE_MD"; then
  echo "OK: cortex/ package map line already present in $CLAUDE_MD — nothing to do."
  exit 0
fi

# Verify the anchor line exists before patching.
if ! grep -qE '^concern/templates/' "$CLAUDE_MD"; then
  echo "ERROR: anchor line 'concern/templates/' not found in $CLAUDE_MD." >&2
  echo "       The package map structure may have changed; review specs/cortex-core.md TASK-30." >&2
  exit 2
fi

# Insert the 4 cortex lines immediately after concern/templates/.
# Use awk for portability; sed -i syntax differs between BSD + GNU.
TMPFILE=$(mktemp)
trap 'rm -f "$TMPFILE"' EXIT

awk '
  /^concern\/templates\// {
    print
    print "cortex/                            Parallel-cognition substrate (Workspace, Lobe, Round, Spotlight, Router) — GWT-style shared workspace; spec 1"
    print "cortex/lobes/                      6 v1 Lobes — memoryrecall, walkeeper, rulecheck, planupdate, clarifyq, memorycurator (spec 2)"
    print "cortex/lobes/antitrunc/            Anti-truncation Lobe — critical Notes on truncation phrases / scope underdelivery (spec 9)"
    print "cortex/lanes/                      Lane lifecycle + 6 event types (created/status/delta/cost/note/killed) (spec 3)"
    next
  }
  { print }
' "$CLAUDE_MD" > "$TMPFILE"

# Sanity: the temp file should be exactly 4 lines longer than the original.
ORIG_LINES=$(wc -l < "$CLAUDE_MD")
NEW_LINES=$(wc -l < "$TMPFILE")
if [ "$((NEW_LINES - ORIG_LINES))" -ne 4 ]; then
  echo "ERROR: expected +4 lines, got $((NEW_LINES - ORIG_LINES)). Aborting without writing." >&2
  exit 3
fi

mv "$TMPFILE" "$CLAUDE_MD"
trap - EXIT

echo "OK: 4 cortex lines inserted into $CLAUDE_MD (TASK-30 unblock)."
echo
echo "Next steps:"
echo "  git diff CLAUDE.md           # review the change"
echo "  git add CLAUDE.md && git commit -m 'docs(claude): add cortex/lobes/lanes to package map (TASK-30)'"
echo "  gh pr create --base dev --title 'docs(claude): TASK-30 — cortex package map line'"
