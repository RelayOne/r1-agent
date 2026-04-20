#!/usr/bin/env bash
# ============================================================================
# ladder-supervisor.sh — auto-unblock + retry loop for ladder lanes
#
# Polls ladder-state.json every N seconds. For each lane currently
# BLOCKED (blocked_reason != null and last_result != "passed"):
#
#   1. If the stoke binary's git SHA has advanced since the failed run,
#      the operator has shipped a fix. Unblock and relaunch via
#      ladder-driver.sh on that mode — the H-65 etc. fixes should
#      land on the next try.
#
#   2. Else if retry count for this rung is under a cap (default 2),
#      unblock and relaunch anyway. Transient flakes do happen.
#
#   3. Else leave the block in place and emit a per-lane diagnostic
#      once so the operator knows human intervention is needed.
#
# Runs until `stop` file appears or SIGTERM. Logs to
# plans/scope-suite/supervisor.log.
# ============================================================================
set -uo pipefail

SUITE=/home/eric/repos/stoke/plans/scope-suite
STATE="$SUITE/ladder-state.json"
LOG="$SUITE/supervisor.log"
RETRY_LOG="$SUITE/supervisor-retries.jsonl"
STOPFILE="$SUITE/supervisor.stop"
MAX_RETRIES_PER_RUNG="${MAX_RETRIES_PER_RUNG:-2}"
POLL_INTERVAL="${POLL_INTERVAL:-90}"

log() { echo "[$(date -Iseconds)] $*" >> "$LOG"; }

current_stoke_sha() {
  git -C /home/eric/repos/stoke log -1 --format=%H 2>/dev/null
}

retry_count_for() {
  local mode="$1" rung="$2"
  if [[ ! -f "$RETRY_LOG" ]]; then
    echo 0
    return
  fi
  grep -c "\"mode\":\"$mode\",\"rung\":\"$rung\"" "$RETRY_LOG" 2>/dev/null || echo 0
}

record_retry() {
  local mode="$1" rung="$2" sha="$3" reason="$4"
  local ts=$(date -Iseconds)
  echo "{\"ts\":\"$ts\",\"mode\":\"$mode\",\"rung\":\"$rung\",\"sha\":\"$sha\",\"reason\":\"$reason\"}" >> "$RETRY_LOG"
}

unblock_lane() {
  local mode="$1"
  python3 - "$STATE" "$mode" <<'PY'
import json, sys
p, mode = sys.argv[1], sys.argv[2]
d = json.load(open(p))
if mode in d:
    d[mode]['last_result'] = 'auto-unblock-by-supervisor'
    d[mode]['blocked_reason'] = None
    json.dump(d, open(p, 'w'), indent=2)
PY
}

relaunch_lane() {
  local mode="$1"
  nohup bash "$SUITE/ladder-driver.sh" --mode "$mode" \
    >> "$SUITE/ladder-driver-$mode.log" 2>&1 &
  log "relaunched lane=$mode pid=$!"
}

check_once() {
  [[ -f "$STATE" ]] || return 0
  local sha=$(current_stoke_sha)
  local payload
  payload=$(python3 - "$STATE" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
for mode, info in d.items():
    br = info.get('blocked_reason') or ''
    lr = info.get('last_result') or ''
    rung = info.get('next_rung') or ''
    if lr == 'passed' or not rung:
        continue
    if not br:
        continue
    print(f"{mode}|{rung}|{lr}")
PY
)
  [[ -z "$payload" ]] && return 0

  while IFS='|' read -r mode rung lr; do
    # Is any stoke process for this lane already running?
    if pgrep -f "ladder-driver.sh --mode $mode" >/dev/null 2>&1; then
      continue
    fi
    if pgrep -f "stoke sow .*$rung-$mode-" >/dev/null 2>&1; then
      continue
    fi
    local retries=$(retry_count_for "$mode" "$rung")
    if (( retries >= MAX_RETRIES_PER_RUNG )); then
      # Only log once per rung — check marker file.
      local marker="$SUITE/.supervisor-gave-up-$mode-$rung"
      if [[ ! -f "$marker" ]]; then
        log "GIVE UP on $mode/$rung after $retries supervisor retries — operator intervention needed"
        touch "$marker"
      fi
      continue
    fi
    log "auto-unblock $mode/$rung (retries=$retries, sha=$sha, last=$lr)"
    record_retry "$mode" "$rung" "$sha" "$lr"
    unblock_lane "$mode"
    relaunch_lane "$mode"
  done <<< "$payload"
}

log "supervisor started (poll=${POLL_INTERVAL}s, max-retries=$MAX_RETRIES_PER_RUNG)"
trap "log 'supervisor exiting on signal'; exit 0" TERM INT

while true; do
  if [[ -f "$STOPFILE" ]]; then
    log "stop file found — exiting"
    rm -f "$STOPFILE"
    exit 0
  fi
  check_once
  sleep "$POLL_INTERVAL"
done
