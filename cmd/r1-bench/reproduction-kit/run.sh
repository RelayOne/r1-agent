#!/usr/bin/env bash
# cmd/r1-bench/reproduction-kit/run.sh
#
# Reproduces the published TruthfulCompletion leaderboard by running
# every (agent, mission) pair through the docker-compose matrix.
#
# Usage:
#   export ANTHROPIC_API_KEY=...  # required for claude-code, r1
#   export OPENAI_API_KEY=...      # required for codex-cli, aider
#   ./run.sh
#
# Output: ./out/<agent>--<mission>.json per run.
#
# Spec: specs/truthful-completion-benchmark.md §T8.3 (item 57).

set -euo pipefail

# Cd into the directory containing this script — paths in
# docker-compose.yml are relative to it.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${SCRIPT_DIR}"

mkdir -p out

# Validate that at least one API key is present so the run can
# actually produce results.
if [ -z "${ANTHROPIC_API_KEY:-}" ] && [ -z "${OPENAI_API_KEY:-}" ]; then
  echo "ERROR: neither ANTHROPIC_API_KEY nor OPENAI_API_KEY is set." >&2
  echo "       At least one external agent requires an API key to run." >&2
  exit 1
fi

# Per-mission, per-agent run. The 5 seed missions are the public
# defaults; operators with the full 100-mission corpus can override
# MISSIONS via env.
MISSIONS="${MISSIONS:-seed-hello-easy seed-refactor-medium seed-feature-medium seed-migration-hard seed-perfect-agent-fixture}"
SERVICES="${SERVICES:-r1-bench-r1 r1-bench-claude-code r1-bench-aider}"

echo "Building containers..."
docker compose build

for MISSION in $MISSIONS; do
  for SVC in $SERVICES; do
    echo "=== Running ${SVC} on ${MISSION} ==="
    # Override the command's --mission via docker compose run.
    docker compose run --rm "${SVC}" \
      --mission "${MISSION}" \
      --corpus /opt/r1/golden/truthful-completion \
      --output "/out/${SVC}--${MISSION}.json" \
      || echo "WARN: ${SVC}/${MISSION} failed; continuing"
  done
done

echo
echo "Done. Results in ${SCRIPT_DIR}/out/"
ls -la out/
