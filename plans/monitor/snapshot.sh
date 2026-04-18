#!/usr/bin/env bash
# snapshot.sh — capture one 5-min snapshot of each active experiment.
# Appends a dated block to monitor-log.md and writes a parseable JSON
# state file to plans/monitor/state.json for manage.sh to read.
#
# Variants to watch are declared below. To add/remove an experiment,
# edit VARIANTS. Everything else is derived from stoke-run.log + git.

set -uo pipefail

PLANS=/home/eric/repos/stoke/plans
STATE_DIR=$PLANS/monitor
STATE_FILE=$STATE_DIR/state.json
MONITOR_LOG=/home/eric/repos/stoke/monitor-log.md

mkdir -p "$STATE_DIR"

# Format: name repo_dir
# Kept in bash array pairs — name handles ranking; repo_dir locates logs.
VARIANTS=(
  "E1|/home/eric/repos/e1-sentinel-simple"
  "E2|/home/eric/repos/e2-sentinel-sow"
  "E3|/home/eric/repos/e3-relayone-feat"
  "E4|/home/eric/repos/e4-actium-scan"
  "E5|/home/eric/repos/e5-sentinel-h27"
  "E6|/home/eric/repos/e6-sentinel-sow-h27"
  "E7|/home/eric/repos/e7-sentinel-h28"
  "E8|/home/eric/repos/e8-sentinel-sow-h28"
)

NOW_ISO=$(date -Iseconds)
NOW_EPOCH=$(date +%s)

echo "" >> "$MONITOR_LOG"
echo "## Check $NOW_ISO" >> "$MONITOR_LOG"
echo "" >> "$MONITOR_LOG"
echo "| Name | PID | Commits | TS | Gates | H-27 | H-28 | cerr | Phase | LogAge |" >> "$MONITOR_LOG"
echo "|---|---|---|---|---|---|---|---|---|---|" >> "$MONITOR_LOG"

echo "{" > "$STATE_FILE.tmp"
echo "  \"snapshot_iso\": \"$NOW_ISO\"," >> "$STATE_FILE.tmp"
echo "  \"snapshot_epoch\": $NOW_EPOCH," >> "$STATE_FILE.tmp"
echo "  \"variants\": [" >> "$STATE_FILE.tmp"

first=1
for entry in "${VARIANTS[@]}"; do
  name="${entry%%|*}"
  repo="${entry#*|}"
  log="$repo/stoke-run.log"

  # Derive: PID from pgrep on stoke+repo path; alive flag; commits;
  # TS file count; gate-hits; cerr; most-recent phase-banner line; log age.
  pid=$(pgrep -f "stoke .*$repo" 2>/dev/null | head -1)
  pid=${pid:-0}
  if [[ "$pid" != "0" ]] && kill -0 "$pid" 2>/dev/null; then
    alive=true
  else
    alive=false
  fi

  if [[ -d "$repo/.git" ]]; then
    commits=$(git -C "$repo" rev-list --count HEAD 2>/dev/null || echo 0)
    ts_count=$(git -C "$repo" ls-files -- '*.ts' '*.tsx' 2>/dev/null | wc -l | tr -d ' ')
  else
    commits=0
    ts_count=0
  fi

  if [[ -f "$log" ]]; then
    # Pipe through tr -d to collapse any trailing newline + `|| echo 0`
    # produced a double-0 before. `2>/dev/null` handles missing-file;
    # `|| true` keeps the pipeline alive when grep finds nothing.
    # Gate-hit lines are emitted as `  [gate-hit] ...` with a leading
    # indent, so anchor via `\[gate-hit\]` not `^\[`. Fixed 12:24.
    gate_hits=$({ grep -cE "\[gate-hit\]" "$log" 2>/dev/null || true; } | head -1 | tr -d '\n ')
    gate_hits=${gate_hits:-0}
    cerr=$({ grep -c "claude error: exit status" "$log" 2>/dev/null || true; } | head -1 | tr -d '\n ')
    cerr=${cerr:-0}
    # H-27 hits: `declared-symbol-not-implemented` — track separately
    # so the live cohort table shows H-27 validation evidence.
    # H-27 regex hits vs H-28 tree-sitter hits — separate columns so
    # the live cohort table shows per-variant gate-fire counts.
    # H-28 kind is declared-symbol-not-implemented-ts, H-27 is
    # declared-symbol-not-implemented (no suffix).
    h27_hits=$({ grep -cE "declared-symbol-not-implemented[^-]" "$log" 2>/dev/null || true; } | head -1 | tr -d '\n ')
    h27_hits=${h27_hits:-0}
    h28_hits=$({ grep -cE "declared-symbol-not-implemented-ts" "$log" 2>/dev/null || true; } | head -1 | tr -d '\n ')
    h28_hits=${h28_hits:-0}
    phase=$(grep -E "^(📋 Step |🔧 Step |📝 Step |🏗️ |👀 Step |📦 |💡 Final review|  ROUND )" "$log" 2>/dev/null | tail -1 | head -c 120)
    phase=${phase:-"(no phase-banner yet)"}
    log_mtime=$(stat -c %Y "$log" 2>/dev/null || echo 0)
    log_age_sec=$(( NOW_EPOCH - log_mtime ))
    log_age_min=$(( log_age_sec / 60 ))
  else
    gate_hits=0
    h27_hits=0
    h28_hits=0
    cerr=0
    phase="(no log yet)"
    log_age_min=-1
  fi

  # Markdown row — escape pipes in phase.
  phase_md=${phase//|/\\|}
  echo "| $name | $pid | $commits | $ts_count | $gate_hits | $h27_hits | $h28_hits | $cerr | $phase_md | ${log_age_min}m |" >> "$MONITOR_LOG"

  [[ $first -eq 0 ]] && echo "," >> "$STATE_FILE.tmp"
  first=0
  cat >> "$STATE_FILE.tmp" <<EOF
    {
      "name": "$name",
      "repo": "$repo",
      "pid": $pid,
      "alive": $alive,
      "commits": $commits,
      "ts_count": $ts_count,
      "gate_hits": $gate_hits,
      "h27_hits": $h27_hits,
      "h28_hits": $h28_hits,
      "cerr": $cerr,
      "phase": $(printf '%s' "$phase" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read().strip()))' 2>/dev/null || echo "\"$phase\""),
      "log_age_min": $log_age_min
    }
EOF
done

echo "" >> "$STATE_FILE.tmp"
echo "  ]" >> "$STATE_FILE.tmp"
echo "}" >> "$STATE_FILE.tmp"

mv "$STATE_FILE.tmp" "$STATE_FILE"

# Rotate monitor-log.md when it crosses 2 MB so cron writes don't
# unbound-grow it forever.
size=$(stat -c %s "$MONITOR_LOG" 2>/dev/null || echo 0)
if [[ "$size" -gt 2097152 ]]; then
  mv "$MONITOR_LOG" "$MONITOR_LOG.$(date +%Y%m%dT%H%M%S).rot"
  echo "# Stoke monitor log (rotated)" > "$MONITOR_LOG"
fi

exit 0
