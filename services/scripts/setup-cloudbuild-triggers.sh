#!/usr/bin/env bash
# services/scripts/setup-cloudbuild-triggers.sh — wire only the 3 SaaS deploy triggers.
#
# Creates one Cloud Build trigger per env. Each fires on push to its
# matching branch and uses services/cloudbuild-deploy.yaml with
# substitution _ENV.
#
# Idempotent — safe to re-run.
# Does not manage the separate r1-agent CI / PR triggers.
set -euo pipefail

PROJECT="${PROJECT:-relayone-488319}"
REGION="${REGION:-us-central1}"
CONNECTION="${CONNECTION:-relayone-github-conn}"
REPO="${REPO:-r1-agent-repo}"
REPO_RESOURCE="projects/${PROJECT}/locations/${REGION}/connections/${CONNECTION}/repositories/${REPO}"
SERVICE_ACCOUNT="${SERVICE_ACCOUNT:-projects/${PROJECT}/serviceAccounts/claude-eric-agent@${PROJECT}.iam.gserviceaccount.com}"

sample_rate_for_env() {
  local env="$1"
  case "$env" in
    dev|staging) echo "1.0" ;;
    prod) echo "0.1" ;;
    *) echo "0.1" ;;
  esac
}

create_trigger() {
  local name="$1" branch="$2" env="$3"
  local coderadar_sample_rate
  coderadar_sample_rate="$(sample_rate_for_env "$env")"
  echo
  echo "==> trigger ${name} (branch=${branch}, env=${env})"
  if gcloud builds triggers describe "$name" --region="$REGION" --project="$PROJECT" >/dev/null 2>&1; then
    echo "   trigger ${name} already exists; replacing"
    gcloud builds triggers delete "$name" --region="$REGION" --project="$PROJECT" --quiet >/dev/null
  fi
  gcloud alpha builds triggers create github \
    --name="$name" \
    --project="$PROJECT" \
    --region="$REGION" \
    --service-account="$SERVICE_ACCOUNT" \
    --repository="$REPO_RESOURCE" \
    --branch-pattern="^${branch}\$" \
    --build-config=services/cloudbuild-deploy.yaml \
    --substitutions="_ENV=${env},_CODERADAR_SAMPLE_RATE=${coderadar_sample_rate}" \
    --description="r1 SaaS deploy (${env}) — build + deploy r1-coord-api / r1-docs / r1-downloads-cdn / r1-admin on push to ${branch}"
  echo "   trigger ${name} created"
}

create_trigger r1-services-prod-deploy main prod
create_trigger r1-services-staging-deploy staging staging
create_trigger r1-services-dev-deploy dev dev

echo
echo "==> done. r1-services deploy triggers:"
gcloud builds triggers list --filter="name~r1-services" --region="$REGION" --format="table(name,filename)" --project="$PROJECT"
