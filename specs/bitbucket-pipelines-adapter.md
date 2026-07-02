<!-- STATUS: done -->
<!-- CREATED: 2026-05-11 -->
<!-- BUILD_COMPLETED: 2026-05-12 -->
<!-- DEPENDS_ON: -->
<!-- BUILD_ORDER: 44 -->

# BitBucket Pipelines Adapter — Implementation Spec

## Overview

Add a third CI/CD adapter to R1 — BitBucket Pipelines — at strict parity with the
shipped GitHub Actions adapter (`internal/cicd/github/` + `generateGitHub` in
`internal/cicd/cicd.go`) and the GitLab CI adapter (`internal/cicd/gitlab/` +
`generateGitLab` in `internal/cicd/cicd.go`). Surfaces three things: (1) a
`bitbucket-pipelines.yml` template generator (mirrors the GH/GitLab template
generators in `internal/cicd/cicd.go`), (2) a REST runtime client
(`internal/cicd/bitbucket/`) for triggering pipelines, polling status, fetching
logs, posting PR comments, and writing commit statuses, and (3) generator
wiring through the existing `r1 cicd` cobra command in `cmd/r1/cicd_cmd.go`.

Per SOW C5, this is a parity adapter — no new capabilities beyond what GH
Actions already exposes. Bitbucket Cloud is the primary target; Bitbucket Data
Center (self-hosted) is a documented fallback where the REST surface diverges.

> Delivery status (audit A056, 2026-07-02): (1) the template generator and
> (3) the `r1 cicd` generator wiring shipped with C5; (2) the REST runtime
> client was delivered as a library but stayed unwired until
> `r1 cicd trigger|status|logs --provider bitbucket` landed
> (cmd/r1/cicd_cmd.go). Trigger/status/logs are now CLI-reachable; the PR
> comment + auto-review surface (comment.go / reviewer.go) remains
> library-only with no CLI verb.

## Stack & Versions

- Go 1.22+ (matches existing adapters).
- stdlib `net/http`, `encoding/json` only. Do NOT add a Bitbucket Go SDK —
  the GitLab adapter at `internal/cicd/gitlab/gitlab.go` proves that a hand-
  rolled REST client is the project convention. (GitHub uses
  `github.com/google/go-github/v62` because the project already has it as a
  transitive dep; do not introduce a new dependency for Bitbucket.)
- BitBucket Cloud REST API v2.0 — `https://api.bitbucket.org/2.0/`.
- BitBucket Pipelines YAML schema: the pipelines validator at
  `https://api.bitbucket.org/2.0/repositories/{w}/{r}/pipelines-config/validate`
  is authoritative — used in integration tests (T22).
- Atlassian OIDC: `BITBUCKET_STEP_OIDC_TOKEN` env var produced when a step sets
  `oidc: true`. JWT issuer is
  `https://api.bitbucket.org/2.0/workspaces/{workspace}/pipelines-config/identity/oidc`.

## Existing Patterns to Follow — READ THESE FIRST

| Concern | Reference file | Note |
|---|---|---|
| Template generator shape | `internal/cicd/cicd.go` (`generateGitHub`, `generateGitLab`, `generateCircleCI`) | strings.Builder + `fmt.Fprintf`. Returns `(yaml, outputPath, err)`. No external template engine. |
| Validation lint | `internal/cicd/cicd.go` `ValidateConfig` | Substring presence check; no YAML parser. |
| Mode dispatch | `internal/cicd/cicd.go` `Mode` (`review` / `autofix` / `mission`) | The Bitbucket adapter must support all three modes. |
| Options struct | `internal/cicd/cicd.go` `Options` | Add no new fields — reuse as-is. `NodeLabel` becomes Bitbucket Docker image. |
| REST client | `internal/cicd/gitlab/gitlab.go` | Copy structure verbatim: `Config`, `Client`, `do`, `doRaw`, `applyAuth`, `APIError`, `IsNotFound`. Replace `PRIVATE-TOKEN` with `Authorization: Bearer`. |
| Auto-reviewer pipeline | `internal/cicd/github/reviewer.go` | `Reviewer{c, parser, prompt}` + `LLMFunc` + `ParseFindings` + `RenderCommentBody` — copy the contract exactly; only the post-comment call differs. |
| Cobra wiring | `cmd/r1/cicd_cmd.go` | Adds Bitbucket to provider switch. Auto-detect project type for image selection (T4). |
| Integration skill notes | `internal/skill/builtin/integrations/bitbucket.md` | Already documents v2.0 base URL, auth methods, commit status endpoint, webhook signature. Re-use these as gospel — do not re-research. |
| Test patterns | `internal/cicd/cicd_test.go`, `internal/cicd/gitlab/gitlab_test.go`, `internal/cicd/github/github_test.go` | Substring assertions, `httptest.NewServer`, table-driven mode coverage. |

## Library Preferences

- HTTP: stdlib `net/http` with `context.WithTimeout` (matches `gitlab.go`).
- JSON: stdlib `encoding/json`.
- YAML: no YAML parser — `ValidateConfig` does substring checks. The
  authoritative validator is BitBucket's own pipelines-config validate REST
  endpoint (T22).
- Logging: existing `logging/` redaction helpers; redact `BITBUCKET_API_TOKEN`,
  `BITBUCKET_USERNAME`, `R1_API_TOKEN`.

## Boundaries — What NOT To Do

- DO NOT add features the GH Actions adapter lacks. Parity for v1.
- DO NOT depend on Bitbucket Cloud-only features when a Server (self-hosted /
  Data Center) equivalent exists; if a Cloud-only feature is unavoidable,
  guard it with a `Config.Edition` flag (default `cloud`) and document the
  Server fallback in `docs/integrations/bitbucket-pipelines.md`.
- DO NOT use long-lived BitBucket access tokens in CI. OIDC exchange only
  (T10). Long-lived tokens are permitted in `Config.Token` for local-dev
  invocation of the runtime client (e.g., a developer running `r1` from their
  workstation) — never in the generated pipeline YAML.
- DO NOT duplicate logic across adapters. If a third copy of an identifiable
  helper would emerge (e.g., `ParseFindings`, `RenderCommentBody`, `LLMFunc`
  type, OIDC exchange), promote it to `internal/cicd/shared/` and refactor
  the GH + GitLab adapters in the same PR.
- DO NOT import a Bitbucket Go SDK. Stdlib only (consistency with GitLab
  adapter; SOW estimate of 2 weeks assumes no new deps).
- DO NOT write the pipeline YAML output anywhere other than
  `bitbucket-pipelines.yml` at the repo root — Bitbucket only reads from this
  single canonical path.

## Boundaries — Parity Source of Truth

The parity-audit test (T23) treats this enumerated list as the contract:

1. Workflow/template file generation — `GenerateConfig(ProviderBitbucket, opts)`.
2. Three modes — `review`, `autofix`, `mission`.
3. Step-by-step R1 invocation — `r1 review`, `r1 scan-repair`, `r1 build`.
4. Artifact upload — tracebundles, audit reports.
5. Secret injection — `R1_API_TOKEN`, `R1_AUDIT_ENDPOINT`, `ANTHROPIC_API_KEY`.
6. PR/MR commenting on findings (inline, line-anchored — matches GH which
   posts inline via `PostReviewComment`).
7. Status check / required-check integration on the head commit.
8. Runtime REST client: `TriggerPipeline`, `GetPipelineStatus`,
   `WaitForCompletion`, `GetJobLog`, `ListPipelineJobs` equivalents.
9. Auto-reviewer pipeline mirroring `github/reviewer.go`.
10. Error classification: `IsNotFound`, `IsUnauthorized`.

If GH Actions ever gains a capability, this list extends and the parity test
forces the BB adapter to follow.

## Data Models

### `internal/cicd/bitbucket/types.go`

```go
// Pipeline mirrors the trimmed BB v2.0 pipeline payload.
type Pipeline struct {
    UUID        string    `json:"uuid"`
    BuildNumber int       `json:"build_number"`
    State       State     `json:"state"`
    Repository  RepoRef   `json:"repository"`
    Target      Target    `json:"target"`
    CreatedOn   time.Time `json:"created_on,omitempty"`
    CompletedOn time.Time `json:"completed_on,omitempty"`
    DurationSec int64     `json:"duration_in_seconds,omitempty"`
}

func (p Pipeline) IsTerminal() bool { return p.State.Name == "COMPLETED" }

type State struct {
    Name   string `json:"name"`           // PENDING | IN_PROGRESS | COMPLETED
    Result struct {
        Name string `json:"name"`         // SUCCESSFUL | FAILED | STOPPED | ERROR
    } `json:"result,omitempty"`
}

// Step is one step inside a pipeline.
type Step struct {
    UUID   string `json:"uuid"`
    Name   string `json:"name"`
    State  State  `json:"state"`
    LogURL string `json:"-"` // synthesized by client
}

// CommitStatus shape for POST /commit/{sha}/statuses/build.
type CommitStatus struct {
    Key         string `json:"key"`         // "r1-verify"
    State       string `json:"state"`       // INPROGRESS | SUCCESSFUL | FAILED | STOPPED
    Name        string `json:"name"`        // "R1 Verify"  (must equal shared.CommitStatusName)
    URL         string `json:"url"`
    Description string `json:"description"` // <= 140 chars; truncate client-side
}

// PRComment shape for POST /pullrequests/{id}/comments.
type PRComment struct {
    Content struct {
        Raw string `json:"raw"`
    } `json:"content"`
    Inline *struct {
        Path string `json:"path"`
        To   int    `json:"to,omitempty"`   // new-side line
        From int    `json:"from,omitempty"` // old-side line
    } `json:"inline,omitempty"`
}
```

## Implementation Checklist

Each item is self-contained: file paths, function signatures, pattern source,
and acceptance condition. Subagents pick them off in order.

1. [ ] T1: Adapter parity survey (document-in-spec) — Before writing code,
   produce `docs/integrations/bitbucket-pipelines-parity.md` that enumerates
   every public exported symbol in `internal/cicd/github/` and
   `internal/cicd/gitlab/`, mapping each to its Bitbucket equivalent (or
   marking "N/A — Bitbucket lacks X"). The parity-audit test (T23) reads
   this file as the contract. Use a markdown table:
   `| GH Symbol | GitLab Symbol | BB Symbol | Required | Note |`. This task
   exists so the implementer cannot drift from the parity scope.

2. [ ] T2: Provider constant + dispatch wiring — In `internal/cicd/cicd.go`,
   add `ProviderBitbucket Provider = "bitbucket"`. Append to `AllProviders()`.
   Extend the `GenerateConfig` switch with
   `case ProviderBitbucket: return generateBitbucket(opts)`. Extend
   `ValidateConfig` with required-key checks for the BB YAML (see T5). The
   `TestUnsupportedProvider` test in `internal/cicd/cicd_test.go:174`
   currently passes `"bitbucket"` as the unsupported provider — update that
   test to use `"jenkins"` (or any string not in the new provider list);
   failing to update it is a build-break.

3. [ ] T3: `generateBitbucket` template renderer — New file
   `internal/cicd/cicd_bitbucket.go` (mirrors how `generateGitHub` lives in
   `cicd.go`; split out only because `cicd.go` is already 455 lines).
   Signature:
   `func generateBitbucket(opts Options) (yaml string, outputPath string, err error)`.
   Returns `(<rendered>, "bitbucket-pipelines.yml", nil)`. Body builds a
   `bitbucket-pipelines.yml` with: top-level `image:` (auto-detected by T4),
   `definitions:` (caches + services), `pipelines:` map with `default`,
   `pull-requests`, `branches.{branch}`, `custom.r1-mission` keyed by the
   active `Mode`. Use `bitbucketTrigger(opts)` + `bitbucketJobStep(opts)`
   mode-dispatch helpers — same shape as `githubTrigger` + `githubJobStep`.
   Header comment block matches the GH/GitLab generators (workflow name,
   generated-by line, required-secrets reminder).

4. [ ] T4: Project-type auto-detection — Helper
   `func detectImage(workspaceDir string) string` in
   `internal/cicd/cicd_bitbucket.go`. Reads (in order, first match wins):
   `go.mod` → `golang:1.22-bookworm`; `package.json` → `node:20-bookworm`;
   `pyproject.toml` || `requirements.txt` → `python:3.12-bookworm`; else
   `atlassian/default-image:5`. Override path: `Options.NodeLabel` (treated
   as Docker image when provider is Bitbucket — matches how `nodeLabel`
   doubles as image for GitLab). Unit test in
   `internal/cicd/cicd_bitbucket_test.go` with a `t.TempDir` per case.

5. [ ] T5: `ValidateConfig` lint keys for Bitbucket — Extend the switch in
   `internal/cicd/cicd.go ValidateConfig` with:
   `for _, key := range []string{"pipelines:", "image:", "script:", "ANTHROPIC_API_KEY"} { ... }`.
   Mirrors the GH/GitLab pattern exactly.

6. [ ] T6: Mode → trigger map (`bitbucketTrigger`) — Helper in
   `cicd_bitbucket.go`. Returns the YAML key under `pipelines:` for the active
   mode:
   - `review` → `pull-requests: '**':` (Bitbucket's PR-event analogue to GH's
     `pull_request:`).
   - `autofix` → `branches: {main}:` plus `pull-requests: '**':`.
   - `mission` → `custom: r1-mission:` (manual trigger; analogue to
     `workflow_dispatch`). Document that `custom` pipelines are triggered via
     `POST /repositories/{w}/{r}/pipelines/` with
     `target.selector.type: custom` and `selector.pattern: r1-mission` —
     implemented in T9.

7. [ ] T7: Mode → step script map (`bitbucketJobStep`) — Helper in
   `cicd_bitbucket.go`. Returns a `- step:` block per mode:
   - `review`: `r1 review --policy "$R1_POLICY" --pr-id "$BITBUCKET_PR_ID" --output bitbucket-comment`.
     The output flag value `bitbucket-comment` is new — register it in the
     same PR (mirrors `github-comment`/`gitlab-comment` in `cicd.go:228`,
     `cicd.go:302`).
   - `autofix`: `r1 scan-repair --policy "$R1_POLICY" --auto-commit --push-fixes`.
   - `mission`: `r1 build "$PLAN" --policy "$R1_POLICY" --workers N --open-pr`.
   Every step includes:
   - `oidc: true` (enables `BITBUCKET_STEP_OIDC_TOKEN`).
   - `caches:` selected by detected project type (T4).
   - `artifacts:` listing `tracebundles/**`, `audit-reports/**`,
     `r1-review.json`, `r1-mission.log`.
   - `before-script:` curl-installs R1 binary from the GitHub releases mirror
     (matches `cicd.go:181` install pattern), then sets the `INPROGRESS`
     commit status via T15.
   - `after-script:` posts `FAILED` commit status on non-zero exit
     (idempotent — overwrites the `INPROGRESS`).

8. [ ] T8: `internal/cicd/bitbucket/bitbucket.go` REST client — Copy
   `internal/cicd/gitlab/gitlab.go` verbatim as the starting point, then:
   - Replace `DefaultBaseURL = "https://gitlab.com/api/v4"` with
     `"https://api.bitbucket.org/2.0"`.
   - Replace the `PRIVATE-TOKEN` header with bearer-token assembly:
     `req.Header.Set("Authorization", "Bearer "+c.token)`. (Workspace API
     token basic-auth mode is permitted via `Config.BasicAuth bool` — when
     true, use `req.SetBasicAuth(c.username, c.token)`. Both modes documented
     in `internal/skill/builtin/integrations/bitbucket.md:13-23`.)
   - Adapt the `Pipeline` JSON shape (UUID-based, not int-based — see
     types.go above).
   - `IsTerminal()` checks `p.State.Name == "COMPLETED"`.
   - All other code (`do`, `doRaw`, `applyAuth`, `APIError`, `IsNotFound`)
     stays structurally identical so a future shared package extraction is
     trivial.

9. [ ] T9: `TriggerPipeline` + status + log methods — Public methods in
   `bitbucket.go` that mirror the GitLab client's signatures one-for-one:
   - `TriggerPipeline(ctx, workspace, repo, ref string, vars map[string]string) (*Pipeline, error)`
     → `POST /repositories/{w}/{r}/pipelines/` with
     `{target: {ref_type: "branch", type: "pipeline_ref_target", ref_name: ref, selector: {type: "branches"}}, variables: [{key, value, secured: false}]}`.
   - `GetPipelineStatus(ctx, workspace, repo, uuid string) (*Pipeline, error)`.
   - `WaitForCompletion(ctx, workspace, repo, uuid string, timeout time.Duration) (*Pipeline, error)`
     — same poll loop as the GitLab adapter.
   - `ListPipelineSteps(ctx, w, r, uuid) ([]Step, error)` →
     `/pipelines/{u}/steps/`.
   - `GetStepLog(ctx, w, r, pipelineUUID, stepUUID string) (string, error)` →
     `/pipelines/{p}/steps/{s}/log` (returns raw text).

10. [ ] T10: `auth.go` — OIDC exchange — `internal/cicd/bitbucket/auth.go`
    exposes
    `ExchangeOIDC(ctx context.Context, audience, oidcToken string) (r1Token string, expiresAt time.Time, err error)`.
    Inside a pipeline step the caller reads
    `os.Getenv("BITBUCKET_STEP_OIDC_TOKEN")` (Atlassian sets this when
    `oidc: true`), then POSTs to R1's `/auth/sso/oidc-exchange` endpoint
    with
    `{"oidc_token": "<jwt>", "issuer": "https://api.bitbucket.org/2.0/workspaces/{w}/pipelines-config/identity/oidc", "audience": "<R1 audience>"}`.
    The R1 audit endpoint dependency is A4 SSO — record it in this spec's
    DEPENDS_ON frontmatter once A4 is in flight. Until A4 lands, the function
    returns `("", ErrSSONotReady)`; the generated YAML detects the error and
    falls back to `R1_API_TOKEN` env var with a stderr warning.

11. [ ] T11: `comment.go` — PR comment helpers — Two functions in
    `internal/cicd/bitbucket/comment.go`:
    - `PostPRComment(ctx, w, r string, prID int, body string) error` →
      unanchored summary comment via `POST /pullrequests/{id}/comments`.
    - `PostInlinePRComment(ctx, w, r string, prID int, path string, line int, body string) error`
      → line-anchored via the same endpoint with
      `inline: {path, to: line}`.
    Plus `BuildSummaryBody(findings []shared.Finding, artifactURL string) string`
    that renders the same markdown format as
    `github/reviewer.go RenderCommentBody`. Reuse the `Finding` struct —
    promote `Finding`, `ParseFindings`, `RenderCommentBody`, `LLMFunc`,
    `ParserFunc`, `DefaultReviewPrompt` from
    `internal/cicd/github/reviewer.go` to `internal/cicd/shared/review.go` in
    the same PR. Update the GH adapter to re-export from `shared` for
    backward compatibility.

12. [ ] T12: `reviewer.go` — auto-review pipeline —
    `internal/cicd/bitbucket/reviewer.go` mirrors `github/reviewer.go`
    `AutoReview`. Signature:
    `func (r *Reviewer) AutoReview(ctx context.Context, workspace, repo string, prID int, llm shared.LLMFunc) ([]shared.Finding, error)`.
    Pipeline:
    1. `GetPullRequestDiff(ctx, w, r, prID)` → `/pullrequests/{id}/diff`
       (raw text; follow `next` pagination per
       `internal/skill/builtin/integrations/bitbucket.md:93`).
    2. `GetPullRequestHead(ctx, w, r, prID)` for the commit hash (needed for
       status).
    3. Render prompt (`shared.DefaultReviewPrompt`).
    4. `llm(ctx, prompt)` → parse with `shared.ParseFindings`.
    5. For each valid finding: `PostInlinePRComment(...)`.
    Returns parsed findings + a wrapped error if any post failed (matches
    `github/reviewer.go:147`).

13. [ ] T13: `artifact.go` — artifact upload helpers —
    `internal/cicd/bitbucket/artifact.go`. Bitbucket Pipelines auto-uploads
    files matching the step's `artifacts:` globs to its built-in artifact
    store (retained 14 days; max 1 GB per artifact). Two helpers:
    - `ArtifactRefForStep(workspace, repo string, pipelineUUID, stepUUID string) string`
      returns the public URL of the artifact archive
      (`/repositories/{w}/{r}/pipelines/{u}/steps/{s}/artifacts/`).
    - `DownloadArtifact(ctx, w, r, pipelineUUID, stepUUID, name string) ([]byte, error)`
      for the runtime client.
    Do NOT re-upload from Go — let Bitbucket's `artifacts:` glob handle it.
    Tracebundle path conventions (`tracebundles/*.tar.zst`) are set by
    `r1 verify`; document in the operator runbook (T24).

14. [ ] T14: `runner.go` — invokes R1 inside the Bitbucket runner image —
    `internal/cicd/bitbucket/runner.go` exposes
    `RunR1Step(ctx context.Context, mode shared.Mode, env map[string]string) error`
    that:
    1. Detects the runner context via env vars
       (`BITBUCKET_BUILD_NUMBER`, `BITBUCKET_REPO_SLUG`,
       `BITBUCKET_WORKSPACE`, `BITBUCKET_PR_ID`, `BITBUCKET_COMMIT`,
       `BITBUCKET_STEP_OIDC_TOKEN`).
    2. If `R1_AUDIT_ENDPOINT` is set, posts the structured audit envelope
       (per A3 `--one-shot` hardening; reference `cmd/r1/audit_emit.go`).
    3. Exchanges OIDC for an R1 token via T10.
    4. Sets the commit status to `INPROGRESS` via T15.
    5. Shells out to the appropriate R1 command
       (`r1 review` / `r1 scan-repair` / `r1 build`) — does not re-implement
       R1 logic.
    6. Captures stdout/stderr to tracebundle in `tracebundles/`.
    7. Sets the commit status to `SUCCESSFUL` / `FAILED` based on exit code.
    Tested with a fake `exec.Cmd` via the existing test pattern used by
    `internal/deploy/`.

15. [ ] T15: Commit-status writer —
    `func (c *Client) PostCommitStatus(ctx context.Context, w, r, commitSHA string, status CommitStatus) error`
    in `internal/cicd/bitbucket/bitbucket.go`. Calls
    `POST /repositories/{w}/{r}/commit/{sha}/statuses/build` with the
    `CommitStatus` JSON shape. Status `Key="r1-verify"`,
    `Name="R1 Verify"` — matches the GH Actions adapter convention. Source
    both names from a single `shared.CommitStatusName` const; assert via test
    that `shared.CommitStatusName == "R1 Verify"`. Per BB docs, `description`
    is truncated server-side at 140 chars — truncate client-side first.

16. [ ] T16: Generator command extension — `cmd/r1/cicd_cmd.go` already
    routes by `--provider`. The Bitbucket path picks up automatically once
    `ProviderBitbucket` is registered (T2). Add to the help text:
    `r1 cicd --provider bitbucket --mode review`. Add an additional
    subcommand surface `r1 cicd init bitbucket [--workspace .]` that is
    sugar for
    `r1 cicd --provider bitbucket --mode review --output ./bitbucket-pipelines.yml`
    and additionally runs T4 image detection against the workspace dir. The
    flag `--workspace` is the project root; default `.`. Implementation: add
    an `init` sub-flag handler at the top of `cicdCmd` that intercepts when
    `args[0] == "init" && args[1] == "bitbucket"` and rewrites args.

17. [ ] T17: Per-language YAML templates —
    `internal/cicd/bitbucket/templates/{go,node,python,generic}.yaml`.
    Embedded via `//go:embed templates/*.yaml`, loaded by `detectImage`
    (T4), and concatenated into the final YAML. Each template contributes a
    `definitions.caches.<lang>` block + a `before-script:` snippet (e.g.,
    `node`: `npm ci`; `go`: `go mod download`; `python`:
    `pip install -r requirements.txt`). The `generic.yaml` template runs
    only `r1 verify` with no language setup.

18. [ ] T18: Required secrets / env handling — Document and enforce:
    - `R1_API_TOKEN` (workspace variable, secured, fallback when OIDC fails).
    - `R1_AUDIT_ENDPOINT` (workspace variable, optional, from A3).
    - `ANTHROPIC_API_KEY` (workspace variable, secured, for `r1 verify`
      subagents — only required for modes that call LLMs).
    - `BITBUCKET_API_TOKEN` (repo or workspace variable, secured) — used by
      `runner.go` to call back into Bitbucket REST (PR comments, commit
      status) when OIDC exchange is not yet available (A4 not landed).
    Workspace-level variables are scoped to all repos in the workspace;
    repo-level override individual workspace vars. The generated YAML reads
    from `$VAR` directly (Bitbucket substitutes at runtime). Document the
    order-of-precedence in the operator runbook (T24).

19. [ ] T19: Tracebundle artifact glob — In every generated step that runs
    R1, declare:
    ```yaml
    artifacts:
      - tracebundles/**/*.tar.zst
      - audit-reports/**/*.json
      - r1-review.json
      - r1-mission.log
    ```
    Verify in `cicd_bitbucket_test.go` that all four globs are present in
    every mode's rendered YAML. The tracebundle is also linked from the PR
    comment summary body via `BuildSummaryBody(findings, artifactURL)` (T11)
    using the URL from T13.

20. [ ] T20: Self-hosted (Data Center) variant — Document in
    `docs/integrations/bitbucket-pipelines.md` and gate behind
    `Config.Edition`:
    - `Edition: "cloud"` (default) → `DefaultBaseURL` =
      api.bitbucket.org/2.0.
    - `Edition: "server"` → caller must set `Config.BaseURL` to the on-prem
      REST root (typically `https://<host>/rest/api/1.0` — note v1.0 path
      for DC, vs v2.0 for Cloud). The runtime client supports both base URL
      patterns; the template generator emits Cloud-shaped YAML by default
      and emits Server-shaped YAML when `Options.Branch` is prefixed with
      `server:` (escape hatch — keep the flag surface small per SOW
      boundaries). v1 ships with Cloud as primary and Server as best-effort.

21. [ ] T21: Unit tests — `internal/cicd/cicd_bitbucket_test.go` and
    `internal/cicd/bitbucket/{bitbucket_test,reviewer_test,runner_test,auth_test}.go`.
    Coverage:
    - All 3 modes × `GenerateConfig(ProviderBitbucket, ...)`: each output
      contains `pipelines:`, `image:`, `oidc: true`, `artifacts:`, all
      secret names from T18, and the mode-specific R1 command.
    - `ValidateConfig(ProviderBitbucket, yaml)` returns zero warnings for
      every mode.
    - `httptest.NewServer` covers: `TriggerPipeline` (POST shape + 201
      response), `GetPipelineStatus` (200 + JSON shape), `WaitForCompletion`
      (polls 3 times then sees COMPLETED), `PostPRComment`,
      `PostInlinePRComment`, `PostCommitStatus`, `GetStepLog`,
      `ListPipelineSteps`, `IsNotFound` classifier on a 404.
    - `Reviewer.AutoReview` against `httptest`: diff fetch, head fetch, LLM
      callback returns 2 findings → 2 inline comments posted.
    - `ExchangeOIDC` against `httptest`: success path + `ErrSSONotReady`
      fallback path.

22. [ ] T22: Integration tests — `internal/cicd/bitbucket/integration_test.go`
    behind a `bitbucket_integration` build tag (matches the GH adapter's tag
    convention; see `internal/cicd/github/github_test.go`). Tests:
    1. Validator round-trip — generate YAML for every mode, POST to
       `/repositories/{w}/{r}/pipelines-config/validate` against a real
       (fixture) repo. Skip when `BITBUCKET_API_TOKEN` is unset. Asserts
       Bitbucket's own validator accepts the YAML.
    2. End-to-end — trigger a pipeline against a fixture repo
       (`r1-bitbucket-fixture` — a separate empty repo with the generated
       YAML committed), poll to completion, fetch the artifact, assert the
       tracebundle is downloadable.
    3. PR comment round-trip — open a PR on the fixture repo, run
       `Reviewer.AutoReview` with a stub LLM that returns one finding,
       assert the comment shows up via
       `GET /pullrequests/{id}/comments`.

23. [ ] T23: Parity-audit test — `internal/cicd/parity_test.go`. Uses
    `go list -json` (or `go/types`) to enumerate public symbols in
    `internal/cicd/github/` and `internal/cicd/bitbucket/` and asserts that
    every public symbol in the GH adapter has a same-named or same-purposed
    symbol in the BB adapter. Concretely: read
    `docs/integrations/bitbucket-pipelines-parity.md` (T1) and fail the test
    if any row marked "Required" lacks a BB-side entry. Hand-curated
    allow-list for genuine N/A rows. The parity doc IS the test fixture — no
    silent drift.

24. [ ] T24: Docs — `docs/integrations/bitbucket-pipelines.md` — Operator
    runbook. Sections:
    - Quickstart per template (go/node/python/generic): copy-paste 5-line
      `r1 cicd init bitbucket` flow.
    - Required Bitbucket workspace variables (T18 list) with steps for
      setting them at workspace vs repo scope.
    - OIDC setup — link to Atlassian docs, JWT issuer URL, R1 audience
      string, claim mapping.
    - Troubleshooting — runner image pull failures (private registry creds),
      OIDC misconfiguration (issuer mismatch, audience mismatch), comment
      API rate limits (~1,000 req/hour per user/app per
      `internal/skill/builtin/integrations/bitbucket.md:92`; 429 with
      Retry-After header).
    - Parity matrix vs GH Actions + GitLab CI — table with rows for each
      contract item (T1) and columns for each provider showing Yes/N/A.
    - Self-hosted (DC) caveats — T20 base URL handling.

25. [ ] T25: Wire `r1 cicd --list` — Update the `cicdCmd` `--list` branch in
    `cmd/r1/cicd_cmd.go` so that `bitbucket` appears in the printed provider
    list. (Falls out for free from T2 — assert with a CLI test.)

26. [ ] T26: Update top-level doc refs —
    `docs/FEATURE-MAP.md:276` and `docs/BUSINESS-VALUE.md:211` currently
    describe the BB adapter as "Potential" / forthcoming. Mark them shipped
    once the build closes. `docs/ARCHITECTURE.md:557` similarly mentions it
    as a future item; update.

## Acceptance Criteria — Measurable

| # | Criterion | How to verify |
|---|---|---|
| 1 | `r1 cicd --provider bitbucket --mode review` writes a valid `bitbucket-pipelines.yml`. | Run the command; `ValidateConfig` returns zero warnings; Bitbucket's own `/pipelines-config/validate` REST endpoint returns 200. |
| 2 | All three modes (review/autofix/mission) render without error. | `TestAllModesAllProviders` in `cicd_test.go` passes after Bitbucket is added. |
| 3 | The generated YAML uses `oidc: true` and references `BITBUCKET_STEP_OIDC_TOKEN`. | Unit test substring assertion. |
| 4 | The parity-audit test (T23) passes — every required GH adapter symbol has a BB equivalent. | `go test ./internal/cicd/...` returns green. |
| 5 | A fixture repo with the generated YAML runs end-to-end on Bitbucket Cloud, posts an inline PR comment from `Reviewer.AutoReview`, and uploads a downloadable tracebundle artifact. | Integration test (T22) with `BITBUCKET_API_TOKEN` set. |
| 6 | The commit status `R1 Verify` appears on the head commit and matches the GH Actions adapter's status name byte-for-byte. | Integration test (T22) + unit test asserting `shared.CommitStatusName == "R1 Verify"`. |
| 7 | No long-lived Bitbucket access tokens are emitted in the generated YAML — only OIDC exchange. | Grep test: rendered YAML must NOT contain `BITBUCKET_API_TOKEN` as a step variable; only the OIDC token path. |
| 8 | No duplicated logic — `Finding`, `ParseFindings`, `RenderCommentBody`, `LLMFunc`, `DefaultReviewPrompt` all live in `internal/cicd/shared/` and are imported by both `github` and `bitbucket` adapters. | `go list -deps ./internal/cicd/github/...` shows `internal/cicd/shared` in the dep graph. |
| 9 | Docs `docs/integrations/bitbucket-pipelines.md` exists with all six sections from T24. | Manual inspection + a docs-presence test asserting the section headers. |
| 10 | The parity matrix in the docs has zero "TBD" or "Missing" rows for v1 features. | Manual review at PR time. |

## Dependencies

- A3 `--one-shot` hardening — needed for the `R1_AUDIT_ENDPOINT` env
  contract (T18). If not yet landed, the runner sets a stderr warning and
  continues without audit emission.
- A4 SSO — needed for OIDC exchange (T10). If not yet landed, the runner
  falls back to `R1_API_TOKEN`.

## Out of Scope (Defer to v2)

- Bitbucket Server (Data Center) full parity — v1 is best-effort behind
  `Config.Edition` flag (T20).
- Bitbucket Pipes (community step library, GH-Actions-marketplace analogue).
- Cross-pipeline artifact passing (BB has `artifacts:` for same-pipeline
  steps; cross-pipeline requires external storage — out of scope).
- Self-hosted runner installation automation.

## Files Touched / Created

New:
- `internal/cicd/cicd_bitbucket.go` — template renderer (T3-T7).
- `internal/cicd/cicd_bitbucket_test.go` — unit tests for renderer (T21).
- `internal/cicd/bitbucket/bitbucket.go` — REST client (T8, T9, T15).
- `internal/cicd/bitbucket/types.go` — JSON shapes.
- `internal/cicd/bitbucket/auth.go` — OIDC exchange (T10).
- `internal/cicd/bitbucket/comment.go` — PR comments (T11).
- `internal/cicd/bitbucket/reviewer.go` — auto-review pipeline (T12).
- `internal/cicd/bitbucket/artifact.go` — artifact helpers (T13).
- `internal/cicd/bitbucket/runner.go` — in-step runner (T14).
- `internal/cicd/bitbucket/*_test.go` — unit tests (T21).
- `internal/cicd/bitbucket/integration_test.go` — integration tests (T22).
- `internal/cicd/bitbucket/templates/{go,node,python,generic}.yaml` —
  embed templates (T17).
- `internal/cicd/shared/review.go` — promoted `Finding`/`ParseFindings`/etc
  (T11).
- `internal/cicd/parity_test.go` — parity audit (T23).
- `docs/integrations/bitbucket-pipelines.md` — runbook (T24).
- `docs/integrations/bitbucket-pipelines-parity.md` — parity contract (T1).

Modified:
- `internal/cicd/cicd.go` — `ProviderBitbucket` const, `AllProviders`,
  `GenerateConfig` dispatch, `ValidateConfig` keys (T2, T5).
- `internal/cicd/cicd_test.go` — fix `TestUnsupportedProvider` so it no
  longer uses `"bitbucket"` (T2).
- `internal/cicd/github/reviewer.go` — re-export from `shared/` (T11).
- `cmd/r1/cicd_cmd.go` — help text + `init bitbucket` subcommand (T16, T25).
- `docs/FEATURE-MAP.md`, `docs/BUSINESS-VALUE.md`, `docs/ARCHITECTURE.md` —
  mark BB adapter as shipped (T26).
