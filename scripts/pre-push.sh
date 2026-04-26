#!/usr/bin/env bash
# scripts/pre-push.sh — r1 (stoke) CI gate (pre-push hook)
# Install: cp scripts/pre-push.sh .git/hooks/pre-push && chmod +x .git/hooks/pre-push
# Runs before any push to ensure tests pass locally.
# Mirrors what Cloud Build runs remotely.
set -euo pipefail

echo "[pre-push] Running r1 CI gate..."

go build ./cmd/stoke ./cmd/r1 ./cmd/stoke-acp
go vet ./...
go test ./... -count=1 -timeout=120s

echo "[pre-push] CI gate passed."
