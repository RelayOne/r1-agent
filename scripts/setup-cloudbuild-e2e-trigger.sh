#!/usr/bin/env bash
# scripts/setup-cloudbuild-e2e-trigger.sh — idempotent installer for
# the release-rehearsal Cloud Build triggers.
#
# Spec: release-rehearsal-ci.md (T2). Reads
# services/cloudbuild-e2e-trigger.yaml and runs `gcloud builds triggers
# import` against the resolute-parity-484218-g1 project. Re-running the script
# updates the existing triggers in-place — does NOT create duplicates.
#
# Required IAM:
#   roles/cloudbuild.builds.editor on resolute-parity-484218-g1
#
# Usage:
#   bash scripts/setup-cloudbuild-e2e-trigger.sh
#
# Exit codes:
#   0 — triggers imported / updated successfully
#   1 — gcloud not available, or import failed (check stderr)
#   2 — caller doesn't have the required IAM role

set -euo pipefail

PROJECT="resolute-parity-484218-g1"
TRIGGER_DESC="$(dirname "$0")/../services/cloudbuild-e2e-trigger.yaml"

if ! command -v gcloud >/dev/null 2>&1; then
  echo "setup-cloudbuild-e2e-trigger: gcloud not on PATH; install Google Cloud SDK first" >&2
  exit 1
fi

if [[ ! -f "$TRIGGER_DESC" ]]; then
  echo "setup-cloudbuild-e2e-trigger: descriptor not found at $TRIGGER_DESC" >&2
  exit 1
fi

# Permission preflight — surface a clear error rather than letting
# gcloud's underlying API call fail with a generic permission denied.
if ! gcloud builds triggers list --project="$PROJECT" --format="value(name)" >/dev/null 2>&1; then
  echo "setup-cloudbuild-e2e-trigger: cannot list triggers in $PROJECT — caller likely lacks roles/cloudbuild.builds.editor" >&2
  exit 2
fi

echo "setup-cloudbuild-e2e-trigger: importing from $TRIGGER_DESC into $PROJECT"
gcloud builds triggers import \
  --source="$TRIGGER_DESC" \
  --project="$PROJECT"

echo "setup-cloudbuild-e2e-trigger: imported. Verify with:"
echo "  gcloud builds triggers list --project=$PROJECT --filter='name~e2e-rehearsal'"
