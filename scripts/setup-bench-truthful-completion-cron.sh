#!/usr/bin/env bash
# scripts/setup-bench-truthful-completion-cron.sh
#
# Provisions the Cloud Scheduler job that triggers the monthly
# TruthfulCompletion benchmark Cloud Build. Idempotent — safe to
# re-run.
#
# Spec: specs/truthful-completion-benchmark.md §T8.1 (item 55).
#
# Run once per project (and after every Cloud Build trigger rename).

set -euo pipefail

PROJECT_ID="${PROJECT_ID:-relayone-488319}"
REGION="${REGION:-us-central1}"
JOB_NAME="bench-truthful-completion-monthly"
TRIGGER_NAME="bench-truthful-completion-monthly"
SCHEDULE="${SCHEDULE:-0 4 1 * *}"   # 04:00 UTC on the 1st of each month
TIME_ZONE="${TIME_ZONE:-Etc/UTC}"

# Sanity check: the Cloud Build trigger must already exist. The
# trigger is created via:
#   gcloud builds triggers import services/cloudbuild-bench-truthful-completion-monthly.yaml
# (or via the Cloud Console). This script wires the SCHEDULER to that
# trigger; it does not create the trigger itself.
if ! gcloud builds triggers describe "${TRIGGER_NAME}" \
     --project "${PROJECT_ID}" --region "${REGION}" >/dev/null 2>&1; then
  echo "ERROR: Cloud Build trigger '${TRIGGER_NAME}' does not exist in" \
       "project '${PROJECT_ID}', region '${REGION}'. Create it first" \
       "by importing services/cloudbuild-bench-truthful-completion-monthly.yaml." >&2
  exit 1
fi

TRIGGER_ID=$(gcloud builds triggers describe "${TRIGGER_NAME}" \
  --project "${PROJECT_ID}" --region "${REGION}" --format='value(id)')

SCHEDULER_URI="https://cloudbuild.googleapis.com/v1/projects/${PROJECT_ID}/locations/${REGION}/triggers/${TRIGGER_ID}:run"
SCHEDULER_SA="cloud-scheduler@${PROJECT_ID}.iam.gserviceaccount.com"

if gcloud scheduler jobs describe "${JOB_NAME}" \
     --location "${REGION}" --project "${PROJECT_ID}" >/dev/null 2>&1; then
  echo "Updating existing Cloud Scheduler job '${JOB_NAME}'"
  gcloud scheduler jobs update http "${JOB_NAME}" \
    --location "${REGION}" \
    --project "${PROJECT_ID}" \
    --schedule "${SCHEDULE}" \
    --time-zone "${TIME_ZONE}" \
    --uri "${SCHEDULER_URI}" \
    --http-method POST \
    --oauth-service-account-email "${SCHEDULER_SA}" \
    --message-body '{"branchName":"main"}'
else
  echo "Creating Cloud Scheduler job '${JOB_NAME}'"
  gcloud scheduler jobs create http "${JOB_NAME}" \
    --location "${REGION}" \
    --project "${PROJECT_ID}" \
    --schedule "${SCHEDULE}" \
    --time-zone "${TIME_ZONE}" \
    --uri "${SCHEDULER_URI}" \
    --http-method POST \
    --oauth-service-account-email "${SCHEDULER_SA}" \
    --message-body '{"branchName":"main"}'
fi

echo "Done. Job '${JOB_NAME}' fires on schedule '${SCHEDULE}' (${TIME_ZONE})."
echo "Cloud Build trigger: ${TRIGGER_NAME} (${TRIGGER_ID})"
