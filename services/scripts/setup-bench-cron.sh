#!/usr/bin/env bash
# services/scripts/setup-bench-cron.sh — wire the nightly benchmark
# automation that re-homes the deleted bench-nightly.yml GHA workflow.
# Resolves issue #99.
#
# Creates two pieces:
#   1. A Cloud Build trigger (manual, no auto-fire) that runs the
#      services/cloudbuild-bench-nightly.yaml pipeline.
#   2. A Cloud Scheduler job that fires a runBuild call against that
#      trigger every night at 04:00 UTC.
#
# Idempotent — safe to re-run.
set -euo pipefail

PROJECT="${PROJECT:-relayone-488319}"
REGION="${REGION:-us-central1}"
CONNECTION="${CONNECTION:-relayone-github-conn}"
REPO="${REPO:-r1-agent-repo}"
REPO_RESOURCE="projects/${PROJECT}/locations/${REGION}/connections/${CONNECTION}/repositories/${REPO}"
SERVICE_ACCOUNT="${SERVICE_ACCOUNT:-projects/${PROJECT}/serviceAccounts/claude-eric-agent@${PROJECT}.iam.gserviceaccount.com}"
TRIGGER_NAME="r1-bench-nightly"
SCHEDULER_JOB="r1-bench-nightly-cron"
BUCKET="gs://relayone-488319-r1-bench-reports"

echo "==> 1. ensure bench reports bucket: $BUCKET"
if ! gcloud storage buckets describe "$BUCKET" --project="$PROJECT" >/dev/null 2>&1; then
  gcloud storage buckets create "$BUCKET" --location="us-central1" --project="$PROJECT" --uniform-bucket-level-access
  echo "   created"
else
  echo "   exists"
fi

echo
echo "==> 2. ensure Cloud Build trigger: $TRIGGER_NAME"
if gcloud builds triggers describe "$TRIGGER_NAME" --region="$REGION" --project="$PROJECT" >/dev/null 2>&1; then
  echo "   trigger ${TRIGGER_NAME} already exists; replacing"
  gcloud builds triggers delete "$TRIGGER_NAME" --region="$REGION" --project="$PROJECT" --quiet >/dev/null
fi
# Use --branch on a manual-only trigger so Scheduler can pin runs to main.
gcloud alpha builds triggers create manual \
  --name="$TRIGGER_NAME" \
  --project="$PROJECT" \
  --region="$REGION" \
  --service-account="$SERVICE_ACCOUNT" \
  --repository="$REPO_RESOURCE" \
  --branch=main \
  --build-config=services/cloudbuild-bench-nightly.yaml \
  --description="Nightly benchmark — runs bench/cmd/bench over corpus, uploads to $BUCKET. Fires on schedule from $SCHEDULER_JOB."
echo "   trigger ${TRIGGER_NAME} created"

# Resolve the trigger id for the Scheduler URL.
TRIGGER_ID=$(gcloud builds triggers describe "$TRIGGER_NAME" --region="$REGION" --project="$PROJECT" --format='value(id)')
SCHEDULER_URL="https://cloudbuild.googleapis.com/v1/projects/${PROJECT}/locations/${REGION}/triggers/${TRIGGER_ID}:run"

echo
echo "==> 3. ensure Cloud Scheduler job: $SCHEDULER_JOB"
if gcloud scheduler jobs describe "$SCHEDULER_JOB" --location="$REGION" --project="$PROJECT" >/dev/null 2>&1; then
  echo "   job ${SCHEDULER_JOB} already exists; updating"
  gcloud scheduler jobs update http "$SCHEDULER_JOB" \
    --location="$REGION" \
    --project="$PROJECT" \
    --schedule="0 4 * * *" \
    --time-zone="Etc/UTC" \
    --uri="$SCHEDULER_URL" \
    --http-method=POST \
    --message-body='{"branchName":"main"}' \
    --oauth-service-account-email="claude-eric-agent@${PROJECT}.iam.gserviceaccount.com"
else
  gcloud scheduler jobs create http "$SCHEDULER_JOB" \
    --location="$REGION" \
    --project="$PROJECT" \
    --schedule="0 4 * * *" \
    --time-zone="Etc/UTC" \
    --uri="$SCHEDULER_URL" \
    --http-method=POST \
    --message-body='{"branchName":"main"}' \
    --oauth-service-account-email="claude-eric-agent@${PROJECT}.iam.gserviceaccount.com" \
    --description="Fires r1-bench-nightly Cloud Build trigger every night at 04:00 UTC. Resolves issue #99."
fi
echo "   scheduler job ${SCHEDULER_JOB} configured"

echo
echo "==> done. Verify with:"
echo "   gcloud scheduler jobs describe $SCHEDULER_JOB --location=$REGION --project=$PROJECT"
echo "   gcloud builds triggers list --filter='name~r1-bench' --region=$REGION --project=$PROJECT"
echo "   gcloud storage ls $BUCKET/"
