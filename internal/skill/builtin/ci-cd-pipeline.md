# ci-cd-pipeline

> CI/CD pipeline design patterns covering build optimization, testing, deployment, and security scanning.

<!-- keywords: ci/cd, github actions, pipeline, deployment, release -->

## GitHub Actions Workflow Patterns

1. Separate workflows by trigger: `ci.yml` (on push/PR), `deploy.yml` (on release/manual), `scheduled.yml` (cron).
2. Use reusable workflows for shared logic: `uses: ./.github/workflows/test.yml` with input parameters.
3. Pin action versions by SHA, not tag: `uses: actions/checkout@a5ac7e51b41094c92402da3b24376905380afc29` to prevent supply chain attacks.
4. Use `concurrency` to cancel redundant runs on the same branch:
```yaml
concurrency:
  group: ${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: true
```
5. Gate deployments with `environment` and required reviewers for production.
6. Use `workflow_dispatch` with input parameters for manual operations (rollback, data migration).

## Build Caching Strategies

1. **Dependency cache**: cache `node_modules`, `~/.cache/go-build`, `~/.m2/repository` using `actions/cache`.
2. **Docker layer cache**: use `docker/build-push-action` with `cache-from: type=gha` and `cache-to: type=gha,mode=max`.
3. **Turbo/Nx remote cache**: for monorepos, share build artifacts across PRs and CI runs.
4. Cache key should include lockfile hash: `key: deps-${{ hashFiles('go.sum') }}`.
5. Use `restore-keys` for partial cache hits when the lockfile changes slightly.
6. Measure cache hit rate and build times weekly. Cache that does not save time wastes storage.

## Test Parallelization

1. Split tests across runners using test-splitting tools: `gotestsum --junitfile` with `split-tests` action.
2. Use matrix strategy for multi-axis testing (OS, language version, database version).
3. Run unit tests first (fast feedback), integration tests in parallel, E2E tests last.
4. Flaky test quarantine: move intermittently failing tests to a separate non-blocking job. Track and fix weekly.
5. Use `--fail-fast` in matrix builds during development, full matrix on main branch.
6. Target total CI time under 10 minutes. If longer, identify bottlenecks with timing reports.

## Environment Promotion

1. **Dev**: deploy on every push to feature branches. Ephemeral environments per PR (preview deploys).
2. **Staging**: deploy on merge to main. Mirrors production infrastructure. Run full E2E suite here.
3. **Production**: deploy on release tag or manual approval. Never auto-deploy to prod from main without a gate.
4. Same artifact through all environments -- only configuration changes (env vars, secrets).
5. Use immutable container image tags: `v1.2.3` or `sha-abc123`, never `latest` in staging or prod.
6. Database migrations run as a separate step before application deployment. Backward-compatible migrations only.
7. Smoke tests run automatically post-deploy: health check, critical API endpoint, key user flow.

## Feature Flags for Progressive Rollout

1. Use a feature flag service (LaunchDarkly, Unleash, Flipt) rather than config files or env vars.
2. Rollout sequence: internal users (1%) -> beta users (10%) -> general (50%) -> full (100%).
3. Decouple deployment from release: deploy dark code behind flags, enable when ready.
4. Flag types: **release** (temporary, remove after rollout), **ops** (kill switch), **experiment** (A/B test).
5. Set flag defaults to "off" so new deployments are safe without flag service connectivity.
6. Clean up stale flags quarterly. Dead flags are tech debt that confuses future developers.

## Rollback Strategies

1. **Container rollback**: `kubectl rollout undo deployment/my-app` or redeploy previous image tag.
2. **Database rollback**: every migration must have a tested down migration. Practice rollback in staging.
3. **Feature flag rollback**: disable the flag instantly without redeployment. Fastest rollback mechanism.
4. Keep the last 5 release artifacts available for immediate rollback (container registry retention policy).
5. Automate rollback triggers: if error rate > 5% within 5 minutes post-deploy, auto-revert.
6. Post-rollback: create an incident ticket, add regression test, do a blameless retrospective.

## Artifact Management

1. Container images: use a private registry (ECR, GCR, GitHub Container Registry). Tag with git SHA and semver.
2. Retention policy: keep last 30 days of images, plus all semver-tagged releases indefinitely.
3. Sign images with cosign/Sigstore. Verify signatures before deployment.
4. Generate and store SBOM (Software Bill of Materials) alongside each artifact.
5. Binary artifacts: publish to GitHub Releases with checksums. Use `goreleaser` or `semantic-release` for automation.

## Security Scanning in Pipeline

1. **SAST** (Static Analysis): run on every PR. Tools: `semgrep`, `gosec`, `eslint-plugin-security`.
2. **Dependency audit**: `go mod verify`, `npm audit`, `trivy fs .` on every PR. Block on critical/high CVEs.
3. **Container scanning**: `trivy image` on built images before pushing to registry.
4. **DAST** (Dynamic Analysis): run against staging after deploy. Tools: OWASP ZAP, Nuclei.
5. **Secret detection**: `gitleaks` or `trufflehog` as a pre-commit hook and in CI. Block PRs with detected secrets.
6. **License compliance**: scan dependencies for copyleft licenses incompatible with your distribution model.
7. Create a security dashboard aggregating findings across all scanners. Triage weekly, SLA by severity:
   - Critical: fix within 24 hours
   - High: fix within 7 days
   - Medium: fix within 30 days
   - Low: track, fix when convenient
