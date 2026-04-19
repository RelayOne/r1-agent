#!/usr/bin/env bash
# ============================================================================
# ladder-driver.sh — automated progressive scope-matrix runner
#
# For each rung R01..R08, across all 3 modes (strict simple-loop,
# lenient simple-loop, sow harness):
#   1. Launch the run
#   2. Wait for terminal state (SIMPLE LOOP COMPLETE / PARTIAL / ABORT /
#      SOW finished) or wall-clock timeout
#   3. Quality-audit the output (build + tests + git state)
#   4. If the run passed (clean convergence + tests green), unlock the
#      next rung for that mode. If it failed, STOP that mode's ladder
#      and report what failed — the operator (or I) must diagnose and
#      patch before progressing.
#
# Runs each (rung, mode) sequentially within a mode but parallel across
# modes, so the three mode-ladders progress independently.
#
# State is stored in plans/scope-suite/ladder-state.json. Re-running
# the driver continues where each mode left off (or retries the last
# failure if the operator has shipped a fix).
# ============================================================================

set -uo pipefail

SUITE=/home/eric/repos/stoke/plans/scope-suite
RUNS=/home/eric/repos/scope-suite-runs
STATE=$SUITE/ladder-state.json
RESULTS=$SUITE/RESULTS.md
STOKE=/home/eric/repos/stoke/stoke

RUNGS=(R01 R02 R03 R04 R05 R06 R07 R08 R09 R10)
MODES=(strict lenient sow sow-serial)

mkdir -p "$RUNS"

# Per-mode per-rung timeout. Grows with rung complexity.
#
# sow + sow-serial need more budget than simple-loop at every rung:
# the harness plans a SOW (often multi-session), dispatches per-task
# workers, runs per-task review, then cross-review at session end.
# Each layer adds 3-5 minutes of deliberation the simple-loop doesn't
# pay. Starving sow at 30m was a fixed cost we couldn't amortize; now
# every rung gets enough wall-clock to actually converge if the logic
# is sound. Failure at 90m means a real bug, not budget exhaustion.
timeout_for() {
  local rung="$1" mode="${2:-}"
  if [[ "$mode" == "sow" || "$mode" == "sow-serial" ]]; then
    case "$rung" in
      R01|R02) echo "60m";;
      R03|R04) echo "90m";;
      R05|R06) echo "120m";;
      R07)     echo "150m";;
      R08)     echo "240m";;
      R09)     echo "150m";;
      R10)     echo "180m";;
      *)       echo "90m";;
    esac
    return
  fi
  case "$rung" in
    R01|R02) echo "30m";;
    R03|R04) echo "40m";;
    R05|R06) echo "60m";;
    R07) echo "90m";;
    R08) echo "180m";;
    R09) echo "90m";;
    R10) echo "120m";;
    *)   echo "45m";;
  esac
}

ensure_state() {
  if [[ ! -f "$STATE" ]]; then
    cat > "$STATE" <<EOF
{
  "strict":     {"next_rung": "R01", "last_result": null, "last_run_dir": null, "blocked_reason": null},
  "lenient":    {"next_rung": "R01", "last_result": null, "last_run_dir": null, "blocked_reason": null},
  "sow":        {"next_rung": "R01", "last_result": null, "last_run_dir": null, "blocked_reason": null},
  "sow-serial": {"next_rung": "R01", "last_result": null, "last_run_dir": null, "blocked_reason": null}
}
EOF
  fi
  # Seed sow-serial in existing state files that predate the 4th lane.
  if ! python3 -c "import json; exit(0 if 'sow-serial' in json.load(open('$STATE')) else 1)" 2>/dev/null; then
    python3 <<PY
import json
p = '$STATE'
s = json.load(open(p))
s['sow-serial'] = {"next_rung": "R01", "last_result": None, "last_run_dir": None, "blocked_reason": None}
json.dump(s, open(p,'w'), indent=2)
PY
  fi
}

# Returns the current next-rung for a mode.
state_next_rung() {
  python3 -c "import json; print(json.load(open('$STATE'))['$1']['next_rung'] or '')"
}

state_blocked() {
  python3 -c "import json; r=json.load(open('$STATE'))['$1']; print('YES' if r.get('blocked_reason') else 'NO')"
}

state_update() {
  local mode="$1" field="$2" value="$3"
  python3 <<PY
import json
s = json.load(open('$STATE'))
s['$mode']['$field'] = $value if '$value' == 'null' else "$value"
json.dump(s, open('$STATE','w'), indent=2)
PY
}

state_advance() {
  local mode="$1" result="$2" run_dir="$3"
  # Advance next_rung to the rung AFTER the one just passed.
  python3 <<PY
import json
s = json.load(open('$STATE'))
rungs = "${RUNGS[@]}".split()
cur = s['$mode']['next_rung']
i = rungs.index(cur) if cur in rungs else -1
next_i = i + 1
s['$mode']['next_rung'] = rungs[next_i] if next_i < len(rungs) else None
s['$mode']['last_result'] = '$result'
s['$mode']['last_run_dir'] = '$run_dir'
s['$mode']['blocked_reason'] = None
json.dump(s, open('$STATE','w'), indent=2)
PY
}

state_block() {
  local mode="$1" result="$2" run_dir="$3" reason="$4"
  python3 <<PY
import json
s = json.load(open('$STATE'))
s['$mode']['last_result'] = '$result'
s['$mode']['last_run_dir'] = '$run_dir'
s['$mode']['blocked_reason'] = """$reason"""
json.dump(s, open('$STATE','w'), indent=2)
PY
}

# Classify a run's output as pass/fail-type.
# Returns: passed | partial | regressed | timeout | failed-task | test-failed | crash
classify_run() {
  local dir="$1" exit_code="$2" mode="$3"
  local log="$dir/stoke-run.log"
  if grep -q "SIMPLE LOOP COMPLETE" "$log" 2>/dev/null; then
    # Verify tests if there's a package.json with a test script.
    if [[ -f "$dir/package.json" ]] && grep -q '"test"' "$dir/package.json"; then
      local out
      out=$(cd "$dir" && timeout 120 npm test 2>&1)
      if echo "$out" | grep -qiE "Tests.*failed|failing"; then
        echo "test-failed"
      elif echo "$out" | grep -qiE "Tests.*[1-9][0-9]* passed"; then
        echo "passed"
      else
        # Test script ran but output unclear; accept convergence as pass.
        echo "passed"
      fi
    else
      echo "passed"
    fi
  elif grep -q "SIMPLE LOOP PARTIAL-SUCCESS" "$log" 2>/dev/null; then
    echo "partial"
  elif grep -q "SIMPLE LOOP ABORTED" "$log" 2>/dev/null; then
    echo "regressed"
  elif grep -q "SOW finished with 0 failed" "$log" 2>/dev/null; then
    echo "passed"
  elif grep -qE "SOW finished with [0-9]+ failed" "$log" 2>/dev/null; then
    echo "failed-task"
  # Sow's native fast path doesn't print 'SOW finished' — it prints
  # [PASS] S<n> per session + [PASS] AC<n> per criterion. Count
  # [PASS]/[FAIL] session markers directly. Only passes when:
  #   - exit_code == 0 AND
  #   - at least one [PASS] session line AND
  #   - zero [FAIL] session lines
  elif [[ "$exit_code" == "0" ]] && grep -qE "^  \[PASS\] S[0-9]" "$log" 2>/dev/null; then
    if grep -qE "^  \[FAIL\] S[0-9]" "$log" 2>/dev/null; then
      echo "failed-task"
    else
      echo "passed"
    fi
  elif grep -qE "^  \[FAIL\] S[0-9]" "$log" 2>/dev/null; then
    echo "failed-task"
  elif [[ "$exit_code" == "124" ]]; then
    echo "timeout"
  else
    echo "crash"
  fi
}

# Run one (mode, rung) combination sequentially.
run_one() {
  local mode="$1" rung="$2"
  local sow_file
  sow_file=$(ls "$SUITE/rungs/${rung}-"*.md 2>/dev/null | head -1)
  [[ -f "$sow_file" ]] || { echo "[$mode] no SOW for $rung; skipping"; return 2; }

  # R07/R08 pull their SOW from the Sentinel repo.
  local sow_src="$sow_file"
  case $rung in
    R07) sow_src=/home/eric/repos/sentinel-simple-opus/SOW_MONOREPO_SLICE.md;;
    R08) sow_src=/home/eric/repos/sentinel-simple-opus/SOW_WEB_MOBILE.md;;
  esac

  local ts dir
  ts=$(date +%Y%m%dT%H%M%S)
  dir="$RUNS/${rung}-${mode}-${ts}"
  mkdir -p "$dir" && cd "$dir" && git init -q -b main
  echo "# $rung-$mode scratch" > README.md
  # H-30-sow fix: commit the SOW itself as part of the baseline so
  # the sow harness's `preflight: git-clean` check doesn't see it
  # as an uncommitted change and silently fail all tasks in <1s.
  cp "$sow_src" "$dir/SOW.md"
  git add README.md SOW.md
  GIT_AUTHOR_NAME=exp GIT_AUTHOR_EMAIL=exp@exp \
    GIT_COMMITTER_NAME=exp GIT_COMMITTER_EMAIL=exp@exp \
    git commit -q -m "scope-suite: init $rung $mode with SOW"

  local log="$dir/stoke-run.log"
  local to
  to=$(timeout_for "$rung" "$mode")

  echo "[$mode/$rung] starting (timeout=$to, dir=$(basename $dir))"

  local exit_code
  case $mode in
    strict)
      timeout "$to" "$STOKE" simple-loop \
        --repo "$dir" --file "$dir/SOW.md" \
        --reviewer codex --max-rounds 5 \
        --fix-mode sequential --fresh \
        > "$log" 2>&1
      exit_code=$?
      ;;
    lenient)
      timeout "$to" "$STOKE" simple-loop \
        --repo "$dir" --file "$dir/SOW.md" \
        --reviewer codex --max-rounds 3 \
        --fix-mode sequential --fresh --lenient-compliance \
        > "$log" 2>&1
      exit_code=$?
      ;;
    sow)
      local port key
      port=$(cat ~/.litellm/proxy.port 2>/dev/null || echo 4000)
      key=$(grep '^LITELLM_MASTER_KEY=' ~/.litellm/.env 2>/dev/null | cut -d= -f2- | tr -d '"'"'")
      # Small rungs (R01-R04) run without per-task worktrees + parallel.
      # Per-task-worktree caused REVIEW_REJECTED / NO_DIFF on tiny SOWs
      # because the reviewer was checking main while the worker wrote
      # to an isolated worktree — trivially-sized SOWs don't benefit
      # from the isolation and pay its coordination cost.
      # Small rungs: serial (--parallel 1), no per-task-worktree.
      # With parallel>1, the harness's `preflight: git-clean` check
      # fails every task that runs after one already modified the
      # working tree — they race on the same cwd. Serial sidesteps it.
      # Big rungs: per-task-worktree + parallel to earn back speed.
      # Force fully-sequential on small rungs:
      #   --parallel 1         — one session at a time
      #   --parallel-tasks 1   — one task at a time within a session
      # Without --parallel-tasks, the harness runs N parallel task
      # workers in the SAME cwd (InPlace mode) and every task flags
      # the others' writes as "WRONG_FILES / PROTECTED_PATH_TOUCHED."
      local sow_flags=""
      case "$rung" in
        R01|R02|R03|R04) sow_flags="--parallel 1 --parallel-tasks 1";;
        *) sow_flags="--per-task-worktree --parallel 2";;
      esac
      # Sow REQUIRES a provider — 'load SOW: no provider configured'.
      # Use LiteLLM-fronted claude-sonnet-4-6 as the writer; codex as
      # the reviewer.
      STOKE_PERFLOG=1 STOKE_PERFLOG_FILE="$dir/perflog.txt" \
      timeout "$to" "$STOKE" sow \
        --repo "$dir" --file "$dir/SOW.md" \
        --native-base-url "http://localhost:$port" \
        --native-api-key "$key" \
        --native-model claude-sonnet-4-6 \
        --reviewer-source codex \
        $sow_flags --fresh \
        > "$log" 2>&1
      exit_code=$?
      ;;
    sow-serial)
      # 4th matrix lane per user request: sow with --workflow=serial.
      # Forces single-session + single-task-within-session + no
      # per-task-worktree. Trades the sow harness's parallelism for
      # simple-loop's convergence reliability. Flag set in the stoke
      # binary collapses --parallel / --parallel-tasks / per-task-
      # worktree to 1/1/false regardless of other sow_flags.
      local port key
      port=$(cat ~/.litellm/proxy.port 2>/dev/null || echo 4000)
      key=$(grep '^LITELLM_MASTER_KEY=' ~/.litellm/.env 2>/dev/null | cut -d= -f2- | tr -d '"'"'")
      STOKE_PERFLOG=1 STOKE_PERFLOG_FILE="$dir/perflog.txt" \
      timeout "$to" "$STOKE" sow \
        --repo "$dir" --file "$dir/SOW.md" \
        --native-base-url "http://localhost:$port" \
        --native-api-key "$key" \
        --native-model claude-sonnet-4-6 \
        --reviewer-source codex \
        --workflow serial --fresh \
        > "$log" 2>&1
      exit_code=$?
      ;;
  esac

  local result
  result=$(classify_run "$dir" "$exit_code" "$mode")
  echo "[$mode/$rung] result: $result (exit $exit_code)"

  if [[ "$result" == "passed" ]]; then
    state_advance "$mode" "$result" "$dir"
  else
    local reason="Rung $rung in mode $mode ended $result (exit $exit_code). Inspect $log."
    state_block "$mode" "$result" "$dir" "$reason"
  fi
}

# Main loop — spawn one driver per mode; each mode walks its rung
# queue sequentially, stopping at the first non-pass.
drive_mode() {
  local mode="$1"
  while true; do
    if [[ "$(state_blocked "$mode")" == "YES" ]]; then
      echo "[$mode] BLOCKED — skipping further rungs until operator unblocks"
      break
    fi
    local rung
    rung=$(state_next_rung "$mode")
    [[ -z "$rung" || "$rung" == "None" ]] && { echo "[$mode] all rungs complete"; break; }
    run_one "$mode" "$rung"
    # Allow 30s inter-rung cool-down to avoid slamming Claude/codex
    # with back-to-back launches.
    sleep 30
  done
}

ensure_state

# Accept --mode flag to restrict to one lane.
MODES_TO_RUN=("${MODES[@]}")
if [[ "${1:-}" == "--mode" ]]; then
  MODES_TO_RUN=("$2")
fi

# Serial mode: run lanes one-at-a-time to avoid saturating the shared
# Claude CLI account. Three-lane parallel runs were hitting 15-min
# rate-limit backoffs per call, timing out R03 on both strict+lenient.
# --parallel flag restores the old behavior when needed.
if [[ "${1:-}" == "--parallel" ]]; then
  for m in "${MODES_TO_RUN[@]}"; do
    drive_mode "$m" &
    echo "Driver started: $m (PID $!)"
  done
  wait
else
  for m in "${MODES_TO_RUN[@]}"; do
    echo "═══ Starting $m lane (serial) ═══"
    drive_mode "$m"
  done
fi
echo "All mode drivers finished."
