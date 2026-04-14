# gitlab

> GitLab: git hosting + CI/CD + issues + MRs. REST v4 and GraphQL APIs. Personal Access Tokens, OAuth2, or Project/Group deploy tokens. Self-managed instances use the same API — just different base URL.

<!-- keywords: gitlab, gitlab api, merge request, pipelines, gitlab ci -->

**Official docs:** https://docs.gitlab.com/ee/api/  |  **Verified:** 2026-04-14.

## Base URL + auth

- SaaS: `https://gitlab.com/api/v4/`
- Self-managed: `https://gitlab.example.com/api/v4/`
- Auth: `Authorization: Bearer <PAT>` or `PRIVATE-TOKEN: <PAT>` header.

## Token types

- **Personal Access Token**: per-user, scoped (api, read_api, read_repo, write_repo, etc.).
- **Project/Group Access Token**: scoped to one project/group; auto-created "bot" user.
- **Deploy Token**: read/write package + container registry + repo.
- **CI Job Token (`CI_JOB_TOKEN`)**: automatic in pipelines; limited scope.

## Common REST calls

```
GET  /projects/{id}                              # id can be numeric or URL-encoded path
GET  /projects/{id}/merge_requests?state=opened
POST /projects/{id}/merge_requests               { source_branch, target_branch, title }
POST /projects/{id}/merge_requests/{iid}/notes   { body: "LGTM" }
PUT  /projects/{id}/merge_requests/{iid}/merge   { merge_commit_message, should_remove_source_branch }

GET  /projects/{id}/issues
POST /projects/{id}/issues                       { title, description, labels }

GET  /projects/{id}/pipelines
POST /projects/{id}/pipeline?ref=main            # trigger pipeline
```

URL-encode project path: `mygroup%2Fmyrepo`.

## Webhooks

Project → Settings → Webhooks. Events: Push, Merge request, Issue, Pipeline, Job, Comment, Tag, Release, etc.

Verify secret: GitLab sends `X-Gitlab-Token` header — compare to your configured secret (plain string, not HMAC).

```ts
if (req.headers["x-gitlab-token"] !== WEBHOOK_SECRET) return 401;
```

## GitLab CI (`.gitlab-ci.yml`)

```yaml
stages: [build, test, deploy]

build:
  stage: build
  image: node:20
  script:
    - npm ci
    - npm run build
  artifacts:
    paths: [dist/]

test:
  stage: test
  image: node:20
  script: [npm test]

deploy:
  stage: deploy
  only: [main]
  environment: production
  script: [./deploy.sh]
```

Runner types: shared (SaaS-provided), group, project, self-hosted (shell / Docker / Kubernetes).

## Merge request approvals

```
GET  /projects/{id}/merge_requests/{iid}/approvals
POST /projects/{id}/merge_requests/{iid}/approve
POST /projects/{id}/merge_requests/{iid}/unapprove
```

Premium tier: merge request approval rules enforce required reviewers.

## GraphQL

`POST https://gitlab.com/api/graphql` with same auth header. Sometimes more efficient for deep MR/issue queries:

```graphql
query { project(fullPath: "gitlab-org/gitlab") { mergeRequests(state: opened) { nodes { iid title } } } }
```

## Common gotchas

- **Project ID URL encoding**: pass numeric ID OR URL-encoded path. Plain `group/repo` breaks routing.
- **Rate limit**: 2,000 req/min per user on SaaS; unauthenticated far lower. `RateLimit-*` response headers.
- **Self-managed API versions**: self-hosted may be older than gitlab.com — consult `/version` endpoint before relying on a feature.
- **Deploy tokens can't create MRs or comment** — for bots, use a Project Access Token.
- **Pipelines vs jobs**: one pipeline contains many jobs; operations apply at the pipeline OR job level — don't conflate the endpoints.

## Key reference URLs

- REST API root: https://docs.gitlab.com/ee/api/api_resources.html
- Merge requests: https://docs.gitlab.com/ee/api/merge_requests.html
- Pipelines: https://docs.gitlab.com/ee/api/pipelines.html
- Webhooks: https://docs.gitlab.com/ee/user/project/integrations/webhook_events.html
- CI YAML ref: https://docs.gitlab.com/ee/ci/yaml/
