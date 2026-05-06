#!/bin/bash
# scripts/lint-lane-events.sh
# Lints for direct streamjson writes of lane.* events that bypass the
# internal/streamjson/lane.go adapter.
#
# Spec: specs/lanes-protocol.md item 32.
#
# Wire into the existing CI gate. Per CLAUDE.md, the canonical CI commands are
#   go build ./cmd/r1
#   go test ./...
#   go vet ./...
# This script is invoked alongside `go vet` from cloudbuild.yaml.
#
# Run locally: bash scripts/lint-lane-events.sh
# Exit code: 0 on clean, non-zero on bypass detected.

set -e

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# Files allowed to write lane.* events directly:
#   internal/streamjson/lane.go               — the canonical adapter
#   internal/streamjson/lane_test.go          — tests for the adapter
#   internal/streamjson/lane_golden_test.go   — golden replay tests
#   internal/streamjson/testdata/lanes/*.json — golden fixtures
ALLOWED_FILES=(
  "internal/streamjson/lane.go"
  "internal/streamjson/lane_test.go"
  "internal/streamjson/lane_golden_test.go"
  "internal/streamjson/testdata/lanes/"
  "internal/streamjson/twolane.go"      # canonical critical-classification dispatcher
)

# Search for direct emit patterns of lane.* event types in streamjson code that
# isn't in the allowed-files list.
#
# Patterns that indicate bypass (false positives are tolerated; they prompt
# the developer to either add their file to ALLOWED_FILES with justification
# or route through the adapter):
#   "lane.created" | "lane.status" | "lane.delta" | "lane.cost"
#   | "lane.note"  | "lane.killed"  appearing as a string literal in a Go file
#   under internal/streamjson/, NOT in an allowed file.

FOUND=0
TMP=$(mktemp)
cd "$REPO_ROOT"

# Find all *.go files under internal/streamjson/ that mention any lane.* literal.
# Then exclude allowed files.
grep -rEn --include='*.go' \
  '"(lane\.created|lane\.status|lane\.delta|lane\.cost|lane\.note|lane\.killed)"' \
  internal/streamjson/ > "$TMP" 2>/dev/null || true

while IFS= read -r line; do
  file="${line%%:*}"
  allowed=0
  for allowed_path in "${ALLOWED_FILES[@]}"; do
    if [[ "$file" == "$allowed_path"* ]]; then
      allowed=1
      break
    fi
  done
  if [[ $allowed -eq 0 ]]; then
    echo "BYPASS: $line"
    FOUND=1
  fi
done < "$TMP"

rm -f "$TMP"

if [[ $FOUND -ne 0 ]]; then
  echo ""
  echo "scripts/lint-lane-events.sh: lane events written outside the canonical adapter."
  echo "Either route via internal/streamjson/lane.go RegisterLaneEvents, or"
  echo "add the file to ALLOWED_FILES in this script with justification."
  exit 1
fi

echo "scripts/lint-lane-events.sh: clean — no lane-event bypass detected."
