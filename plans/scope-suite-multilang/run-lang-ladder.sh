#!/usr/bin/env bash
# ============================================================================
# run-lang-ladder.sh — generic driver for any scope-suite-<lang> ladder
#
# Usage: run-lang-ladder.sh --lang rust [--mode sow-serial|simple-loop-lenient]
#                           [--rungs R01 R02 ...] [--parallel]
#
# Locates rungs in $STOKE/plans/scope-suite-<lang>/rungs/Rxx-*.md and
# for each one launches stoke against a fresh run dir in
# /home/eric/repos/scope-suite-runs-<lang>/.
#
# Sequential by default; pass --parallel to fire all rungs at once
# with a 5s stagger between spawns.
# ============================================================================
set -uo pipefail

STOKE_ROOT=/home/eric/repos/stoke
STOKE=$STOKE_ROOT/stoke
LANG=""
MODE="sow-serial"
RUNGS=()
PARALLEL=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --lang) LANG="$2"; shift 2;;
    --mode) MODE="$2"; shift 2;;
    --rungs) shift; while [[ $# -gt 0 && "$1" != --* ]]; do RUNGS+=("$1"); shift; done;;
    --parallel) PARALLEL=1; shift;;
    *) echo "unknown: $1"; exit 2;;
  esac
done
[[ -z "$LANG" ]] && { echo "--lang required"; exit 2; }

SUITE=$STOKE_ROOT/plans/scope-suite-${LANG}
[[ -d "$SUITE" ]] || { echo "no suite at $SUITE"; exit 2; }
RUNS_ROOT=/home/eric/repos/scope-suite-runs-${LANG}
mkdir -p "$RUNS_ROOT"

# Discover all rung specs if --rungs wasn't given.
if [[ ${#RUNGS[@]} -eq 0 ]]; then
  for f in "$SUITE/rungs/"R??-*.md; do
    base=$(basename "$f")
    RUNGS+=("${base:0:3}")
  done
fi

PORT=$(cat "$HOME/.litellm/proxy.port" 2>/dev/null || echo 4000)
API_KEY=$(grep '^LITELLM_MASTER_KEY=' "$HOME/.litellm/.env" 2>/dev/null | cut -d= -f2- | tr -d '"'"'")
BASE_URL="http://localhost:${PORT}"

timeout_for() {
  local r="$1"
  case "$r" in
    R01|R02) echo "45m";;
    R03|R04) echo "90m";;
    *) echo "120m";;
  esac
}

launch_rung() {
  local rung="$1"
  local src=$(ls "$SUITE/rungs/${rung}"*.md 2>/dev/null | head -1)
  [[ -z "$src" ]] && { echo "[$LANG/$rung] no spec"; return 1; }
  local ts=$(date +%Y%m%dT%H%M%S)
  local dir="$RUNS_ROOT/${rung}-${MODE}-${ts}"
  mkdir -p "$dir"
  cp "$src" "$dir/SOW.md"
  git -C "$dir" init -q
  # Language-specific minimal seed file so preflight/scrub has context.
  case "$LANG" in
    rust) echo "[package]\nname = \"rung-${rung,,}\"\nversion = \"0.1.0\"\nedition = \"2021\"" > "$dir/Cargo.toml";;
    python) printf '[project]\nname = "rung-%s"\nversion = "0.1.0"\nrequires-python = ">=3.10"\n' "${rung,,}" > "$dir/pyproject.toml";;
    go) echo "module example.com/rung-${rung,,}" > "$dir/go.mod"; echo "" >> "$dir/go.mod"; echo "go 1.22" >> "$dir/go.mod";;
    react-native) printf '{"name":"rung-%s","version":"1.0.0","private":true}\n' "${rung,,}" > "$dir/package.json";;
  esac
  git -C "$dir" add -A
  git -C "$dir" -c user.email=x@x -c user.name=x commit -q -m "seed" 2>/dev/null || true

  local to=$(timeout_for "$rung")
  local log="$dir/stoke-run.log"
  echo "[$LANG/$rung] starting (mode=$MODE, timeout=$to, dir=${dir##*/})"
  (
    # STOKE_SYSDEP_INSTALL=1 opts into H-69 apt-install preflight so
    # ACs referencing docker/psql/redis-cli/etc. resolve before worker
    # dispatch. Best-effort (no sudo → silent skip); successful
    # installs make the difference between AC2-docker-compose passing
    # and hitting exit 127.
    export STOKE_SYSDEP_INSTALL=1
    # H-86: prepend ~/.cargo/bin + pyenv shims so the run picks up
    # rustup's current toolchain (1.95) instead of the system rustc
    # (1.84 on Debian) which can't parse edition2024. R06/R07 rust
    # sow-serial today lost AC2 to 'indexmap 2.14.0 requires
    # edition2024' even though rustup had 1.95 available, because
    # PATH resolved to /usr/bin/rustc first.
    if [[ -d "$HOME/.cargo/bin" ]]; then
      export PATH="$HOME/.cargo/bin:$PATH"
    fi
    if [[ -d "$HOME/.pyenv/shims" ]]; then
      export PATH="$HOME/.pyenv/shims:$PATH"
    fi
    case "$MODE" in
      sow|sow-serial)
        STOKE_PERFLOG=1 STOKE_PERFLOG_FILE="$dir/perflog.txt" \
          timeout "$to" "$STOKE" sow \
          --repo "$dir" --file "$dir/SOW.md" \
          --native-base-url "$BASE_URL" --native-api-key "$API_KEY" \
          --native-model claude-sonnet-4-6 \
          --reviewer-source codex --workflow serial --fresh \
          > "$log" 2>&1
        ;;
      simple-loop|loop-strict)
        STOKE_PERFLOG=1 STOKE_PERFLOG_FILE="$dir/perflog.txt" \
          timeout "$to" "$STOKE" simple-loop \
          --repo "$dir" --file "$dir/SOW.md" \
          --reviewer codex --max-rounds 5 \
          --fix-mode sequential --fresh \
          > "$log" 2>&1
        ;;
      loop-lenient)
        STOKE_PERFLOG=1 STOKE_PERFLOG_FILE="$dir/perflog.txt" \
          timeout "$to" "$STOKE" simple-loop \
          --repo "$dir" --file "$dir/SOW.md" \
          --reviewer codex --max-rounds 3 \
          --fix-mode sequential --fresh --lenient-compliance \
          > "$log" 2>&1
        ;;
      *) echo "unsupported mode: $MODE"; exit 2;;
    esac
    echo "[$LANG/$rung] exit=$?"
  )
}

if (( PARALLEL )); then
  for r in "${RUNGS[@]}"; do
    launch_rung "$r" &
    sleep 5
  done
  wait
else
  for r in "${RUNGS[@]}"; do
    launch_rung "$r"
  done
fi
