#!/usr/bin/env bash
# scripts/setup-cloudbuild-base-ci-triggers.sh
#
# Provisions the base Cloud Build CI trigger pair for r1-agent:
#   - r1-agent-ci: push to main, runs cloudbuild.yaml
#   - r1-agent-pr: pull requests, runs cloudbuild.yaml
#
# The PR trigger is configured to auto-run for internal contributors and
# require /gcbrun only for external contributors. The script also removes the
# stray legacy trigger `tmp` if it still exists, because that trigger points at
# the deploy pipeline and pollutes PR commits with irrelevant failing checks.
#
# Idempotent — safe to re-run.

set -euo pipefail

PROJECT="${PROJECT:-resolute-parity-484218-g1}"
CONNECTION="${CONNECTION:-resolute-parity-github-conn}"
REPO_OWNER="${REPO_OWNER:-RelayOne}"
REPO_NAME="${REPO_NAME:-r1-agent}"
SERVICE_ACCOUNT="${SERVICE_ACCOUNT:-projects/${PROJECT}/serviceAccounts/claude-eric-agent@${PROJECT}.iam.gserviceaccount.com}"

create_or_replace_push_trigger() {
  local name="$1" branch_pattern="$2" description="$3"
  echo
  echo "==> trigger ${name}"
  if gcloud builds triggers describe "$name" --project="$PROJECT" >/dev/null 2>&1; then
    echo "   trigger ${name} already exists; replacing"
    gcloud builds triggers delete "$name" --project="$PROJECT" --quiet >/dev/null
  fi
  gcloud alpha builds triggers create github \
    --name="$name" \
    --project="$PROJECT" \
    --service-account="$SERVICE_ACCOUNT" \
    --repo-owner="$REPO_OWNER" \
    --repo-name="$REPO_NAME" \
    --branch-pattern="$branch_pattern" \
    --build-config=cloudbuild.yaml \
    --description="$description"
  echo "   trigger ${name} created"
}

create_or_replace_pr_trigger() {
  local name="$1" base_pattern="$2" description="$3"
  echo
  echo "==> trigger ${name}"
  if gcloud builds triggers describe "$name" --project="$PROJECT" >/dev/null 2>&1; then
    echo "   trigger ${name} already exists; replacing"
    gcloud builds triggers delete "$name" --project="$PROJECT" --quiet >/dev/null
  fi
  gcloud alpha builds triggers create github \
    --name="$name" \
    --project="$PROJECT" \
    --service-account="$SERVICE_ACCOUNT" \
    --repo-owner="$REPO_OWNER" \
    --repo-name="$REPO_NAME" \
    --pull-request-pattern="$base_pattern" \
    --comment-control=COMMENTS_ENABLED_FOR_EXTERNAL_CONTRIBUTORS_ONLY \
    --build-config=cloudbuild.yaml \
    --description="$description"
  echo "   trigger ${name} created"
}

remove_tmp_trigger_if_present() {
  echo
  echo "==> removing stray tmp trigger if present"
  local deleted=0
  if gcloud builds triggers describe tmp --project="$PROJECT" --region=us-central1 >/dev/null 2>&1; then
    gcloud builds triggers delete tmp --project="$PROJECT" --region=us-central1 --quiet >/dev/null
    echo "   deleted regional tmp (us-central1)"
    deleted=1
  fi
  if gcloud builds triggers describe tmp --project="$PROJECT" >/dev/null 2>&1; then
    gcloud builds triggers delete tmp --project="$PROJECT" --quiet >/dev/null
    echo "   deleted global tmp"
    deleted=1
  fi
  if [[ "$deleted" -eq 0 ]]; then
    echo "   tmp not present"
  fi
}

create_or_replace_push_trigger \
  r1-agent-ci \
  '^main$' \
  'r1-agent CI on push to main (F-N+41-2)'

create_or_replace_pr_trigger \
  r1-agent-pr \
  '^.*$' \
  'r1-agent CI on PR (F-N+41-2)'

remove_tmp_trigger_if_present

echo
echo "==> done. verify with:"
echo "   gcloud builds triggers list --project=$PROJECT --filter='name=r1-agent-ci OR name=r1-agent-pr OR name=tmp'"
