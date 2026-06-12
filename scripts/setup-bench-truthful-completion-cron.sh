#!/usr/bin/env bash
# scripts/setup-bench-truthful-completion-cron.sh
#
# Provisions the checked-in TruthfulCompletion benchmark automation:
#   - reports bucket
#   - monthly manual Cloud Build trigger
#   - PR Cloud Build trigger for bench/antitrunc changes
#   - monthly Cloud Scheduler job that runs the manual trigger
#
# Idempotent — safe to re-run.
#
# Spec: specs/truthful-completion-benchmark.md §T8.1 / §T8.2 (items 174-176).

set -euo pipefail

PROJECT_ID="${PROJECT_ID:-relayone-488319}"
REGION="${REGION:-us-central1}"
CONNECTION="${CONNECTION:-relayone-github-conn}"
REPO="${REPO:-r1-agent-repo}"
REPO_RESOURCE="projects/${PROJECT_ID}/locations/${REGION}/connections/${CONNECTION}/repositories/${REPO}"
SERVICE_ACCOUNT="${SERVICE_ACCOUNT:-projects/${PROJECT_ID}/serviceAccounts/claude-eric-agent@${PROJECT_ID}.iam.gserviceaccount.com}"
BUCKET="${BUCKET:-gs://relayone-488319-r1-bench-reports}"

MONTHLY_TRIGGER="bench-truthful-completion-monthly"
MONTHLY_JOB="bench-truthful-completion-monthly"
PR_TRIGGER="bench-truthful-completion-pr"
SCHEDULE="${SCHEDULE:-0 4 1 * *}"   # 04:00 UTC on the 1st of each month
TIME_ZONE="${TIME_ZONE:-Etc/UTC}"

ensure_bucket() {
  echo "==> ensure bench reports bucket: ${BUCKET}"
  if ! gcloud storage buckets describe "${BUCKET}" --project="${PROJECT_ID}" >/dev/null 2>&1; then
    gcloud storage buckets create "${BUCKET}" --location="${REGION}" --project="${PROJECT_ID}" --uniform-bucket-level-access
    echo "   created"
  else
    echo "   exists"
  fi
}

ensure_monthly_trigger() {
  echo
  echo "==> ensure monthly trigger: ${MONTHLY_TRIGGER}"
  if gcloud builds triggers describe "${MONTHLY_TRIGGER}" --project="${PROJECT_ID}" --region="${REGION}" >/dev/null 2>&1; then
    echo "   trigger ${MONTHLY_TRIGGER} already exists; replacing"
    gcloud builds triggers delete "${MONTHLY_TRIGGER}" --project="${PROJECT_ID}" --region="${REGION}" --quiet >/dev/null
  fi
  gcloud alpha builds triggers create manual \
    --name="${MONTHLY_TRIGGER}" \
    --project="${PROJECT_ID}" \
    --region="${REGION}" \
    --service-account="${SERVICE_ACCOUNT}" \
    --repository="${REPO_RESOURCE}" \
    --branch=main \
    --build-config=services/cloudbuild-bench-truthful-completion-monthly.yaml \
    --description="TruthfulCompletion monthly full benchmark. Runs the checked-in matrix config and uploads reports to ${BUCKET}."
  echo "   trigger ${MONTHLY_TRIGGER} created"
}

ensure_pr_trigger() {
  echo
  echo "==> ensure PR trigger: ${PR_TRIGGER}"
  if gcloud builds triggers describe "${PR_TRIGGER}" --project="${PROJECT_ID}" --region="${REGION}" >/dev/null 2>&1; then
    echo "   trigger ${PR_TRIGGER} already exists; replacing"
    gcloud builds triggers delete "${PR_TRIGGER}" --project="${PROJECT_ID}" --region="${REGION}" --quiet >/dev/null
  fi
  gcloud alpha builds triggers create github \
    --name="${PR_TRIGGER}" \
    --project="${PROJECT_ID}" \
    --region="${REGION}" \
    --service-account="${SERVICE_ACCOUNT}" \
    --repository="${REPO_RESOURCE}" \
    --pull-request-pattern="^main$" \
    --comment-control=COMMENTS_ENABLED_FOR_EXTERNAL_CONTRIBUTORS_ONLY \
    --build-config=services/cloudbuild-bench-truthful-completion-pr.yaml \
    --included-files="internal/antitrunc/**,internal/bench/**,cmd/r1-bench/**,services/cloudbuild-bench-truthful-completion-pr.yaml" \
    --description="TruthfulCompletion PR mini-run for bench/antitrunc changes targeting main."
  echo "   trigger ${PR_TRIGGER} created"
}

ensure_monthly_scheduler() {
  local trigger_id scheduler_url
  trigger_id="$(gcloud builds triggers describe "${MONTHLY_TRIGGER}" --project="${PROJECT_ID}" --region="${REGION}" --format='value(id)')"
  scheduler_url="https://cloudbuild.googleapis.com/v1/projects/${PROJECT_ID}/locations/${REGION}/triggers/${trigger_id}:run"

  echo
  echo "==> ensure monthly scheduler job: ${MONTHLY_JOB}"
  if gcloud scheduler jobs describe "${MONTHLY_JOB}" --location "${REGION}" --project "${PROJECT_ID}" >/dev/null 2>&1; then
    echo "   job ${MONTHLY_JOB} already exists; updating"
    gcloud scheduler jobs update http "${MONTHLY_JOB}" \
      --location "${REGION}" \
      --project "${PROJECT_ID}" \
      --schedule "${SCHEDULE}" \
      --time-zone "${TIME_ZONE}" \
      --uri "${scheduler_url}" \
      --http-method POST \
      --message-body '{"branchName":"main"}' \
      --oauth-service-account-email "claude-eric-agent@${PROJECT_ID}.iam.gserviceaccount.com"
  else
    gcloud scheduler jobs create http "${MONTHLY_JOB}" \
      --location "${REGION}" \
      --project "${PROJECT_ID}" \
      --schedule "${SCHEDULE}" \
      --time-zone "${TIME_ZONE}" \
      --uri "${scheduler_url}" \
      --http-method POST \
      --message-body '{"branchName":"main"}' \
      --oauth-service-account-email "claude-eric-agent@${PROJECT_ID}.iam.gserviceaccount.com" \
      --description "Fires ${MONTHLY_TRIGGER} on the first of each month at 04:00 UTC."
  fi
  echo "   scheduler job ${MONTHLY_JOB} configured"
}

ensure_bucket
ensure_monthly_trigger
ensure_pr_trigger
ensure_monthly_scheduler

echo
echo "==> done. verify with:"
echo "   gcloud builds triggers list --filter='name~bench-truthful-completion' --region=${REGION} --project=${PROJECT_ID}"
echo "   gcloud scheduler jobs describe ${MONTHLY_JOB} --location=${REGION} --project=${PROJECT_ID}"
echo "   gcloud storage ls ${BUCKET}/"
