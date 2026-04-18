#!/usr/bin/env bash
# ============================================================================
# scope-suite/run.sh — runs stoke against one or all ladder rungs
#
# Usage:
#   ./run.sh R01                          # one rung, default config
#   ./run.sh all                           # sequential run of all rungs
#   ./run.sh R03 --mode sow               # force sow-harness mode (default simple-loop)
#   ./run.sh R03 --timeout 2h              # per-rung wall-clock cap
#
# Writes results to plans/scope-suite/results.jsonl (append-only).
# ============================================================================

set -uo pipefail

STOKE=/home/eric/repos/stoke/stoke
SUITE_DIR=/home/eric/repos/stoke/plans/scope-suite
RUNGS_DIR=$SUITE_DIR/rungs
RESULTS=$SUITE_DIR/results.jsonl
WORK_ROOT=/home/eric/repos/scope-suite-runs

[[ -x "$STOKE" ]] || { echo "❌ stoke binary missing at $STOKE"; exit 1; }

mkdir -p "$WORK_ROOT"

# Defaults.
MODE="simple-loop"
TIMEOUT="1h"
REVIEWER="codex"
EXTRA_ARGS=()

# First positional arg = rung name (or 'all').
RUNG="${1:-}"
if [[ -z "$RUNG" ]]; then
  echo "usage: $0 <rung|all> [--mode simple-loop|sow] [--timeout 1h] [--reviewer codex|cc-sonnet|cc-opus]"
  echo "available rungs: $(ls $RUNGS_DIR/ | sed 's/.md//' | tr '\n' ' ')"
  exit 2
fi
shift || true

# Parse remaining flags.
while [[ $# -gt 0 ]]; do
  case $1 in
    --mode) MODE="$2"; shift 2;;
    --timeout) TIMEOUT="$2"; shift 2;;
    --reviewer) REVIEWER="$2"; shift 2;;
    *) EXTRA_ARGS+=("$1"); shift;;
  esac
done

run_one_rung() {
  local rung="$1"
  # Accept either full filename stem (R01-hello-world) or short tag (R01).
  local sow_file="$RUNGS_DIR/$rung.md"
  if [[ ! -f "$sow_file" ]]; then
    sow_file=$(ls "$RUNGS_DIR"/${rung}-*.md 2>/dev/null | head -1)
  fi
  [[ -f "$sow_file" ]] || { echo "❌ no rung file matching '$rung' in $RUNGS_DIR"; return 2; }
  # Normalize rung short-tag for result logging.
  rung="$(basename "$sow_file" .md | cut -d- -f1)"

  local dir="$WORK_ROOT/$rung-$(date +%Y%m%dT%H%M%S)"
  echo ""
  echo "════════════════════════════════════════════════════════════════"
  echo "  RUNG $rung"
  echo "  SOW: $sow_file ($(wc -c < "$sow_file")B)"
  echo "  mode: $MODE"
  echo "  timeout: $TIMEOUT"
  echo "  workdir: $dir"
  echo "════════════════════════════════════════════════════════════════"

  mkdir -p "$dir"
  (cd "$dir" && git init -q -b main && \
    echo "# $rung scratch" > README.md && \
    git add README.md && \
    GIT_AUTHOR_NAME=exp GIT_AUTHOR_EMAIL=exp@exp \
    GIT_COMMITTER_NAME=exp GIT_COMMITTER_EMAIL=exp@exp \
      git commit -q -m "scope-suite: init $rung baseline")

  # R07 and R08 pull their SOW from the Sentinel repo, not from the
  # ladder rung file (which is just a pointer). Handle that.
  case $rung in
    R07) cp /home/eric/repos/sentinel-simple-opus/SOW_MONOREPO_SLICE.md "$dir/SOW.md";;
    R08) cp /home/eric/repos/sentinel-simple-opus/SOW_WEB_MOBILE.md "$dir/SOW.md";;
    *)   cp "$sow_file" "$dir/SOW.md";;
  esac

  local log="$dir/stoke-run.log"
  local start_s end_s duration
  start_s=$(date +%s)

  local extra_flags=()
  case $MODE in
    simple-loop)
      timeout "$TIMEOUT" "$STOKE" simple-loop \
        --repo "$dir" --file "$dir/SOW.md" \
        --reviewer "$REVIEWER" \
        --max-rounds 5 --fix-mode sequential --fresh \
        > "$log" 2>&1
      ;;
    sow)
      LITELLM_PORT=$(cat ~/.litellm/proxy.port 2>/dev/null || echo 4000)
      LITELLM_KEY=$(grep '^LITELLM_MASTER_KEY=' ~/.litellm/.env 2>/dev/null | cut -d= -f2- | tr -d '"'"'")
      timeout "$TIMEOUT" "$STOKE" sow \
        --repo "$dir" --file "$dir/SOW.md" \
        --native-base-url "http://localhost:$LITELLM_PORT" \
        --native-api-key "$LITELLM_KEY" \
        --native-model claude-sonnet-4-6 \
        --reviewer-source "$REVIEWER" \
        --per-task-worktree --parallel 2 --fresh \
        > "$log" 2>&1
      ;;
    *)
      echo "❌ unknown mode: $MODE"; return 2;;
  esac
  local exit_code=$?
  end_s=$(date +%s)
  duration=$(( end_s - start_s ))

  local result="crash"
  case $exit_code in
    0)   result="converged";;
    3)   result="regressed";;    # H-6 regression cap (simple-loop)
    4)   result="partial";;      # H-29 plateau (simple-loop)
    124) result="timeout";;      # GNU timeout killed the process
    *)   result="exit_$exit_code";;
  esac

  local commits
  commits=$(git -C "$dir" rev-list --count HEAD 2>/dev/null)
  local gates h27 h28
  gates=$(grep -cE "\[gate-hit\]" "$log" 2>/dev/null)
  h27=$(grep -cE "declared-symbol-not-implemented[^-]" "$log" 2>/dev/null)
  h28=$(grep -cE "declared-symbol-not-implemented-ts" "$log" 2>/dev/null)

  local binary_commit
  binary_commit=$(git -C /home/eric/repos/stoke rev-parse --short HEAD 2>/dev/null)

  local ts
  ts=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

  # Append JSONL result line.
  printf '{"ts":"%s","rung":"%s","binary":"%s","mode":"%s","reviewer":"%s","duration_s":%d,"commits":%d,"gates":%d,"h27":%d,"h28":%d,"result":"%s","exit_code":%d,"workdir":"%s"}\n' \
    "$ts" "$rung" "$binary_commit" "$MODE" "$REVIEWER" \
    "$duration" "$commits" "$gates" "$h27" "$h28" \
    "$result" "$exit_code" "$dir" \
    >> "$RESULTS"

  echo ""
  echo "--- $rung result ---"
  echo "  result:    $result (exit $exit_code)"
  echo "  duration:  ${duration}s"
  echo "  commits:   $commits"
  echo "  gates:     $gates  (h27=$h27, h28=$h28)"
  echo "  workdir:   $dir"
  echo "  logged to: $RESULTS"

  return $exit_code
}

if [[ "$RUNG" == "all" ]]; then
  for r in $(ls "$RUNGS_DIR" | sed 's/.md//' | sort); do
    run_one_rung "$r"
  done
else
  run_one_rung "$RUNG"
fi
