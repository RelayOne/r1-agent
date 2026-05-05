#!/usr/bin/env bash
# services/scripts/setup-cloudbuild-triggers.sh — wire the 3 SaaS deploy triggers.
#
# Creates one Cloud Build trigger per env. Each fires on push to its
# matching branch and uses services/cloudbuild-deploy.yaml with
# substitution _ENV.
#
# Idempotent — safe to re-run.
set -euo pipefail

PROJECT="${PROJECT:-relayone-488319}"
REPO_OWNER="${REPO_OWNER:-RelayOne}"
REPO_NAME="${REPO_NAME:-r1-agent}"

create_trigger() {
  local name="$1" branch="$2" env="$3"
  echo
  echo "==> trigger ${name} (branch=${branch}, env=${env})"
  if gcloud builds triggers describe "$name" --project="$PROJECT" >/dev/null 2>&1; then
    echo "   trigger ${name} already exists; updating"
    gcloud builds triggers delete "$name" --project="$PROJECT" --quiet >/dev/null
  fi
  gcloud builds triggers create github \
    --name="$name" \
    --project="$PROJECT" \
    --repo-owner="$REPO_OWNER" \
    --repo-name="$REPO_NAME" \
    --branch-pattern="^${branch}\$" \
    --build-config=services/cloudbuild-deploy.yaml \
    --substitutions="_ENV=${env}" \
    --description="r1 SaaS deploy (${env}) — auto-build + deploy r1-coord-api / r1-docs / r1-downloads-cdn on push to ${branch}"
  echo "   trigger ${name} created"
}

create_trigger r1-services-prod-deploy main prod
create_trigger r1-services-staging-deploy staging staging
create_trigger r1-services-dev-deploy dev dev

echo
echo "==> done. r1-services triggers:"
gcloud builds triggers list --filter="name~r1-services" --format="table(name,github.branch:label=BRANCH,filename)" --project="$PROJECT"
