#!/usr/bin/env bash
# LAUNCH-E1-E4.sh — fresh post-H-24/H-25 cohort launch
#
# Run AFTER codex verification signs off on the diff. Launches four
# focused experiments (see NEXT-EXPERIMENTS.md) with 60s stagger to
# avoid the concurrent-codex Step-2 latency hit observed in the
# 11:45 cohort yesterday.
#
# Prereqs checked at top:
#  - /home/eric/repos/stoke/stoke binary is fresh (rebuilt post-commit)
#  - RelayOne, ActiumChat origin clones exist
#  - Canonical SOWs exist at the named paths

set -euo pipefail

STOKE=/home/eric/repos/stoke/stoke
REPOS=/home/eric/repos

# SAFETY — refuse to launch when any existing e1..e4 dir has a live
# stoke process. This prevents an accidental rerun from nuking a
# running experiment out from under itself (the issue at 11:47 today:
# launcher's rm -rf deleted /home/eric/repos/e3-relayone-feat while
# E3 was mid-round, losing ~15 commits of E1 progress and orphaning
# E3's PID against a deleted tree).
for name in e1-sentinel-simple e2-sentinel-sow e3-relayone-feat e4-actium-scan; do
  if pgrep -f "stoke .*$name" >/dev/null 2>&1; then
    echo "❌ REFUSED: $name is alive (PID $(pgrep -f "stoke .*$name"))."
    echo "   Kill it first, or rename this script invocation to launch a single slot."
    echo "   To launch only E2 (typical single-slot case): inline the E2 block of this script."
    exit 2
  fi
done

if [[ ! -x "$STOKE" ]]; then
  echo "❌ $STOKE missing or not executable — run: (cd /home/eric/repos/stoke && go build -o stoke ./cmd/r1)"
  exit 1
fi

SENTINEL_SOW="$REPOS/sentinel-simple-opus/SOW_WEB_MOBILE.md"
RELAYONE_SOW="$REPOS/relayone-feat-exp/SOW_FEATURE.md"
ACTIUM_SCAN_SOW="$REPOS/actium-scan-exp/SOW_SCAN_REPAIR.md"

for f in "$SENTINEL_SOW" "$RELAYONE_SOW" "$ACTIUM_SCAN_SOW"; do
  [[ -f "$f" ]] || { echo "❌ missing SOW: $f"; exit 1; }
done

# Source of truth clones; E1-E4 get fresh siblings beside them.
for src in RelayOne ActiumChat; do
  [[ -d "$REPOS/$src/.git" ]] || { echo "❌ missing origin clone: $REPOS/$src"; exit 1; }
done

# -------- E1: simple-loop shippability A/B on Sentinel --------
E1_DIR=$REPOS/e1-sentinel-simple
rm -rf "$E1_DIR"
git clone "$REPOS/sentinel-simple-opus" "$E1_DIR"
rm -rf "$E1_DIR/.stoke"
cp "$SENTINEL_SOW" "$E1_DIR/SOW.md"
nohup $STOKE simple-loop \
  --repo "$E1_DIR" \
  --file "$E1_DIR/SOW.md" \
  --reviewer codex \
  --max-rounds 5 \
  --fix-mode sequential \
  --fresh \
  > "$E1_DIR/stoke-run.log" 2>&1 &
echo "🚀 E1 launched (sentinel simple-loop): PID $! log $E1_DIR/stoke-run.log"
sleep 60

# -------- E2: sow + MiniMax + Codex + per-task-worktree on Sentinel --------
E2_DIR=$REPOS/e2-sentinel-sow
rm -rf "$E2_DIR"
git clone "$REPOS/sentinel-simple-opus" "$E2_DIR"
rm -rf "$E2_DIR/.stoke"
cp "$SENTINEL_SOW" "$E2_DIR/SOW.md"
LITELLM_PORT=$(cat "$HOME/.litellm/proxy.port" 2>/dev/null || echo 4000)
LITELLM_KEY=$(grep '^LITELLM_MASTER_KEY=' "$HOME/.litellm/.env" 2>/dev/null | cut -d= -f2- | tr -d '"'"'")
nohup $STOKE sow \
  --repo "$E2_DIR" \
  --file "$E2_DIR/SOW.md" \
  --native-base-url "http://localhost:$LITELLM_PORT" \
  --native-api-key "$LITELLM_KEY" \
  --native-model claude-sonnet-4-6 \
  --reviewer-source codex \
  --per-task-worktree \
  --parallel 2 \
  --fresh \
  > "$E2_DIR/stoke-run.log" 2>&1 &
echo "🚀 E2 launched (sentinel sow cheap+frontier): PID $! log $E2_DIR/stoke-run.log"
sleep 60

# -------- E3: R1F-feat replay (real-world RelayOne simple-loop) --------
E3_DIR=$REPOS/e3-relayone-feat
rm -rf "$E3_DIR"
git clone "$REPOS/RelayOne" "$E3_DIR"
rm -rf "$E3_DIR/.stoke"
cp "$RELAYONE_SOW" "$E3_DIR/SOW.md"
nohup $STOKE simple-loop \
  --repo "$E3_DIR" \
  --file "$E3_DIR/SOW.md" \
  --reviewer codex \
  --max-rounds 5 \
  --fix-mode sequential \
  --fresh \
  > "$E3_DIR/stoke-run.log" 2>&1 &
echo "🚀 E3 launched (relayone feat simple-loop): PID $! log $E3_DIR/stoke-run.log"
sleep 60

# -------- E4: scan-repair on ActiumChat (A-deep replay) --------
E4_DIR=$REPOS/e4-actium-scan
rm -rf "$E4_DIR"
git clone "$REPOS/ActiumChat" "$E4_DIR"
rm -rf "$E4_DIR/.stoke"
nohup $STOKE scan-repair \
  --repo "$E4_DIR" \
  --mode simple-loop \
  --max-sections 0 \
  --max-patterns 0 \
  > "$E4_DIR/stoke-run.log" 2>&1 &
echo "🚀 E4 launched (actium scan-repair): PID $! log $E4_DIR/stoke-run.log"

echo ""
echo "═══════════════════════════════════════════════════════════════"
echo "  All four experiments launched with 60s stagger."
echo "  First evidence checkpoint: 30 min from now."
echo "  Watch: tail -f $REPOS/e{1,2,3,4}-*/stoke-run.log"
echo "═══════════════════════════════════════════════════════════════"
