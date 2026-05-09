# scan-ci-infra — CI / Infrastructure / Deployment audit

Date: 2026-05-08
Scope: `.github/workflows/`, `cloudbuild*.yaml`, `Dockerfile*`, `scripts/`,
`.claude/scripts/`, `services/*.yaml`, `services/*.sh`, root config files.

Severity rubric:
- **HIGH**: hardcoded secret, missing test gate on a release path, broken
  e2e layer, missing rollback for prod deploy.
- **MED**: hardcoded sleep, missing health-check, manual trigger that could
  be auto-fired, version drift between configs, missing lint in a release
  workflow.
- **LOW**: doc/style/nit, or trivially fixable comment-only issues.

No literal secret values were observed. All `secrets.*` references resolve
to GitHub Actions secret stores; no `sk-…` / `Bearer …` tokens are baked
into committed YAML. GCP project-id `relayone-488319` is by design (per
the deployment rules) and is **not** treated as a secret here.

## Findings

| File:Line | Severity | Category | Issue | Effort |
|---|---|---|---|---|
| desktop/tests/e2e/helpers/tauri-driver-session.ts:107-122 | HIGH | broken-e2e | `driverRequest` POSTs to `http://127.0.0.1:$port/click`, `/waitForEvent`, `/testState` — these are **not** WebDriver protocol endpoints. Real `tauri-driver` exposes `/session/{id}/element/{id}/click` etc. The shim cannot drive the real binary; every e2e spec under `desktop/tests/e2e/*.spec.ts` fails fast against a live driver. The whole layer-2/3 suite is a stub. | L |
| desktop/tests/e2e/helpers/tauri-driver-session.ts:39-47 | HIGH | broken-e2e | `spawn(binaryPath, [], …)` launches the desktop binary directly with no driver attach handshake, and never opens a WebDriver session via `POST /session`. State machine is missing. | L |
| .github/workflows/desktop-augmentation.yml:146 | HIGH | broken-e2e | `cargo install tauri-driver --locked` installs the daemon, but no platform-specific WebDriver backend is installed — Linux needs `webkit2gtk-driver` (apt: `webkit2gtk-driver`), macOS needs `safaridriver` enabled, Windows needs `msedgedriver`. `tauri-driver` proxies to one of those; without them the e2e job's HTTP requests on port 4444 will return `unable to connect to chromedriver/msedgedriver` even if the shim were correct. | M |
| .github/workflows/desktop-augmentation.yml:172-176 | MED | hardcoded-sleep | `tauri-driver --port=$R1_TAURI_DRIVER_PORT &` then `sleep 2`. Should poll `curl -fs http://127.0.0.1:$R1_TAURI_DRIVER_PORT/status` until 200. 2-second hard wait causes flakes when the runner is loaded. | S |
| .github/workflows/desktop-augmentation.yml:177 | LOW | best-effort-cleanup | `kill $DRIVER_PID || true` swallows errors and won't `wait` on the child — driver process can outlive the job and zombie on self-hosted runners. | S |
| .github/workflows/desktop-augmentation.yml:38-44 | LOW | tech-debt | `RUSTFLAGS: -D warnings` is back on, but the comment admits two modules carry `#![allow(dead_code)]`. Tracking issue not linked from the file. | S |
| .github/workflows/desktop-augmentation.yml:26-31 | MED | scoping-gap | The `pull_request` `paths` filter excludes `services/cloudbuild-deploy.yaml`, `cloudbuild.yaml`, root `package.json` workspace edits — a PR that breaks Go-side desktop interop won't fire this gate. | S |
| .github/workflows/desktop-augmentation.yml:59 | MED | version-drift | Hard-pins `toolchain: '1.95'` here while `cloudbuild.yaml` uses `golang:1.25` and `cloudbuild-binaries.yaml` uses `golang:1.26`. Three different versions across three pipelines. | S |
| .github/workflows/e2e-rehearsal-manual.yml:13-19 | MED | manual-trigger-only | Workflow is `workflow_dispatch:` only. Spec calls this a release-rehearsal lane that should also fire on tag pushes from the GitHub side, but only Cloud Build's `r1-agent-e2e-rehearsal-tag` trigger does that. If the Cloud Build trigger is removed or fails to import, no GitHub-side fallback exists. | M |
| .github/workflows/e2e-rehearsal-manual.yml:43-54 | MED | no-poll | `gcloud builds triggers run … --format='value(metadata.build.id)'` returns and the workflow exits immediately. There is no poll on `gcloud builds describe $BUILD_ID` to surface the actual rehearsal pass/fail in the GHA summary — operators must click through to Cloud Build console. | M |
| cloudbuild.yaml:10-24 | LOW | image-version | `node:22.13-bookworm-slim` is pinned to a patch version while `cloudbuild-e2e.yaml:23` uses the same string — but desktop-augmentation.yml uses `node-version: '22'` (floating). Inconsistent pinning policy. | S |
| cloudbuild.yaml:45-46 | MED | runtime-install | `apt-get update -q && apt-get install -y -q gcc` runs inside the build step every CI run. Repeated three times in this file (build, test, race steps). Should use a custom builder image with gcc baked in or a single setup step. Adds ~10s/step. | M |
| cloudbuild.yaml:83 | MED | test-timeout | `go test … -timeout=120s` for the entire repo (132 packages) is aggressive — flake risk on first-run cache misses. The race test on line 95 uses 600s, but the regular test budget is too tight for race-free coverage of slow packages (e.g. `bench/`, `apiclient/`). | S |
| cloudbuild.yaml:87-97 | MED | test-coverage-gate | No `go test -coverprofile=…` step; nothing enforces a coverage floor on PRs. | M |
| cloudbuild.yaml:117-152 | MED | release-coupling | `build-multi-platform-r1` and `publish-r1-binaries` run on **every push to main**, not just on tag. `gs://relayone-488319-public/r1/latest/*` is overwritten by every commit to main. Means an in-progress release can be partially overwritten by an unrelated merge. | M |
| cloudbuild.yaml:99-115 | LOW | stdout-noise | `antitrunc verify` shells via `go run` every CI run; cold compile cost charged to every PR. Bake the binary in `build` step output and reuse. | S |
| cloudbuild.yaml:154-160 | LOW | logging | `logging: CLOUD_LOGGING_ONLY` — no GCS log archive. Lost after Cloud Logging retention expires. | S |
| cloudbuild-binaries.yaml:5 | MED | version-drift | Uses `golang:1.26` while sibling `cloudbuild.yaml` uses `1.25` and `cloudbuild-release.yaml` uses `1.25`. Build-time toolchain inconsistency. | S |
| cloudbuild-binaries.yaml:1-65 | HIGH | missing-test-gate | This pipeline **builds + publishes** binaries to a public bucket, but does **not** run `go test`, `go vet`, or any lint. If wired to a trigger that fires on a release branch, broken binaries will ship. | M |
| cloudbuild-binaries.yaml:11-16 | LOW | hidden-fallback | `if [ ! -f go.mod ] && [ -f r1-agent/go.mod ]; then ROOT="r1-agent"; fi` — undocumented monorepo fallback. Comment exists but is buried in build script. | S |
| cloudbuild-binaries.yaml:20-25 | MED | stale-replace | `go mod edit -replace …/coderadar/...=../CodeRadar/sdks/go/coderadar` mutates go.mod inside the build container without a `git diff` check; if the replace path doesn't exist, the build silently uses the third_party snapshot. No warning. | S |
| cloudbuild-binaries.yaml:62-65 | LOW | timeout | `timeout: '1800s'` is generous, but no `machineType` override — defaults to E2 standard. | S |
| cloudbuild-release.yaml:32 | HIGH | missing-publish-step | `goreleaser release --skip=publish --clean` skips publishing on every run. The intent is "Cloud Build pushes to GCS instead of GitHub Releases" — but if a real GitHub release is needed, this never publishes one. Combined with `.goreleaser.yml:82-91` which declares a `release: github: …` block, the two configs disagree. | M |
| cloudbuild-release.yaml:6-21 | MED | curl-no-checksum | `curl -sSL …goreleaser…tar.gz \| tar -xz` with no SHA verification. A compromised CDN or MITM could swap the binary. | S |
| cloudbuild-release.yaml:30-31 | MED | duplicated-install | Lines 16-17 install gcc, lines 30-31 install gcc **again** in the next step. Wasted cycle time. | S |
| cloudbuild-release.yaml:56-70 | HIGH | missing-test-gate | `docker build …r1-agent:${TAG}` then `docker push` with no test-image-runs-correctly verification (no `docker run --rm r1-agent:${TAG} --version`). Broken binary can land tagged `:latest`. | M |
| cloudbuild-release.yaml:69-70 | HIGH | latest-tag-race | `:latest` tag pushed from the same step; if two release builds run concurrently, `:latest` racing. | S |
| services/cloudbuild-e2e.yaml:23-33 | MED | runtime-cost | `npx playwright install --with-deps chromium` in node:22 step then re-`apt-get install nodejs npm` in next golang:1.25 step. Chromium binary path likely lost between steps (Cloud Build doesn't share /home between named-image steps). Comment on line 45 acknowledges this — fix is to bake one builder image. | M |
| services/cloudbuild-e2e.yaml:60-75 | HIGH | missing-status-publish | The `publish-rehearsal-result` step is a **placeholder echo** — the comment says "this step is a placeholder" and no actual GitHub commit-status post is wired. The release-blocking gate the trigger advertises does not exist. | M |
| services/cloudbuild-e2e.yaml:50 | MED | tag-only | `go test -tags=e2e -timeout=10m -v ./...` runs the entire repo in e2e mode; only `cmd/r1-server/e2e/...` should be built-tagged. Run-time waste. | S |
| services/cloudbuild-e2e-trigger.yaml:38-42 | LOW | scope | `includedFiles: cmd/r1-server/**, internal/server/**, web/**, services/cloudbuild-e2e.yaml` — desktop changes don't trigger the rehearsal even though they bind to the same release. | S |
| services/cloudbuild-deploy.yaml:55-66 | HIGH | latest-tag-race | Each build tags `:${_ENV}-latest` and pushes; concurrent deploys race. Worse, the deploy step on line 98 uses `:$SHORT_SHA` (immutable, good) but Cloud Run revision will then point at a SHA-tagged image while bystanders read `:prod-latest` to mean "current prod" — diverges. | M |
| services/cloudbuild-deploy.yaml:90-111 | HIGH | no-rollback | `gcloud run deploy r1-coord-api-${_ENV}` rolls 100% traffic to the new revision immediately. No `--no-traffic` step + canary, no `gcloud run services update-traffic --to-revisions=…=10` ramp. If smoke fails on line 207, **the bad revision is already serving 100%.** | M |
| services/cloudbuild-deploy.yaml:182-210 | MED | ineffective-rollback | The smoke step exits 1 on `/livez != 200`, but at that point `gcloud run deploy` already shifted traffic. Build fails red, but no rollback action. Manual `gcloud run services update-traffic` required. | M |
| services/cloudbuild-deploy.yaml:198-204 | MED | hardcoded-sleep | `for i in 1 2 3 4 5; do … curl … --max-time 10 … sleep 2; done` — fixed 5×(10s curl + 2s sleep). Should be exponential backoff and longer; a cold Cloud Run instance can take >10s to warm. | S |
| services/cloudbuild-deploy.yaml:1-218 | HIGH | missing-test-gate | Deploy pipeline rebuilds + ships **with no test step**. Triggered on push to main / staging / dev (per `services/scripts/setup-cloudbuild-triggers.sh:39-41`). Means a bad merge to main goes straight to prod Cloud Run if `cloudbuild.yaml` (the test gate) is on a different trigger and isn't required for the deploy trigger. The two triggers fire independently. | M |
| services/cloudbuild-deploy.yaml:101-108 | LOW | hardcoded-quota | `--max-instances=10 --concurrency=80 --cpu=1 --memory=512Mi` baked in for all 4 services; one-size-fits-all. r1-coord-api needs different resourcing than r1-docs. | S |
| services/cloudbuild-deploy.yaml:179 | LOW | env-derive-fragile | `R1_COORD_API_URL=https://api$${SUB}.r1.run` — relies on bash double-escape. Brittle; one whitespace change breaks prod admin URL. | S |
| services/cloudbuild-bench-nightly.yaml:54-56 | MED | error-swallow | `bench-bin report … || true` — partial-run report. Acceptable if intentional; comment says so. Document better what "partial" means downstream. | S |
| services/deploy.sh:46 | MED | tag-resolve-fragile | `gcloud artifacts docker images list … \| head -1` picks the most recent tag — but if a prerelease was pushed last, deploy.sh ships it. No filter on `:vX.Y.Z` pattern. | S |
| services/deploy.sh:88 | LOW | url-bug-suspect | `https://api.${env/prod/}r1.run` — `${env/prod/}` strips the literal "prod" anywhere in $env. For `env=prod`, this evaluates to `https://api.r1.run` (intended). For `env=staging`, evaluates to `https://api.staging.r1.run` (intended). For `env=dev-prod` (impossible but) would silently strip. Brittle. | S |
| services/deploy.sh:127-138 | MED | smoke-no-rollback | `if curl -sSf … then OK else FAIL` — prints FAIL but continues to next service. No rollback, no exit non-zero on smoke fail; operator must scan output. | S |
| services/deploy.sh:13-14 | LOW | doc-only | Comment lists secret-naming convention; OK. | S |
| services/scripts/setup-cloudbuild-triggers.sh:39-41 | HIGH | no-r1-agent-pr-trigger | Creates `r1-services-{prod,staging,dev}-deploy` triggers, but **does not create** the `r1-agent-pr` trigger that `scripts/setup-branch-protection.sh:45` requires as a status check. The required status check name `r1-agent-pr (relayone-488319)` is referenced but no IaC creates it — it exists (or doesn't) only in the Cloud Build console. PR gate is not config-as-code. | M |
| services/scripts/setup-cloudbuild-triggers.sh:26 | LOW | gcloud-alpha | `gcloud alpha builds triggers create github` — alpha surface; can break without notice. | S |
| services/scripts/setup-bench-cron.sh:40 | LOW | gcloud-alpha | Same issue: `gcloud alpha builds triggers create manual`. | S |
| Dockerfile:4 | MED | version-drift | `golang:1.23-bookworm` while CI uses 1.25; produced binary may differ from CI binary. | S |
| Dockerfile:9-11 | LOW | layer-cache | `COPY go.mod go.sum ./ && go mod download && COPY . .` — standard but `go mod download` will fetch from network even though CI uses `-mod=vendor`. Inconsistent. | S |
| Dockerfile:13 | MED | no-vendor | `go build` without `-mod=vendor` — relies on network module fetch in container builds; cloudbuild.yaml uses `-mod=vendor` everywhere. The image won't build behind a network-isolated runner. | S |
| Dockerfile.pool:36-37 | LOW | hidden-failure | `RUN claude --version \|\| true` — install verification that swallows failure. If `npm install -g @anthropic-ai/claude-code` produced a broken binary, the image still ships. | S |
| Dockerfile.pool:39-40 | LOW | weak-default | `ENTRYPOINT ["/bin/bash", "-c"] / CMD ["echo 'stoke-pool worker ready'"]` — usable, but no health-check directive. | S |
| Dockerfile.pool:7-8 | LOW | dual-tags | Comment lists two image tags; only one is built in the Dockerfile (caller decides). Drift risk. | S |
| .goreleaser.yml:5-9 | MED | duplicate-test-gate | `before.hooks` runs `go vet ./...` + `go test ./... -count=1 -timeout=120s` — but `cloudbuild-release.yaml:32` invokes goreleaser with `--skip=publish` and inherits the same hooks. The 120s timeout repeats from cloudbuild.yaml; if tests are flaky there, they'll be flaky here. Not parallelisable across configs. | S |
| .goreleaser.yml:82-91 | MED | release-target-mismatch | `release.github: owner: RelayOne / name: r1-agent` — but the release pipeline `cloudbuild-release.yaml` skips publish to GitHub. Two configs disagree on whether GitHub releases happen. | S |
| .github/workflows/e2e-rehearsal-manual.yml:34 | LOW | json-credentials | Uses `credentials_json: ${{ secrets.GCP_SA_JSON }}` instead of Workload Identity Federation — the secret is a long-lived service-account key. WIF is the GCP-recommended path; key rotation friction. | M |
| scripts/pre-push.sh:10-12 | LOW | local-only-gate | Pre-push hook runs `go build / vet / test` but **no race**, **no lint**, **no antitrunc**. CI is stricter; PR can pass pre-push and fail CI. | S |
| scripts/lint-lane-events.sh:50-67 | LOW | grep-only-lint | Pure grep against string-literal patterns; semantic bypasses (e.g. constructed from `"lane." + suffix`) are missed. Documented as "false positives tolerated" but false-negatives are silent. | S |
| scripts/install-hooks.sh:38-39 | LOW | no-pre-push-install | `install-hooks.sh` only wires `core.hooksPath = scripts/git-hooks/`. The post-commit-antitrunc hook is the only file in that dir. The richer `scripts/pre-push.sh` is a separate file the operator must `cp` manually (per its line-3 comment). Would benefit from being moved into `scripts/git-hooks/pre-push.sh` so install-hooks wires it too. | S |
| scripts/setup-branch-protection.sh:45-46 | HIGH | gate-mismatch | `required_status_checks.contexts = ["r1-agent-pr (relayone-488319)"]` — the **only** required status check. If `r1-agent-pr` covers go build/vet/test (cloudbuild.yaml), but the PR trigger is misconfigured to point at e.g. `cloudbuild-binaries.yaml` (which has no test step), every PR can merge with no test gate. The contract is config-out-of-band. | M |
| scripts/setup-branch-protection.sh:67-72 | MED | review-count | `required_approving_review_count: 1` for main + staging — for solo-operator project this is ignored via admin override. Not a security concern but documents single-reviewer pattern. | S |
| scripts/setup-cloudbuild-e2e-trigger.sh:38 | LOW | preflight-permissions | Permission preflight via `gcloud builds triggers list` — fine. | S |
| scripts/apply-task-30-claude-md.sh:1-69 | LOW | one-shot | Idempotent but is task-specific; once TASK-30 lands the script is dead code. Should be removed after merge. | S |
| scripts/git-hooks/post-commit-antitrunc.sh:46-51 | LOW | non-blocking-by-design | Hook always exits 0; documented but means a "spec N done" lie commits successfully. The blocking gate is `go run ./cmd/r1 antitrunc verify` in cloudbuild.yaml; gap is local-only. | S |
| scripts/vendor-ui.sh:25-44 | LOW | cdn-dependency | jsdelivr is the only CDN; no fallback. RT-VENDOR-SCRIPT-PATTERNS already documents the jsdelivr-only choice; OK as a documented decision. | S |
| .claude/scripts/codex-execute.sh:43-49 | LOW | timeout-default | `TIMEOUT_SEC="${CODEX_TIMEOUT:-600}"` — 10 minute default for code-modifying agent invocation; fine, comment explains. | S |
| .claude/scripts/codex-execute.sh:62 | LOW | best-effort-stash-pop | `git stash pop -q 2>/dev/null \|\| true` — silently swallows merge-conflict on stash pop; could leave repo in stash-conflicted state. | S |
| .claude/scripts/codex-review.sh:172-175 | MED | hardcoded-sleep | `sleep 5` between retries on empty result; should be exponential. The other branches (line 233) do exponential backoff; this branch does not. Inconsistent. | S |
| .claude/scripts/codex-review.sh:78 | LOW | truncate-loud | `${DIFF_CONTENT:0:30000}` — silent truncation; no warning when a diff is bigger. | S |
| .claude/scripts/codex-review.sh:163 | LOW | jq-dep | `jq -e '.verdict' "$OUTPUT"` — silent failure if jq absent. Pre-flight check is missing. | S |
| .claude/scripts/cross-verify.sh:178 | LOW | error-fail-closed | Unparseable response is a fail (good); but the `:0:200` truncation in the JSON message can produce malformed JSON if the substring contains an unescaped quote. | S |
| .claude/scripts/cross-verify.sh:39 | LOW | empty-skip | `[ -z "$CONTENT" ] && echo '{"pass":true,"reason":"No changes to review"}' && exit 0` — passes when there's nothing to review. Defensible default. | S |
| .claude/scripts/deterministic-scan.sh:9-13 | LOW | grep-only | Same grep-only-lint caveat as `lint-lane-events.sh`; misses semantic patterns. | S |
| .claude/scripts/deterministic-scan.sh:32-41 | LOW | weak-test-rules | `\.toBeUndefined()` etc. flagged as "weak"; reasonable, but test-only filter only on filename pattern — won't catch `*spec.tsx`. | S |
| .claude/scripts/security/scan_config.py:42 | LOW | regex-only | Pure regex secret scan; no entropy detection or git-history scan. Bring trufflehog/gitleaks for higher fidelity. | M |
| .claude/scripts/security/scan_config.py:55-56 | LOW | encoding-fail-silent | `try: lines = open(fp, encoding='utf-8').readlines() except: continue` — bare except + silent continue; non-utf8 file is skipped without log. | S |
| .claude/scripts/security/scan_inputs.py:74-75 | LOW | encoding-fail-silent | Same pattern as above. | S |
| .claude/scripts/security/scan_dataflow.py:60-62 | LOW | encoding-fail-silent | Same pattern. | S |
| .claude/scripts/project-mapper.sh:7-13 | LOW | exclusion | Excludes `vendor/`, `target/`, `.claude/`, `audit/`, `plans/`, `specs/` — fine, but `cmd/r1-server/ui/vendor/` SRI'd blobs are excluded from secrets-scan reach. | S |
| Dockerfile (overall) | MED | no-healthcheck | Neither `Dockerfile` nor `Dockerfile.pool` has `HEALTHCHECK`. r1 runs as a Cloud Run-style HTTP server in some configs; missing healthcheck. | S |
| dependabot.yml | LOW | scope-narrow | Only `tauri-plugin-*` and `@tauri-apps/plugin-*` grouped; root `package-lock.json` and `vendor/` Go modules have no dependabot coverage. | S |

## Top 10 by impact

These are the items where a CI failure (or unflagged green) would
materially affect a real release path.

1. **services/cloudbuild-deploy.yaml:1-218 — deploy pipeline has no
   test gate.** Push to main/staging/dev triggers rebuild + 100%
   traffic shift to Cloud Run. Tests live on a different trigger
   (`cloudbuild.yaml`). Branch protection requires `r1-agent-pr` —
   but see #2.
2. **services/scripts/setup-cloudbuild-triggers.sh — `r1-agent-pr` is
   the required status check, but no IaC creates it.** Trigger config
   exists only in the Cloud Build console. If it's deleted or
   misconfigured, branch protection silently passes every PR.
3. **services/cloudbuild-deploy.yaml:90-111 — no canary / no rollback.**
   Deploy goes 100% to live revision before smoke. Smoke failure
   leaves bad revision serving prod traffic.
4. **desktop/tests/e2e/helpers/tauri-driver-session.ts:107-122 —
   WebDriver shim is a stub.** It POSTs to non-WebDriver endpoints
   (`/click`, `/waitForEvent`, `/testState`); cannot drive real
   `tauri-driver`. The entire layer-2/3 e2e suite cannot run
   end-to-end against the real binary.
5. **.github/workflows/desktop-augmentation.yml:146 — missing
   platform WebDriver backend.** `tauri-driver` is installed but
   `webkit2gtk-driver` (Linux), `safaridriver` (macOS), and
   `msedgedriver` (Windows) are not. Even with #4 fixed, the e2e
   job cannot connect.
6. **services/cloudbuild-e2e.yaml:60-75 — release-rehearsal commit
   status is a placeholder.** Spec advertises this as the release
   gate that posts green/red, but the step is an `echo`. No actual
   commit status posted. Release tooling thinks the gate is wired.
7. **cloudbuild-binaries.yaml — publishes binaries with no test
   gate.** If wired to a release-path trigger, broken binaries
   will land in `gs://relayone-488319-public/r1/latest/`.
8. **cloudbuild-release.yaml:56-70 — docker push `:latest` with
   no smoke test on the resulting image.** `docker run --rm
   r1-agent:${TAG} --version` not invoked. Broken binary can be
   tagged latest.
9. **cloudbuild-release.yaml:69 + services/cloudbuild-deploy.yaml:55-66
   — `:latest` tag race.** Concurrent release/deploy runs will
   corrupt the moving tag. Use immutable SHA tags only.
10. **cloudbuild.yaml:117-152 — release artifacts published on
    every main push, not just on tag.** `gs://…/latest/` is
    overwritten on every commit, so a release in flight can be
    clobbered by an unrelated merge.

## Notes on what is healthy

- No literal hardcoded secrets observed in committed YAML/scripts.
  Project-id `relayone-488319` is the documented prod project.
- `cloudbuild.yaml` does run `go build`, `go vet`, `go test`,
  `go test -race`, `lint-lane-events`, `lint-chdir`, and
  `antitrunc verify` — strong primary CI gate.
- `desktop-augmentation.yml` runs cargo build + test + vitest +
  components — strong desktop unit/integration gate. Only the
  e2e job (after the three above succeed) is broken.
- `services/cloudbuild-deploy.yaml:182-210` does include a smoke
  curl on `/livez` for all four services after deploy — health-
  check exists, but it's reactive, not gating.
- Branch protection covers main + staging + dev with PR review +
  status check (item 2 caveat).
- Pre-push hook + post-commit antitrunc hook are wired via
  install-hooks.sh.
