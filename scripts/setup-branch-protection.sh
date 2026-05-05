#!/usr/bin/env bash
# scripts/setup-branch-protection.sh — env branch creation + protection.
#
# Run AFTER the current PR (claude/w521-...) merges to main.
#
# Creates `staging` and `dev` from the merge commit, then applies
# branch protection to all three (main, staging, dev) per the
# standing GCP rules:
#   - main:    no direct commits, requires PR from staging, status checks, signed commits OK
#   - staging: no direct commits, requires PR from dev, status checks
#   - dev:     allows direct commits for minor fixes; large changes via PR from feature branch
#
# Idempotent — safe to re-run.
set -euo pipefail

REPO="${REPO:-RelayOne/r1-agent}"
MAIN_TIP="$(gh api "repos/${REPO}/branches/main" --jq '.commit.sha')"

echo "==> repo=${REPO}  main_tip=${MAIN_TIP}"

# 1. Create staging + dev branches from current main.
for ENV in staging dev; do
  echo
  echo "==> ensure branch ${ENV}"
  if gh api "repos/${REPO}/branches/${ENV}" >/dev/null 2>&1; then
    echo "   ${ENV} already exists; leaving alone (don't overwrite)"
  else
    gh api "repos/${REPO}/git/refs" -f "ref=refs/heads/${ENV}" -f "sha=${MAIN_TIP}" >/dev/null
    echo "   created ${ENV} at ${MAIN_TIP}"
  fi
done

# 2. Branch protection — strict for main + staging, permissive for dev.
apply_protection() {
  local branch="$1" reviewers="$2" allow_direct="$3"
  echo
  echo "==> protect ${branch} (reviewers=${reviewers}, allow_direct=${allow_direct})"

  # GitHub Branches API uses PUT with a JSON body; build it.
  local body
  # Required check is the Cloud Build PR-runner — the only context that
  # reports on every PR (push-to-main has different check names like
  # "r1-agent-ci" / "r1-agent-binaries", but those don't run on PRs and
  # therefore can't gate merges via branch protection).
  local checks='["r1-agent-pr (relayone-488319)"]'
  if [[ "$allow_direct" == "true" ]]; then
    # dev: looser — status checks only, no required reviews
    body=$(jq -n --argjson checks "$checks" '{
      required_status_checks: {
        strict: true,
        contexts: $checks
      },
      enforce_admins: false,
      required_pull_request_reviews: null,
      restrictions: null,
      allow_force_pushes: false,
      allow_deletions: false
    }')
  else
    # main + staging: PR-only with reviewer enforcement
    body=$(jq -n --argjson reviewers "$reviewers" --argjson checks "$checks" '{
      required_status_checks: {
        strict: true,
        contexts: $checks
      },
      enforce_admins: false,
      required_pull_request_reviews: {
        required_approving_review_count: $reviewers,
        dismiss_stale_reviews: true,
        require_code_owner_reviews: false
      },
      restrictions: null,
      allow_force_pushes: false,
      allow_deletions: false
    }')
  fi

  echo "$body" | gh api -X PUT "repos/${REPO}/branches/${branch}/protection" --input - >/dev/null
  echo "   protection applied to ${branch}"
}

apply_protection main 1 false
apply_protection staging 1 false
apply_protection dev 0 true

echo
echo "==> done. Branch state:"
gh api "repos/${REPO}/branches" --jq '.[].name' | sort
