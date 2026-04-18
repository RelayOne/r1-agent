#!/usr/bin/env bash
# manage.sh — auto-manage the E1-E4 cohort based on wedge rules
# derived from yesterday's 14h run. Kill conditions are conservative:
# each rule needs both a liveness signal AND a git-progress signal to
# avoid killing a variant that's working but quiet.
#
# Rules:
#   - WEDGED: log-mtime > 20 min AND git rev-count unchanged for 30+ min
#     → SIGTERM then SIGKILL via process group.
#   - PROCESS DEAD (not alive, clean exit code preserved in log):
#     mark it so manage.sh doesn't re-check next cycle.
#
# Reads plans/monitor/state.json (written by snapshot.sh) and
# plans/monitor/history.json (prior commits + timestamp per variant,
# written by this script). First run seeds history, kills nothing.
#
# Prints one line per kill decision. Exits 0 on no action; 1 if a
# kill fired (so the outer driver can flag it).

set -uo pipefail

PLANS=/home/eric/repos/stoke/plans
STATE_FILE=$PLANS/monitor/state.json
HIST_FILE=$PLANS/monitor/history.json
DECISIONS_LOG=$PLANS/monitor/manage-decisions.log

if [[ ! -f "$STATE_FILE" ]]; then
  echo "manage: no state file at $STATE_FILE — did snapshot.sh run?" >&2
  exit 0
fi

# Wedge thresholds (minutes).
LOG_STALE_THRESHOLD=20
COMMIT_STALE_THRESHOLD=30

NOW=$(date +%s)
NOW_ISO=$(date -Iseconds)

# Load history (empty map if missing).
if [[ -f "$HIST_FILE" ]]; then
  HIST_BODY=$(cat "$HIST_FILE")
else
  HIST_BODY='{}'
fi

# Python does the actual policy work — easier than jq-over-bash for
# the paired state+history update.
python3 - "$STATE_FILE" "$HIST_BODY" "$LOG_STALE_THRESHOLD" "$COMMIT_STALE_THRESHOLD" "$NOW" "$NOW_ISO" "$DECISIONS_LOG" "$HIST_FILE" <<'PY'
import json, os, signal, sys

state_path    = sys.argv[1]
hist_body     = sys.argv[2]
log_stale     = int(sys.argv[3])   # minutes
cmt_stale_min = int(sys.argv[4])   # minutes
now_epoch     = int(sys.argv[5])
now_iso       = sys.argv[6]
decisions_log = sys.argv[7]
hist_path     = sys.argv[8]

with open(state_path) as f:
    state = json.load(f)

try:
    history = json.loads(hist_body)
except Exception:
    history = {}

killed_anything = False
new_history = {}

with open(decisions_log, "a") as dlog:
    for v in state.get("variants", []):
        name = v["name"]
        prev = history.get(name, {})
        # Update history for this name (current commits + timestamp if
        # commits changed, else preserve prior changed-at).
        cur_commits = v["commits"]
        prev_commits = prev.get("commits", -1)
        if cur_commits != prev_commits:
            changed_at = now_epoch
        else:
            changed_at = prev.get("changed_at", now_epoch)
        new_history[name] = {"commits": cur_commits, "changed_at": changed_at}

        # Skip management on dead processes — nothing to kill.
        if not v["alive"]:
            continue

        # Skip first observation (no prior history) — need a prior
        # data point to compute "unchanged for X minutes."
        if prev_commits < 0:
            continue

        log_age  = v.get("log_age_min", 0)
        cmt_idle = (now_epoch - changed_at) // 60

        wedged = (log_age >= log_stale) and (cmt_idle >= cmt_stale_min)
        if not wedged:
            continue

        pid = v["pid"]
        dlog.write(
            f"{now_iso} KILL {name} pid={pid} log_age={log_age}m cmt_idle={cmt_idle}m "
            f"phase='{v.get('phase','?')}'\n"
        )
        print(f"🧹 KILL {name} (pid {pid}): log stale {log_age}m + commits idle {cmt_idle}m")

        # SIGTERM the process group; best-effort SIGKILL 2s later.
        try:
            pgid = os.getpgid(pid)
            os.killpg(pgid, signal.SIGTERM)
        except ProcessLookupError:
            pass
        except PermissionError as e:
            dlog.write(f"{now_iso} SIGTERM denied on {name} pid={pid}: {e}\n")

        import time
        time.sleep(2)
        try:
            pgid = os.getpgid(pid)
            os.killpg(pgid, signal.SIGKILL)
        except Exception:
            pass

        killed_anything = True
        # Mark this variant as killed in history so repeated 5-min
        # checks don't emit "killed X again and again" noise when the
        # process group takes a beat to reap.
        new_history[name]["killed_at"] = now_epoch

with open(hist_path, "w") as f:
    json.dump(new_history, f, indent=2)

sys.exit(1 if killed_anything else 0)
PY
EXIT=$?
exit $EXIT
