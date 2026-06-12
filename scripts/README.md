# `scripts/` — operational scripts

Idempotent setup + maintenance scripts for r1-agent's deployment +
CI infrastructure.

| Script | Purpose | When to run |
|---|---|---|
| `vendor-ui.sh` | (Re-)fetch the pinned frontend vendor blobs (htmx, three.js, d3-force-3d, ...) into `cmd/r1-server/ui/vendor/` and verify each blob's SRI. | Every time a vendored library is bumped. |
| `setup-branch-protection.sh` | Apply branch-protection rules to main + staging + dev — required status checks, required reviewers, force-push prevention. | Once per repo + when protection policy changes. |
| `setup-cloudbuild-base-ci-triggers.sh` | Provision the base `r1-agent-ci` push trigger and `r1-agent-pr` PR trigger, and remove the stray `tmp` deploy trigger if it still exists. | Once per project + whenever the base Cloud Build CI trigger config changes. |
| `setup-bench-truthful-completion-cron.sh` | Provision the TruthfulCompletion reports bucket, monthly manual trigger, PR mini-run trigger, and monthly scheduler job. | Once per project + whenever benchmark trigger config changes. |
| `promote-r1.sh` | Confirm live `dev`/`staging`/`prod` versions against branch tips, then open the gated `dev -> staging` or `staging -> main` promotion PR. | When promoting hosted SaaS changes through the environment ladder. |
| `setup-cloudbuild-triggers.sh` | Create / update the three hosted SaaS deploy triggers for `dev`, `staging`, and `prod`. | Once per project + when deploy trigger config changes. |
| `setup-cloudbuild-e2e-trigger.sh` | Create / update the release-rehearsal E2E triggers — fires `services/cloudbuild-e2e.yaml` on push-to-main + tag-push. | Once per project; rerun when `services/cloudbuild-e2e-trigger.yaml` changes. Spec: `release-rehearsal-ci.md`. |
| `deploy.sh` | One-shot prod deploy script (build → push → roll). | Manual deploys only; CI handles the automated path. |

Every script is **idempotent**: rerunning is safe, never duplicates,
and surfaces a clear error if the caller lacks the required IAM role
or required env var. Each script's first 20 lines documents what
permissions / preconditions it needs.

## Standard run pattern

```bash
# All scripts run from repo root:
bash scripts/<script>.sh

# Specific to GCP-touching scripts: requires gcloud + active
# auth that has roles/cloudbuild.builds.editor on relayone-488319
gcloud auth application-default login   # one-time
bash scripts/setup-cloudbuild-e2e-trigger.sh
```
