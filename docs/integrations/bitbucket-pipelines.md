# BitBucket Pipelines Adapter — Operator Runbook (C5)

R1 ships a BitBucket Pipelines adapter at strict parity with the GitHub Actions
and GitLab CI adapters. This runbook covers setup, per-language quickstart,
required workspace variables, OIDC, troubleshooting, and the on-prem
(Bitbucket Data Center) caveats.

> Implements `specs/bitbucket-pipelines-adapter.md`. Parity contract:
> [`bitbucket-pipelines-parity.md`](./bitbucket-pipelines-parity.md).

## Quickstart

Each language is one command:

### Go projects

```bash
cd my-go-project
r1 cicd init bitbucket --workspace .
git add bitbucket-pipelines.yml
git commit -m "ci: add R1 BitBucket Pipelines"
git push
```

The generator detects `go.mod` and emits a step that uses the
`golang:1.22-bookworm` image with a `gomod` cache.

### Node.js projects

```bash
cd my-node-project
r1 cicd init bitbucket --workspace .
git add bitbucket-pipelines.yml
git commit -m "ci: add R1 BitBucket Pipelines"
git push
```

Detected via `package.json`. Image: `node:20-bookworm`. Cache: `nodemod`.

### Python projects

```bash
cd my-python-project
r1 cicd init bitbucket --workspace .
git add bitbucket-pipelines.yml
git commit -m "ci: add R1 BitBucket Pipelines"
git push
```

Detected via `pyproject.toml` or `requirements.txt`. Image:
`python:3.12-bookworm`. Cache: `pip`.

### Generic / polyglot projects

```bash
cd my-project
r1 cicd --provider bitbucket --mode review
```

Default image: `atlassian/default-image:5`. No language toolchain is
installed; the step runs `r1 verify` only.

## Required workspace variables

Set these in the Bitbucket workspace (or per-repo override at
**Repository settings → Pipelines → Repository variables**):

| Variable               | Scope               | Secured | Required when                                              |
|------------------------|---------------------|:-------:|------------------------------------------------------------|
| `ANTHROPIC_API_KEY`    | Workspace or repo   |  Yes    | Any mode that calls an LLM (review, autofix, mission).     |
| `R1_API_TOKEN`         | Workspace           |  Yes    | Fallback when OIDC exchange is not yet available (A4).     |
| `R1_AUDIT_ENDPOINT`    | Workspace           |  No     | Optional — A3 audit-envelope endpoint.                     |
| `BITBUCKET_API_TOKEN`  | Repository          |  Yes    | Only when OIDC fallback path is active. **Not** referenced as a step-level env var in the rendered YAML. |

**Order of precedence:** repo-level workspace vars override workspace-level
vars. The pipeline step reads `$VAR` directly; Bitbucket substitutes at
runtime.

## OIDC setup (preferred — no long-lived secrets)

Bitbucket Pipelines issues an OpenID Connect JWT to every step that sets
`oidc: true`. R1's generated YAML always sets `oidc: true`, so the JWT
appears in `$BITBUCKET_STEP_OIDC_TOKEN`.

1. In your R1 IdP/SSO panel (admin.r1.run), add a new OIDC issuer:
   ```
   https://api.bitbucket.org/2.0/workspaces/<your-workspace>/pipelines-config/identity/oidc
   ```
2. Configure the audience claim to match your R1 tenant's audience string
   (default: `r1.run`).
3. The runner posts the JWT to `/auth/sso/oidc-exchange` and receives a
   short-lived R1 token. No `R1_API_TOKEN` workspace secret is required.

> **Dependency:** SSO exchange depends on A4. Until A4 lands the
> exchange endpoint returns 404; the runner detects `ErrSSONotReady` and
> falls back to `R1_API_TOKEN` with a stderr warning. The pipeline still
> succeeds.

References:

- [Atlassian OIDC docs](https://support.atlassian.com/bitbucket-cloud/docs/integrate-pipelines-with-resource-servers-using-oidc/)

## Modes

| Mode        | Trigger                                                                          | What it does                                                         |
|-------------|----------------------------------------------------------------------------------|----------------------------------------------------------------------|
| `review`    | `pull-requests:` (every PR)                                                       | R1 reviews the diff, posts inline comments on findings, writes a `R1 Verify` commit status row. |
| `autofix`   | `branches: main` + `pull-requests:`                                               | R1 fixes failing lint/test issues and pushes the fix commits.        |
| `mission`   | `custom: r1-mission` (manual trigger)                                             | R1 executes a plan file end-to-end and opens a PR when complete.     |

Manual mission trigger:

```bash
curl -X POST \
  -u "$EMAIL:$BITBUCKET_API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"target":{"type":"pipeline_ref_target","ref_type":"branch","ref_name":"main","selector":{"type":"custom","pattern":"r1-mission"}}}' \
  "https://api.bitbucket.org/2.0/repositories/$WS/$REPO/pipelines/"
```

## Troubleshooting

### Runner image pull fails (private registry)

If you use a private base image, register a Docker config in
**Repository settings → Pipelines → Variables** under `DOCKER_CONFIG_JSON`.
Bitbucket auto-mounts the credentials when the step pulls.

### OIDC issuer mismatch

The R1 SSO config must list **exactly**
`https://api.bitbucket.org/2.0/workspaces/<your-workspace>/pipelines-config/identity/oidc`
as a trusted issuer. A wildcard or a different workspace slug fails the
audience check on the R1 side.

### OIDC audience mismatch

If `r1 review` reports `audience mismatch`, set the `R1_AUDIENCE` workspace
variable to match the audience your R1 tenant expects (default `r1.run`).

### Comment API rate limits

Bitbucket caps inline-comment posting at ~1,000 requests/hour per app/user.
The auto-reviewer retries 429s respecting the `Retry-After` header. For
mega-PRs (>1,000 findings), tune the `R1_MAX_COMMENTS` workspace variable
(default 50) to cap how many inline comments R1 posts per run.

### Missing `R1 Verify` status row

The status row is anchored to the PR head commit. If the commit SHA is not
available in `$BITBUCKET_COMMIT` (very old Pipelines runner image), upgrade
the runner image to the latest `atlassian/default-image:5` or set the
commit hash explicitly via `R1_COMMIT_OVERRIDE`.

## Parity matrix — BB Pipelines vs GH Actions vs GitLab CI

| Contract item                                  | GH Actions | GitLab CI | BB Pipelines |
|------------------------------------------------|:----------:|:---------:|:------------:|
| Template generator                             | Yes        | Yes       | Yes          |
| All three modes (review/autofix/mission)       | Yes        | Yes       | Yes          |
| Step-by-step R1 invocation                     | Yes        | Yes       | Yes          |
| Artifact upload (tracebundles, audit reports)  | Yes        | Yes       | Yes          |
| Secret injection                               | Yes        | Yes       | Yes          |
| Inline PR commenting                           | Yes        | N/A       | Yes          |
| Commit-status integration                      | Yes        | Yes       | Yes          |
| Runtime REST client                            | Yes        | Yes       | Yes          |
| Auto-reviewer pipeline                         | Yes        | N/A       | Yes          |
| Error classification (`IsNotFound`)            | Yes        | Yes       | Yes          |
| Error classification (`IsUnauthorized`)        | Yes        | N/A       | Yes          |
| OIDC exchange (no long-lived tokens)           | Native     | N/A       | Yes          |
| Shared review primitives (`internal/cicd/shared`) | Yes (re-exports) | N/A (template-only) | Yes |

Source of truth: [`bitbucket-pipelines-parity.md`](./bitbucket-pipelines-parity.md).

## Self-hosted (Bitbucket Data Center) caveats

Bitbucket Data Center exposes a different REST surface
(`https://<host>/rest/api/1.0` rather than v2.0 at `api.bitbucket.org`).
v1 of the R1 BB adapter:

- Defaults `Config.Edition = "cloud"` (production target).
- Accepts `Config.Edition = "server"` + an explicit `Config.BaseURL` to
  redirect REST traffic to the on-prem root.
- Emits Cloud-shaped YAML by default. To emit Server-shaped YAML, prefix
  the `--branch` flag with `server:` (escape hatch — kept minimal per the
  spec's "no new flag surface" boundary).
- The OIDC exchange endpoint differs in Data Center; the runner detects
  `ErrSSONotReady` and falls back to `R1_API_TOKEN`. Full DC OIDC parity
  is deferred to v2.

## File reference

- Template renderer: `internal/cicd/cicd_bitbucket.go`
- REST client: `internal/cicd/bitbucket/bitbucket.go`
- OIDC exchange: `internal/cicd/bitbucket/auth.go`
- PR comments: `internal/cicd/bitbucket/comment.go`
- Auto-reviewer: `internal/cicd/bitbucket/reviewer.go`
- Artifact helpers: `internal/cicd/bitbucket/artifact.go`
- In-step runner: `internal/cicd/bitbucket/runner.go`
- Per-language templates: `internal/cicd/bitbucket/templates/`
- Shared review primitives: `internal/cicd/shared/`
- Parity-audit test: `internal/cicd/parity_test.go`
