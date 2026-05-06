# `scripts/` — operational scripts

Idempotent setup + maintenance scripts for r1-agent's deployment +
CI infrastructure.

| Script | Purpose | When to run |
|---|---|---|
| `vendor-ui.sh` | (Re-)fetch the pinned frontend vendor blobs (htmx, three.js, d3-force-3d, ...) into `cmd/r1-server/ui/web/vendor/` and verify each blob's SRI. | Every time a vendored library is bumped. |
| `setup-branch-protection.sh` | Apply branch-protection rules to main + staging + dev — required status checks, required reviewers, force-push prevention. | Once per repo + when protection policy changes. |
| `setup-cloudbuild-triggers.sh` | Create / update the main Cloud Build triggers — the per-PR `r1-agent-pr` check + the per-push CI lane. | Once per project + when trigger config changes. |
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
