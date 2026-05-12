# C5 — BitBucket Pipelines Adapter — Build Report

**Spec:** [`specs/bitbucket-pipelines-adapter.md`](../specs/bitbucket-pipelines-adapter.md)
**Status:** built 2026-05-12 on branch `build/bitbucket-pipelines-adapter` (parent: `dev`).

## CI gate

| Gate | Result | Notes |
|---|---|---|
| `go build ./...` | PASS (exit 0) | Full repo build clean. |
| `go vet ./...` | PASS (exit 0) | No vet findings. |
| `go test -count=1 -short ./internal/cicd/...` | PASS — 5 packages, 67 tests | `cicd`, `cicd/bitbucket`, `cicd/github`, `cicd/gitlab`, `cicd/shared`. |
| `go test -count=1 -short ./cmd/r1/...` | 1 PRE-EXISTING FAIL (`TestServeSmoke`) | See below. |

## Parity audit (T23)

`TestAdapterParity` — PASS. Walks the public-symbol surface of
`internal/cicd/github`, `internal/cicd/gitlab`, `internal/cicd/bitbucket`
via `go/parser`. Every Required row in
`docs/integrations/bitbucket-pipelines-parity.md` has a same-purposed
BitBucket symbol. No drift.

`TestParityDocPresent` — PASS. Asserts the parity contract markdown is
present and references the required symbols (TriggerPipeline,
GetPipelineStatus, PostCommitStatus).

## Pre-existing failure — TestServeSmoke (cmd/r1)

```
--- FAIL: TestServeSmoke (0.05s)
    serve_smoke_test.go:49: GET /: response carries no CSP, neither via header nor <meta http-equiv>
```

**Verified pre-existing** by `git stash` + re-run on the worktree baseline
(commit `473d36d5`, before any C5 files were tracked): the test still
fails identically. The failure is in the `r1 serve` HTTP layer, unrelated
to C5 (the spec touches `internal/cicd/` + `cmd/r1/cicd_cmd.go` only).

**Finding for triage:** TestServeSmoke checks `GET /` for a CSP header
(via response headers or `<meta http-equiv="Content-Security-Policy">`).
Neither is present in the response served by the smoke test's
`r1 serve --port 0`. Likely a recent change to the serve handler dropped
the CSP injection step. Not in C5's scope; surfaced for the operator.

## Files touched / created (C5)

### Created
- `internal/cicd/cicd_bitbucket.go` — template renderer (T3-T7, T17, T19).
- `internal/cicd/cicd_bitbucket_test.go` — renderer unit tests (T21).
- `internal/cicd/parity_test.go` — parity-audit test (T23).
- `internal/cicd/shared/review.go` — promoted Finding/ParseFindings/LLMFunc etc. (T11).
- `internal/cicd/shared/review_test.go` — shared-primitive tests.
- `internal/cicd/bitbucket/bitbucket.go` — REST client (T8, T9, T15).
- `internal/cicd/bitbucket/types.go` — JSON shapes.
- `internal/cicd/bitbucket/auth.go` — OIDC exchange (T10).
- `internal/cicd/bitbucket/comment.go` — PR comments (T11).
- `internal/cicd/bitbucket/reviewer.go` — auto-review pipeline (T12).
- `internal/cicd/bitbucket/artifact.go` — artifact helpers (T13).
- `internal/cicd/bitbucket/runner.go` — in-step R1 runner (T14).
- `internal/cicd/bitbucket/bitbucket_test.go` + `reviewer_test.go` +
  `auth_test.go` + `runner_test.go` — unit tests (T21).
- `internal/cicd/bitbucket/integration_test.go` — `//go:build bitbucket_integration` integration tests (T22).
- `internal/cicd/bitbucket/templates/{go,node,python,generic}.yaml` — embedded language overlays (T17).
- `docs/integrations/bitbucket-pipelines.md` — operator runbook (T24).
- `docs/integrations/bitbucket-pipelines-parity.md` — parity contract (T1).

### Modified
- `internal/cicd/cicd.go` — `ProviderBitbucket` const, `AllProviders` extension, `GenerateConfig` + `ValidateConfig` dispatch, `nodeLabel` BB default (T2, T5).
- `internal/cicd/cicd_test.go` — `TestUnsupportedProvider` now uses `"jenkins"` (T2).
- `internal/cicd/github/reviewer.go` — re-exports `Finding`/`ParseFindings`/`RenderCommentBody`/`LLMFunc`/`ParserFunc`/`DefaultReviewPrompt` from `internal/cicd/shared` (T11).
- `cmd/r1/cicd_cmd.go` — help text, `init bitbucket` subcommand surface (T16, T25).
- `docs/FEATURE-MAP.md` — C5 row marked Done.
- `docs/BUSINESS-VALUE.md` — C5 marked shipped.
- `docs/ARCHITECTURE.md` — `internal/cicd/bitbucket/` description updated to "shipped 2026-05-12".
- `README.md` — C5 entry marked shipped.
- `specs/bitbucket-pipelines-adapter.md` — frontmatter `STATUS: done`, `BUILD_COMPLETED: 2026-05-12`.

## Acceptance criteria roll-up

| # | Criterion | Result |
|---|---|---|
| 1 | `r1 cicd --provider bitbucket --mode review` writes valid YAML; ValidateConfig clean | PASS (`TestGenerateBitbucket_Review` + ValidateConfig) |
| 2 | All three modes render | PASS (`TestAllModesAllProviders` + `TestGenerateBitbucket_*`) |
| 3 | YAML uses `oidc: true` and references `BITBUCKET_STEP_OIDC_TOKEN` | PASS (`TestBitbucketRenderedContainsOIDC`) |
| 4 | Parity-audit test passes | PASS (`TestAdapterParity`) |
| 5 | Integration test (gated by build tag + env) authored | PASS (build-tagged, skips on missing env) |
| 6 | `R1 Verify` status name byte-for-byte parity | PASS (`TestCommitStatusNameStable` + `TestPostCommitStatusSendsName`) |
| 7 | No long-lived `$BITBUCKET_API_TOKEN` step-level var | PASS (`TestBitbucketNoLongLivedTokenInStepVars`) |
| 8 | Shared primitives in `internal/cicd/shared/` | PASS (visible from both GH and BB adapter via re-exports / direct import) |
| 9 | Operator docs exist with all sections | PASS (`docs/integrations/bitbucket-pipelines.md` shipped) |
| 10 | Parity matrix has zero "TBD" / "Missing" rows | PASS (manual review of parity doc + audit test) |
