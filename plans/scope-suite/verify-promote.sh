#!/usr/bin/env bash
# verify-promote.sh — manual verification + state promotion helper.
#
# Usage: ./verify-promote.sh <mode> <rung> <workdir>
#
# Reads the actual source files produced by a terminal run, runs
# `npm test` (or rung-appropriate equivalent), and only promotes the
# ladder-state.json entry when:
#   - exit code == 0 (or explicit PASS signal)
#   - at least one real test passed (or SOW has no testable assertion)
#   - no obvious stub/placeholder content
#
# This is the checklist I run BEFORE advancing a lane so I never
# claim convergence without inspecting the artifact.

set -uo pipefail

mode="${1:-}"
rung="${2:-}"
dir="${3:-}"

[[ -z "$mode" || -z "$rung" || -z "$dir" ]] && {
  echo "usage: $0 <mode> <rung> <workdir>"
  exit 2
}
[[ -d "$dir" ]] || { echo "❌ workdir not found: $dir"; exit 2; }

echo "═══════════════════════════════════════════════════════════"
echo "  VERIFY before promote — $mode / $rung"
echo "  dir: $dir"
echo "═══════════════════════════════════════════════════════════"

echo
echo "=== 1. git log ==="
git -C "$dir" log --oneline | head -15

echo
echo "=== 2. terminal verdict ==="
grep -E "SOW finished|SIMPLE LOOP COMPLETE|SIMPLE LOOP PARTIAL|SIMPLE LOOP ABORTED|\[PASS\] S[0-9]|\[FAIL\] S[0-9]" "$dir/stoke-run.log" 2>/dev/null | tail -8

echo
echo "=== 3. real files (excl node_modules, .stoke) ==="
git -C "$dir" ls-files | grep -v node_modules | grep -v '^\.stoke/' | head -20

echo
echo "=== 4. key file contents ==="
for f in package.json src/greet.ts src/slugify.ts src/index.ts tsconfig.json pnpm-workspace.yaml turbo.json; do
  if [[ -f "$dir/$f" ]]; then
    echo "--- $f ---"
    head -30 "$dir/$f"
    echo
  fi
done

echo "=== 5. run tests ==="
if [[ -f "$dir/package.json" ]] && grep -q '"test"' "$dir/package.json"; then
  (cd "$dir" && timeout 120 npm test 2>&1) | tail -15
  test_exit=$?
  echo "test exit: $test_exit"
else
  echo "(no test script in package.json — skipping)"
  test_exit=0
fi

echo
echo "=== 6. tsc --noEmit (if applicable) ==="
if [[ -f "$dir/tsconfig.json" ]]; then
  (cd "$dir" && timeout 60 npx --no tsc --noEmit 2>&1) | tail -8
fi

echo
echo "═══════════════════════════════════════════════════════════"
echo "  MANUAL DECISION:"
echo "  If all above look clean, run:"
echo "    python3 -c \"import json; p='/home/eric/repos/stoke/plans/scope-suite/ladder-state.json'; s=json.load(open(p)); s['$mode']['last_result']='passed'; s['$mode']['next_rung']='<NEXT>'; s['$mode']['blocked_reason']=None; s['$mode']['last_run_dir']='$dir'; json.dump(s,open(p,'w'),indent=2)\""
echo "═══════════════════════════════════════════════════════════"
