# github

> GitHub REST API v2022-11-28 + GraphQL. GitHub Apps for server-to-server (installation tokens), fine-grained PATs for scripts, OAuth for user-auth flows. Always send `X-GitHub-Api-Version: 2022-11-28`.

<!-- keywords: github, github api, octokit, github app, installation token, webhook, github webhook, pull request, issue, graphql -->

**Official docs:** https://docs.github.com/en/rest  |  **Verified:** 2026-04-14 via web search.

## Auth options (pick the right one)

| Use case | Token type | Notes |
|---|---|---|
| Server-side integration with your own org/repos | **GitHub App** + installation token | Fine-grained perms, short-lived tokens, audit trail. Preferred. |
| Scripts / CLI / CI with narrow access | Fine-grained PAT | Tenant-scoped, expiring. |
| User login to your app | OAuth App | User authorizes; you get user token with their scopes. |
| Legacy / broad access | Classic PAT (`ghp_...`) | Discouraged; global scopes, no expiry by default. |

## Request shape (all endpoints)

```
GET /repos/{owner}/{repo}/issues
Authorization: Bearer ghs_...                  # or ghp_... / installation token
Accept: application/vnd.github+json
X-GitHub-Api-Version: 2022-11-28
```

Octokit SDK handles headers automatically. REST base: `https://api.github.com`.

## GitHub App → installation token flow

```js
// Step 1: mint a JWT as the App (RS256 with your App's private key; iss = App ID; exp = now + 10min)
const appJwt = jwt.sign({ iat: now, exp: now + 600, iss: APP_ID }, privateKeyPem, { algorithm: "RS256" });

// Step 2: trade JWT for an installation access token (1-hour TTL)
const resp = await fetch(`https://api.github.com/app/installations/${INSTALLATION_ID}/access_tokens`, {
  method: "POST",
  headers: { Authorization: `Bearer ${appJwt}`, Accept: "application/vnd.github+json" },
});
const { token: installationToken } = await resp.json();

// Step 3: use installationToken for actual API calls. Cache for its 1h lifetime, re-mint on expiry.
```

Or use Octokit:

```js
import { App } from "@octokit/app";
const app = new App({ appId: APP_ID, privateKey });
const octokit = await app.getInstallationOctokit(INSTALLATION_ID);
await octokit.rest.issues.list({ owner, repo });
```

## Common operations

```js
import { Octokit } from "@octokit/rest";
const octokit = new Octokit({ auth: token });

// List issues
const { data } = await octokit.rest.issues.listForRepo({ owner, repo, state: "open" });

// Create PR
await octokit.rest.pulls.create({ owner, repo, title, head: "feature-branch", base: "main", body });

// Add comment
await octokit.rest.issues.createComment({ owner, repo, issue_number, body });

// Create/update file
await octokit.rest.repos.createOrUpdateFileContents({
  owner, repo, path: "README.md",
  message: "update readme",
  content: Buffer.from(newContent).toString("base64"),
  sha: existingSha,     // required when updating existing file
});
```

## Webhooks (repository or org events)

Configure in repo/org/app settings → Webhooks. Events: `push`, `pull_request`, `issues`, `issue_comment`, `workflow_run`, etc.

Verify signature:

```js
import crypto from "crypto";
const sig = req.headers["x-hub-signature-256"];  // "sha256=..."
const expected = "sha256=" + crypto.createHmac("sha256", WEBHOOK_SECRET).update(rawBody).digest("hex");
if (!crypto.timingSafeEqual(Buffer.from(sig), Buffer.from(expected))) return 401;
```

The `X-GitHub-Event` header tells you the event type; `X-GitHub-Delivery` is a unique ID for dedup.

## Rate limits

- Authenticated: 5000/hour (PAT, OAuth), 15000/hour (GitHub App installation).
- Unauthenticated: 60/hour.
- Secondary rate limits kick in for abusive patterns (many concurrent requests, tight polling).
- Response includes `X-RateLimit-Remaining` + `X-RateLimit-Reset`. Respect 429s with `Retry-After`.

## GraphQL API

`POST https://api.github.com/graphql` with `{ query, variables }`. Much fewer requests for deep data (e.g. "list PRs with their last review state and CI status"). Use when you'd otherwise make 10+ REST calls.

## Common gotchas

- **Installation tokens expire in 1 hour** — cache with expiry tracking, re-mint proactively (5 min before).
- **Classic PATs leak easily**; if one shows up in a commit, revoke immediately and rotate.
- **Webhook secrets differ per webhook**; don't reuse across environments.
- **Pagination**: default 30, max 100; use `per_page=100` and follow `Link` header's `rel="next"` to paginate.

## Key reference URLs

- REST overview: https://docs.github.com/en/rest?apiVersion=2022-11-28
- GitHub Apps: https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app
- Webhooks: https://docs.github.com/en/webhooks
- Webhook signature verification: https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries
- Octokit.js: https://github.com/octokit/octokit.js
