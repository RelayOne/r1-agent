#!/usr/bin/env bash
# refresh-status.sh — replace the ## Live cohort block in STATUS.md
# with the current ranking/state derived from plans/monitor/state.json.
#
# Strategy: regenerate only the block between
#   <!-- LIVE-COHORT-BEGIN --> and <!-- LIVE-COHORT-END -->
# markers. If either marker is missing, append a new block at the
# bottom. Idempotent; cron-safe.

set -uo pipefail

PLANS=/home/eric/repos/stoke/plans
STATE_FILE=$PLANS/monitor/state.json
STATUS=/home/eric/repos/stoke/STATUS.md

if [[ ! -f "$STATE_FILE" ]]; then
  exit 0
fi
if [[ ! -f "$STATUS" ]]; then
  echo "# Status" > "$STATUS"
fi

NEW_BLOCK=$(python3 - "$STATE_FILE" <<'PY'
import json, sys
with open(sys.argv[1]) as f:
    s = json.load(f)

lines = []
lines.append("<!-- LIVE-COHORT-BEGIN -->")
lines.append(f"## Live cohort (auto-refreshed {s.get('snapshot_iso','?')})")
lines.append("")
lines.append("| Name | PID | Alive | Commits | TS | Gate-hits | cerr | Phase | LogAge |")
lines.append("|---|---|---|---|---|---|---|---|---|")
for v in s.get("variants", []):
    alive = "🟢" if v["alive"] else "💀"
    phase = str(v.get("phase","")).replace("|","\\|")[:100]
    lines.append(
        f"| {v['name']} | {v['pid']} | {alive} | {v['commits']} | {v['ts_count']} | "
        f"{v['gate_hits']} | {v['cerr']} | {phase} | {v.get('log_age_min',-1)}m |"
    )
lines.append("")
lines.append("<!-- LIVE-COHORT-END -->")
print("\n".join(lines))
PY
)

# Replace existing block or append. Use a temp file for atomicity.
TMP=$(mktemp)
if grep -q "<!-- LIVE-COHORT-BEGIN -->" "$STATUS" && grep -q "<!-- LIVE-COHORT-END -->" "$STATUS"; then
  awk -v new="$NEW_BLOCK" '
    BEGIN { in_block = 0 }
    /<!-- LIVE-COHORT-BEGIN -->/ { print new; in_block = 1; next }
    /<!-- LIVE-COHORT-END -->/   { in_block = 0; next }
    !in_block { print }
  ' "$STATUS" > "$TMP"
else
  cat "$STATUS" > "$TMP"
  echo "" >> "$TMP"
  echo "$NEW_BLOCK" >> "$TMP"
fi

mv "$TMP" "$STATUS"
exit 0
