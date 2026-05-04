#!/usr/bin/env bash
# snapshot_drift_check.sh — CI guard against accidental snapshot updates
# per specs/agentic-test-harness.md §10a + §12 item 22:
#
#   > Add a CI check that fails when the golden snapshot diff is
#   > non-empty AND the source diff in web/src/, internal/tui/,
#   > desktop/src-tauri/ is empty (catches accidental auto-updates).
#
# Behavior:
#   - Run inside a git checkout where HEAD is the PR branch and
#     ${BASE_REF:-origin/main} is the merge base.
#   - Compute the diff against ${BASE_REF}.
#   - SNAP_DIFF: changes under tests/agent/**/golden/ (golden a11y
#     snapshots).
#   - SRC_DIFF:  changes under web/src/, internal/tui/,
#     desktop/src-tauri/ (the surfaces that legitimately drive
#     snapshot churn).
#   - PASS when:    SNAP_DIFF empty, OR (SNAP_DIFF non-empty AND
#                   SRC_DIFF non-empty).
#   - FAIL when:    SNAP_DIFF non-empty AND SRC_DIFF empty
#                   (accidental auto-update).
#
# Exit codes:
#   0 PASS
#   1 FAIL (drift detected)
#   2 setup error (missing git, can't resolve BASE_REF)

set -euo pipefail

BASE_REF="${BASE_REF:-origin/main}"

if ! command -v git >/dev/null 2>&1; then
    echo "snapshot_drift_check: git not on PATH" >&2
    exit 2
fi

# Resolve the base ref. If origin/main isn't fetched, fall back to
# main, then to HEAD~1 so the script is at least usable in a fresh
# clone for local debugging.
for candidate in "$BASE_REF" main HEAD~1; do
    if git rev-parse --verify --quiet "$candidate" >/dev/null; then
        BASE_REF="$candidate"
        break
    fi
done

# Skip when the merge base equals HEAD (no diff to inspect; e.g. CI
# job invoked outside a PR context).
if [ "$(git rev-parse "$BASE_REF")" = "$(git rev-parse HEAD)" ]; then
    echo "snapshot_drift_check: base equals HEAD; nothing to check"
    exit 0
fi

SNAP_DIFF=$(git diff --name-only "$BASE_REF...HEAD" -- 'tests/agent/*/golden/*' || true)
SRC_DIFF=$(git diff --name-only "$BASE_REF...HEAD" -- 'web/src/*' 'internal/tui/*' 'desktop/src-tauri/*' || true)

if [ -z "$SNAP_DIFF" ]; then
    echo "snapshot_drift_check: no golden snapshot changes; PASS"
    exit 0
fi

if [ -n "$SRC_DIFF" ]; then
    echo "snapshot_drift_check: snapshots changed AND source changed; PASS"
    echo "  snapshot files: $SNAP_DIFF"
    echo "  source files:   $SRC_DIFF"
    exit 0
fi

echo "snapshot_drift_check: FAIL — golden snapshots changed without"
echo "any source change in web/src/, internal/tui/, or desktop/src-tauri/."
echo ""
echo "Either:"
echo "  1. Roll back the snapshot changes — they are most likely an"
echo "     accidental commit of \`make agent-features-update\` output."
echo "  2. OR include the legitimate UI source change that justifies"
echo "     the snapshot diff in the same PR so reviewers can compare."
echo ""
echo "Snapshot files that changed:"
for f in $SNAP_DIFF; do
    echo "  $f"
done
exit 1
