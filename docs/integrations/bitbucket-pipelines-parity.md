# BitBucket Pipelines Adapter — Parity Contract (C5, T1 + T23)

The table below is the **contract** that the parity-audit test (T23) reads.
Each row is one capability of the CI/CD adapter surface; the BitBucket
adapter must expose an equivalent symbol for every row marked **Required**.

> The audit reads the public-symbol surface of `internal/cicd/github/`,
> `internal/cicd/gitlab/`, and `internal/cicd/bitbucket/` via `go/parser`
> and fails the build if a Required row lacks a BitBucket entry. Add new
> capabilities here and to `parityContract` in
> `internal/cicd/parity_test.go` in the same change.

| Capability                                       | GH Symbol                                        | GitLab Symbol         | BB Symbol                                  | Required | Note |
|--------------------------------------------------|--------------------------------------------------|-----------------------|--------------------------------------------|----------|------|
| Construct REST client                            | `New`                                            | `New`                 | `New`                                      | Yes      | Three-arg `Config{}` constructor. |
| Trigger pipeline / workflow                      | `TriggerWorkflow`                                | `TriggerPipeline`     | `TriggerPipeline`                          | Yes      | Plus `TriggerCustomPipeline` for the workflow_dispatch analogue. |
| Get pipeline / run status                        | `GetRunStatus`                                   | `GetPipelineStatus`   | `GetPipelineStatus`                        | Yes      |  |
| Block until terminal state                       | `WaitForCompletion`                              | `WaitForCompletion`   | `WaitForCompletion`                        | Yes      | Same poll-loop pattern in all three. |
| Fetch per-step / per-job log                     | `GetJobLogs`                                     | `GetJobLog`           | `GetStepLog`                               | Yes      | BB names them "steps"; semantically identical. |
| List jobs / steps in a pipeline                  | `ListPullRequestFiles` (per-file proxy)          | `ListPipelineJobs`    | `ListPipelineSteps`                        | Yes      |  |
| Fetch PR diff for code review                    | `GetPullRequestDiff`                             | N/A                   | `GetPullRequestDiff`                       | Yes (GH) |  |
| Fetch PR head commit                             | `GetPullRequestHeadSHA`                          | N/A                   | `GetPullRequestHead`                       | Yes (GH) | Needed for commit-status anchoring. |
| Post inline / line-anchored PR comment           | `PostReviewComment`, `PostReviewCommentDirect`   | N/A                   | `PostInlinePRComment`                      | Yes (GH) | BB adds `PostPRComment` for unanchored summaries. |
| Auto-review pipeline (LLM → findings → comments) | `NewReviewer`, `Reviewer`                        | N/A                   | `NewReviewer`, `Reviewer`                  | Yes (GH) | All three share `internal/cicd/shared` primitives. |
| Commit status row writer                         | N/A (workflow writes status itself)              | N/A                   | `PostCommitStatus`                         | Yes (BB) | BB's status row drives the PR green/red icon. |
| 404 error classifier                             | `IsNotFound`                                     | `IsNotFound`          | `IsNotFound`                               | Yes      |  |
| 401 error classifier                             | `IsUnauthorized`                                 | N/A                   | `IsUnauthorized`                           | Yes (GH) |  |
| Default base URL constant                        | `DefaultBaseURL`                                 | `DefaultBaseURL`      | `DefaultBaseURL`                           | Yes      |  |
| `Config` type                                    | `Config`                                         | `Config`              | `Config`                                   | Yes      |  |
| `Client` type                                    | `Client`                                         | `Client`              | `Client`                                   | Yes      |  |
| `APIError` structured error                      | N/A (uses go-github's `*ErrorResponse`)          | `APIError`            | `APIError`                                 | Yes (GL) |  |

## Shared primitives (`internal/cicd/shared/`)

Promoted from `internal/cicd/github/reviewer.go` so all three adapters
consume one implementation:

| Primitive             | Purpose                                                    |
|-----------------------|------------------------------------------------------------|
| `Finding`             | Path + line + severity + body — the LLM's review output.   |
| `LLMFunc`             | `(ctx, prompt) → (response, err)` — adapter-neutral.       |
| `ParserFunc`          | `(response) → []Finding` — pluggable parser.               |
| `ParseFindings`       | Default markdown-bullet parser.                            |
| `RenderCommentBody`   | Deterministic comment-body formatter.                      |
| `DefaultReviewPrompt` | Default prompt template with `{{DIFF}}` substitution.      |
| `CommitStatusName`    | `"R1 Verify"` — the byte-for-byte status-row label.        |
| `RenderPrompt`        | Helper that substitutes `{{DIFF}}` (default-fallback).     |

## Modes (`internal/cicd.Mode`)

Every adapter generates all three modes:

| Mode      | BitBucket trigger                          | Equivalent GH trigger     | Equivalent GitLab trigger |
|-----------|--------------------------------------------|---------------------------|---------------------------|
| `review`  | `pipelines.pull-requests.'**'`             | `on: pull_request`        | `merge_request_event`     |
| `autofix` | `pipelines.branches.{main}` + PR           | `on: push` + PR           | `push` + `merge_request_event` |
| `mission` | `pipelines.custom.r1-mission`              | `on: workflow_dispatch`   | `web` trigger             |

## Audit / drift policy

When the GH or GitLab adapter exports a new capability, this doc and the
`parityContract` slice in `internal/cicd/parity_test.go` are updated in the
same PR. The test fails closed: a Required row with no BB entry blocks the
build.
