<!-- STATUS: ready -->
<!-- CREATED: 2026-05-05 -->
<!-- DEPENDS_ON: -->
<!-- BUILD_ORDER: 38 -->

# Release-rehearsal CI lane (Playwright + axe-core E2E)

## 1. Overview

Spec 5 §6 + #167 shipped `cmd/r1-server/e2e/` as a separate Go submodule that runs a full Playwright + axe-core E2E flow when invoked with `go test -tags=e2e`. The corresponding Cloud Build YAML at `services/cloudbuild-e2e.yaml` declares the steps but **the trigger that actually fires it has never been wired** — running it requires `gcloud builds submit --config=services/cloudbuild-e2e.yaml .` from a developer machine with GCP credentials.

This spec wires the trigger so the E2E lane runs automatically on:
1. Every push to `main` (post-deploy verification).
2. Every git tag matching `v*.*.*` (release-rehearsal gate).
3. Manual `gh workflow run release-rehearsal` invocation.

## 2. Stack & Versions

- Existing `services/cloudbuild-e2e.yaml`
- Cloud Build trigger created via `gcloud builds triggers create`
- Optional GitHub Actions wrapper for the manual-run UX
- Existing Playwright + chromium install in node:22.13 container

## 3. Architecture

```
push to main          ── Cloud Build trigger ─→ services/cloudbuild-e2e.yaml
                                                  ├─ build r1-server
                                                  ├─ npm install + playwright install
                                                  └─ go test -tags=e2e ./cmd/r1-server/e2e/...

tag push v*.*.*       ── Cloud Build trigger ─→ same yaml + extra release-artifacts step

manual via gh action  ── GitHub Actions ──────→ POST to Cloud Build trigger via gcloud
```

## 4. Boundaries

- **No new dependency.** Reuses existing Playwright + axe-core in `web/`.
- **No PR-time E2E.** Adds 5+ minutes per build; gated to post-merge / pre-release only.
- **No flake-rerun policy in this spec.** If the E2E is flaky we'll address in a separate spec; for now red = blocked release.
- **Trigger creation is one-time + scripted, not interactive.** Per the operator-rules memory: deploy/trigger setup goes through `gcloud builds triggers import` from a YAML descriptor.

## 5. Implementation checklist (5 items — self-contained)

- [ ] T1 — Write `services/cloudbuild-e2e-trigger.yaml` (the trigger descriptor, distinct from the build descriptor). Fields: `name: r1-agent-e2e-rehearsal`, `github { owner: RelayOne, name: r1-agent, push { branch: ^main$ } }`, `filename: services/cloudbuild-e2e.yaml`, `serviceAccount: projects/relayone-488319/serviceAccounts/cloud-build-byosa@relayone-488319.iam.gserviceaccount.com` (BYOSA — same SA the existing main triggers use). Add a second trigger object in the same file with `tag: ^v.*$`.
- [ ] T2 — Add a script `scripts/setup-cloudbuild-e2e-trigger.sh` that runs `gcloud builds triggers import --source=services/cloudbuild-e2e-trigger.yaml --project=relayone-488319`. The script is idempotent (re-running updates instead of duplicating). Document it in `scripts/README.md` next to `setup-cloudbuild-triggers.sh`.
- [ ] T3 — Add a GitHub Actions workflow at `.github/workflows/e2e-rehearsal-manual.yml` with `workflow_dispatch` trigger. Steps: (a) `gcloud auth activate-service-account --key-file=$GCP_SA_JSON` (secret), (b) `gcloud builds triggers run r1-agent-e2e-rehearsal --branch=main --project=relayone-488319`. Lets operators kick off a manual rehearsal from the GitHub UI without local gcloud.
- [ ] T4 — Update `services/cloudbuild-e2e.yaml`:
    * Add `substitutions: _RELEASE_TAG: ""` so tag-triggered builds can stamp release artifacts.
    * Add a final `publish-rehearsal-result` step that posts a GitHub commit status check via the Checks API: green if e2e passes, red otherwise. Service account needs `repo:status:write` — verify the existing BYOSA already has it; if not, add a separate token via the Cloud Build secret manager.
- [ ] T5 — Document the lane in `docs/DEPLOYMENT.md` § "Release rehearsal":
    * What the lane runs (full E2E + axe).
    * When it runs (push-to-main + tag + manual).
    * What red means (= blocked release; investigate before tagging).
    * How to run locally (the existing `cd cmd/r1-server/e2e && go test -tags=e2e ./...` recipe).
    * Add a row to `docs/FEATURE-MAP.md` under CI infrastructure.

## 6. Acceptance

- `gcloud builds triggers list --project=relayone-488319` includes `r1-agent-e2e-rehearsal`.
- Push a no-op commit to main; the trigger fires; the build completes (or fails honestly with a real test result).
- `gh workflow run e2e-rehearsal-manual` from the GitHub CLI succeeds and the trigger fires from the API.
- A red E2E result posts a red check on the commit + blocks any release that gates on the rehearsal.

**Note on operator authentication:** trigger creation needs `roles/cloudbuild.builds.editor` on the project. If the user running the build script doesn't have it, the script reports the missing role and exits non-zero — operator must escalate before the trigger lands.
