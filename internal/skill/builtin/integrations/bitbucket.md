# bitbucket

> Bitbucket Cloud (Atlassian): git hosting, pull requests, Pipelines CI/CD. REST API 2.0. Tight Jira integration via smart-commits + linking.

<!-- keywords: bitbucket, bitbucket cloud, pull request, pipelines, atlassian -->

**Official docs:** https://developer.atlassian.com/cloud/bitbucket/rest/intro/  |  **Verified:** 2026-04-14.

## Base URL

`https://api.bitbucket.org/2.0/`

## Auth (2025+)

- **Workspace API tokens** (new, recommended): created in workspace settings, scoped to a single workspace, Basic auth with your Atlassian account email.
- **App passwords** (deprecated mid-2025 per Atlassian announcement): per-user, scoped. Still working but being phased out.
- **OAuth2 consumer** (for apps redistributed to others).
- **Repository/Project/Workspace Access Tokens**: scoped bot tokens.

```
Authorization: Bearer <token>           # OAuth2 or access tokens
Authorization: Basic <email:token b64>  # workspace API tokens
```

## Core endpoints

```
GET  /repositories/{workspace}/{repo}
GET  /repositories/{workspace}/{repo}/pullrequests?state=OPEN
POST /repositories/{workspace}/{repo}/pullrequests
     { title, source: { branch: { name: "feat" } }, destination: { branch: { name: "main" } }, description, reviewers: [{ uuid }] }
POST /repositories/{workspace}/{repo}/pullrequests/{id}/approve
POST /repositories/{workspace}/{repo}/pullrequests/{id}/merge
     { merge_strategy: "merge_commit"|"squash"|"fast_forward", close_source_branch: true }
```

## Webhooks

Repo → Settings → Webhooks. Events: `repo:push`, `pullrequest:created`, `pullrequest:updated`, `pullrequest:approved`, `pullrequest:fulfilled`, `pullrequest:rejected`, `repo:commit_status_created`, etc.

Bitbucket Cloud webhooks include `X-Hook-UUID` header but historically did NOT sign payloads. New **Webhook Secrets** (2024) add HMAC-SHA256 via `X-Hub-Signature` header:

```ts
const expected = "sha256=" + crypto.createHmac("sha256", SECRET).update(rawBody).digest("hex");
if (req.headers["x-hub-signature"] !== expected) return 401;
```

## Bitbucket Pipelines (`bitbucket-pipelines.yml`)

```yaml
image: node:20

pipelines:
  default:
    - step:
        name: Build and test
        caches: [node]
        script:
          - npm ci
          - npm run build
          - npm test
        artifacts: [dist/**]
  branches:
    main:
      - step:
          name: Deploy
          deployment: production
          script:
            - ./deploy.sh
```

Runners: Atlassian-hosted (limited monthly minutes per plan) or self-hosted.

## Commit statuses (CI → PR integration)

```
POST /repositories/{w}/{r}/commit/{sha}/statuses/build
     { key: "build", state: "SUCCESSFUL"|"INPROGRESS"|"FAILED", url, description }
```

Drives green/red icons on PRs.

## Pagination

All list endpoints use cursor pagination: response contains `next: "url"`. Follow until absent.

## Common gotchas

- **App passwords deprecated** — migrate to Workspace API tokens or repository/workspace access tokens. Check current deprecation timeline in Atlassian's announcements.
- **Username vs account_id**: username is being removed across Atlassian products; use `account_id` / `uuid` for user references.
- **Pipeline variable secrets are write-only**: can't read back after setting.
- **Rate limit**: roughly 1,000 req/hour per app/user; 429s include `Retry-After`. GraphQL-style `?fields=+...` expansion may help lower request count.
- **PR diff endpoint is paginated too** — very large PRs require following `next` links or using the raw `diff` endpoint.

## Key reference URLs

- REST API reference: https://developer.atlassian.com/cloud/bitbucket/rest/intro/
- Pull requests: https://developer.atlassian.com/cloud/bitbucket/rest/api-group-pullrequests/
- Webhooks: https://support.atlassian.com/bitbucket-cloud/docs/manage-webhooks/
- Pipelines YAML: https://support.atlassian.com/bitbucket-cloud/docs/bitbucket-pipelines-configuration-reference/
- Auth migration: https://developer.atlassian.com/cloud/bitbucket/authentication-methods/
