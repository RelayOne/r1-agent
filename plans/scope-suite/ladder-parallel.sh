#!/usr/bin/env bash
# ============================================================================
# ladder-parallel.sh — launch R05…R10 in parallel on a single lane
#
# The serial ladder-driver.sh gates each rung behind the previous one's
# pass. That made sense while rungs were proving each harness feature.
# Once we've verified simple-loop reaches R10, the async harness can
# progress faster by attacking R05…R10 simultaneously: every rung is
# independent (distinct starting files, distinct SOW) so they don't
# block each other, and parallelism lets us collect ≥6 datapoints in
# the time one sequential climb would take.
#
# Usage:
#   ladder-parallel.sh --mode sow-serial [--rungs R05 R06 R07 R08 R09 R10]
#
# For each rung in the list, spawns one `stoke sow` process against a
# fresh run dir. Logs go to <run-dir>/stoke-run.log. Exit status of
# each child is recorded in plans/scope-suite/parallel-results.jsonl.
#
# Designed to be safe against DoS of the litellm endpoint — pnpm
# install from H-65 is one-shot, and the per-session turn cap means
# each stoke process averages ~3-5 concurrent LLM calls. 6 rungs × 4
# calls = ~24 concurrent requests at peak. Adjust --rungs if your
# endpoint is smaller.
# ============================================================================
set -uo pipefail

SUITE=/home/eric/repos/stoke/plans/scope-suite
RUNS=/home/eric/repos/scope-suite-runs
STOKE=/home/eric/repos/stoke/stoke
RESULTS=$SUITE/parallel-results.jsonl

MODE="sow-serial"
RUNGS=(R05 R06 R07 R08 R09 R10)
TIMEOUT="120m"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --mode) MODE="$2"; shift 2;;
    --rungs) shift; RUNGS=(); while [[ $# -gt 0 && "$1" != --* ]]; do RUNGS+=("$1"); shift; done;;
    --timeout) TIMEOUT="$2"; shift 2;;
    *) echo "unknown arg: $1"; exit 2;;
  esac
done

mkdir -p "$RUNS"

# Discover litellm endpoint the same way ladder-driver.sh does.
PORT=$(cat "$HOME/.litellm/proxy.port" 2>/dev/null || echo 4000)
API_KEY=$(grep '^LITELLM_MASTER_KEY=' "$HOME/.litellm/.env" 2>/dev/null | cut -d= -f2- | tr -d '"'"'")
BASE_URL="http://localhost:${PORT}"
if [[ -z "${API_KEY:-}" ]]; then
  echo "ERROR: no LITELLM_MASTER_KEY in \$HOME/.litellm/.env" >&2
  exit 2
fi

launch_rung() {
  local rung="$1"
  local ts=$(date +%Y%m%dT%H%M%S)
  local dir="$RUNS/${rung}-${MODE}-${ts}"
  local src="$SUITE/rungs/${rung}"*.md
  src=$(ls $src 2>/dev/null | head -1)
  if [[ -z "$src" ]]; then
    echo "[parallel/$rung] ERROR: no rung spec found in $SUITE/rungs/"
    return 1
  fi

  mkdir -p "$dir"
  cp "$src" "$dir/SOW.md"
  ( cd "$dir" && git init -q 2>/dev/null && git add -A 2>/dev/null && git -c user.email=p@p -c user.name=p commit -q -m "initial" 2>/dev/null ) || true
  # Seed a minimal package.json so H-65 preflight has somewhere to
  # add devDependencies. The rung spec will direct real scaffolding.
  if [[ ! -f "$dir/package.json" ]]; then
    cat > "$dir/package.json" <<JSON
{
  "name": "rung-${rung,,}-workspace",
  "version": "1.0.0",
  "private": true
}
JSON
  fi

  local sow_flags=""
  case "$MODE" in
    sow)        sow_flags="--per-task-worktree --parallel 2";;
    sow-serial) sow_flags="--workflow serial";;
    *) echo "unsupported mode: $MODE"; return 2;;
  esac

  local log="$dir/stoke-run.log"
  echo "[parallel/$rung] starting (mode=$MODE, timeout=$TIMEOUT, dir=${dir##*/})"
  (
    STOKE_PERFLOG=1 STOKE_PERFLOG_FILE="$dir/perflog.txt" \
      timeout "$TIMEOUT" "$STOKE" sow \
      --repo "$dir" --file "$dir/SOW.md" \
      --native-base-url "$BASE_URL" --native-api-key "$API_KEY" \
      --native-model claude-sonnet-4-6 \
      --reviewer-source codex $sow_flags --fresh \
      > "$log" 2>&1
    local rc=$?
    local status
    case $rc in
      0) status=passed;;
      1) status=failed-task;;
      124) status=timeout;;
      137) status=crash;;
      *) status="exit-$rc";;
    esac
    jq -nc --arg rung "$rung" --arg mode "$MODE" --arg dir "$dir" --arg status "$status" --arg ts "$ts" \
      '{ts:$ts, rung:$rung, mode:$mode, status:$status, dir:$dir}' \
      >> "$RESULTS" 2>/dev/null || \
      echo "{\"ts\":\"$ts\",\"rung\":\"$rung\",\"mode\":\"$MODE\",\"status\":\"$status\",\"dir\":\"$dir\"}" >> "$RESULTS"
    echo "[parallel/$rung] result: $status (exit $rc)"
  ) &
}

echo "=== parallel-ladder: $MODE × {${RUNGS[*]}} ==="
for rung in "${RUNGS[@]}"; do
  launch_rung "$rung"
  sleep 5  # stagger spawns so litellm isn't slammed with simultaneous prose→SOW conversions
done

echo "[parallel] spawned ${#RUNGS[@]} runs; waiting for all..."
wait
echo "[parallel] all finished. Tail of results:"
tail -n ${#RUNGS[@]} "$RESULTS" 2>/dev/null || true
