# Ember Devbox Security Audit (Semantic Scan)

**Date:** 2026-04-01
**Scope:** `ember/devbox/src/` -- all backend source files
**Auditor:** Claude Opus 4.6 (semantic analysis)

---

## 1. MISSING_AUTH

### Finding 1.1 -- Reconcile endpoint accessible without secret

- **File:** `ember/devbox/src/routes/billing.ts` line 721-726
- **Pattern:** MISSING_AUTH
- **Severity:** CRITICAL
- **Issue:** The `/api/billing/reconcile` endpoint only checks `if (secret && auth !== ...)`. When `RECONCILE_SECRET` is unset (falsy), the condition short-circuits and the endpoint is fully open to unauthenticated callers. Any anonymous request can trigger a full reconciliation pass including Stripe API calls.
- **Fix:** Invert the guard: if `!secret`, return 500 ("RECONCILE_SECRET not configured") unconditionally. Then check the bearer token.

```ts
if (!secret) return c.json({ error: "Reconcile not configured" }, 500);
if (auth !== `Bearer ${secret}`) return c.json({ error: "Unauthorized" }, 401);
```

### Finding 1.2 -- GET /v1/sessions/:id is public (no auth)

- **File:** `ember/devbox/src/routes/sessions.ts` line 69-86
- **Pattern:** MISSING_AUTH
- **Severity:** MEDIUM
- **Issue:** The `GET /v1/sessions/:id` endpoint has no auth middleware. Any caller who can guess or enumerate a nanoid(12) session ID can read Stoke session data including tasks, cost, and worker count. The comment says "public for shareable links" but there is no explicit opt-in by the user to make a session shareable.
- **Fix:** Either add `requireApiKeyV1()` to enforce auth, or add an explicit `public` boolean column on `stoke_sessions` so only user-chosen sessions are shareable. At minimum, redact `total_cost_usd` from public responses.

### Finding 1.3 -- Machine startup script endpoint trusts param without ownership check

- **File:** `ember/devbox/src/routes/machines.ts` line 510-527
- **Pattern:** MISSING_AUTH
- **Severity:** MEDIUM
- **Issue:** The `GET /:id/startup` endpoint authenticates via machine token but only validates that the `:id` param matches `machine.id` OR `machine.flyAppName`. This is correct in practice (the token uniquely binds to one machine), but the pattern of accepting two different ID types in a single param is fragile. If another machine row ever shared a flyAppName due to a bug, the wrong startup script could be returned.
- **Fix:** Tighten validation: compare `:id` against exactly the machine found by token. The current logic is safe as long as `flyAppName` has a UNIQUE constraint (it does in the schema), so this is LOW risk, but documenting the invariant inline would help.

### Finding 1.4 -- /api/account/verify-email has no auth

- **File:** `ember/devbox/src/routes/account.ts` line 133-155
- **Pattern:** MISSING_AUTH
- **Severity:** LOW
- **Issue:** `POST /api/account/verify-email` is unauthenticated by design (token-based). This is standard and acceptable, but the endpoint does not rate-limit token verification attempts. A brute-force attacker could attempt many tokens. The 64-hex-char token space (2^256) makes this practically infeasible, but adding rate limiting is defense-in-depth.
- **Fix:** Add the `authLimiter` middleware to this route.

---

## 2. SECURITY

### Finding 2.1 -- SQL injection via raw interval interpolation

- **File:** `ember/devbox/src/routes/ai.ts` line 156-162
- **Pattern:** SECURITY
- **Severity:** HIGH
- **Issue:** The `interval` variable is constructed from user-controlled `period` query param and then interpolated into SQL: `AND created_at > NOW() - ${interval}::interval`. While `period` is validated against three string values ("day", "week", "month"), the default `interval = "30 days"` path means any unrecognized period value falls through to "30 days". However, the pattern of interpolating a string with `::interval` cast is fragile. If someone adds a new period value and makes a typo (e.g., setting `interval = period`), it becomes direct SQL injection.
- **Fix:** Use a whitelist map that returns only known constants, and throw/400 on unknown period values:

```ts
const INTERVALS: Record<string, string> = { day: "1 day", week: "7 days", month: "30 days" };
const interval = INTERVALS[period];
if (!interval) return c.json({ error: "Invalid period" }, 400);
```

### Finding 2.2 -- CSRF bypass: no-origin POST with valid session cookie

- **File:** `ember/devbox/src/middleware.ts` line 77-83
- **Pattern:** SECURITY
- **Severity:** HIGH
- **Issue:** CSRF protection allows requests with no `Origin` header if a valid session cookie is present. This means any request from contexts that don't send Origin (e.g., `<form>` submissions from same-origin, some browser extensions, or server-side forged requests via embedded browsers) bypass CSRF checks entirely as long as the session cookie is attached. A classic CSRF from an HTML form POST submitted from a third-party site typically sends an Origin header, but not all browsers do so consistently, and some intermediary configurations strip it.
- **Fix:** When `Origin` is absent on mutating requests, also require a custom header (e.g., `X-Requested-With`) that forms cannot set. Or reject all requests without an Origin header:

```ts
if (!origin) return c.json({ error: "Missing Origin header" }, 403);
```

### Finding 2.3 -- Filename header injection in file upload proxy

- **File:** `ember/devbox/src/fly.ts` line 378-380
- **Pattern:** SECURITY
- **Severity:** HIGH
- **Issue:** `uploadFileToMachine` passes `filename` as an HTTP header `X-Filename` without sanitization. The filename comes from user-uploaded form data (`file.name`). A crafted filename containing `\r\n` could inject additional HTTP headers (HTTP header injection / response splitting) depending on the HTTP client implementation.
- **Fix:** Sanitize the filename before passing it as a header value. Strip any `\r`, `\n`, and non-printable characters:

```ts
const safeFilename = filename.replace(/[\r\n\x00-\x1f]/g, "");
```

### Finding 2.4 -- Org name passed directly into GitHub API URL

- **File:** `ember/devbox/src/routes/github.ts` line 165
- **Pattern:** SECURITY
- **Severity:** MEDIUM
- **Issue:** The `org` route parameter is interpolated directly into a fetch URL: `` `https://api.github.com/orgs/${org}/repos` ``. While URL path injection is limited (slashes are not URL-encoded by template literals), a malicious org name like `../../users/admin` could potentially change the API path. In practice, GitHub's API routing would reject this, but the pattern is unsafe.
- **Fix:** Validate `org` matches `^[a-zA-Z0-9_-]+$` or use `encodeURIComponent(org)`.

### Finding 2.5 -- OAuth state cookie not cleared after consumption

- **File:** `ember/devbox/src/routes/auth.ts` lines 124-224, 236-299
- **Pattern:** SECURITY
- **Severity:** LOW
- **Issue:** After validating the OAuth state parameter in GitHub and Google callbacks, the `github_oauth_state`, `google_oauth_state`, and `google_code_verifier` cookies are not cleared. This allows the browser to replay the same state value. Since the code is single-use at the OAuth provider, this is not directly exploitable, but clearing state cookies after use is a best practice.
- **Fix:** Add `setCookie(c, "github_oauth_state", "", { ...cookieOpts, maxAge: 0 })` after state validation.

---

## 3. RACE_CONDITION

### Finding 3.1 -- Stripe customer creation race between concurrent requests

- **File:** `ember/devbox/src/routes/billing.ts` lines 128-139
- **Pattern:** RACE_CONDITION
- **Severity:** HIGH
- **Issue:** The create-customer flow does: read user -> if no `stripe_customer_id` -> `stripe.customers.create` -> `UPDATE users SET stripe_customer_id WHERE stripe_customer_id IS NULL`. If two concurrent checkout requests both see `stripe_customer_id = NULL`, both call `stripe.customers.create`, creating duplicate Stripe customers. The `WHERE stripe_customer_id IS NULL` prevents overwrite, but the loser's Stripe customer becomes an orphan. The subsequent re-read mitigates the functional issue, but orphan customers accumulate in Stripe.
- **Fix:** Use `SELECT ... FOR UPDATE` or a DB advisory lock before the Stripe customer creation:

```ts
await rawSql.begin(async (tx) => {
  const [locked] = await tx`SELECT stripe_customer_id FROM users WHERE id = ${user.id} FOR UPDATE`;
  if (locked.stripe_customer_id) { customerId = locked.stripe_customer_id; return; }
  // ... create customer and update
});
```

### Finding 3.2 -- Similar race in credits checkout customer creation

- **File:** `ember/devbox/src/routes/credits.ts` lines 37-43
- **Pattern:** RACE_CONDITION
- **Severity:** HIGH
- **Issue:** Same pattern as 3.1. Customer creation in the credits checkout also does read-then-write without a lock. Concurrent credit purchase requests can create duplicate Stripe customers.
- **Fix:** Same as 3.1 -- use `SELECT ... FOR UPDATE` in a transaction.

### Finding 3.3 -- Admin credit adjustment idempotency key uses Date.now()

- **File:** `ember/devbox/src/routes/admin.ts` line 77
- **Pattern:** RACE_CONDITION
- **Severity:** MEDIUM
- **Issue:** The idempotency key for admin credit adjustments is `'admin_' + Date.now() + '_' + userId`. If two admin requests arrive at the same millisecond for the same user, they would have the same idempotency key, causing the second to fail with a unique constraint violation (which is actually good), or succeed silently (which would be bad). The `Date.now()` resolution is only 1ms, making collisions plausible under automation.
- **Fix:** Use `nanoid()` or `crypto.randomUUID()` as the idempotency key instead of a timestamp.

---

## 4. MISSING_VALIDATION

### Finding 4.1 -- Machine region not validated against allowed list

- **File:** `ember/devbox/src/routes/machines.ts` line 25
- **Pattern:** MISSING_VALIDATION
- **Severity:** HIGH
- **Issue:** The `createSchema` accepts any string for `region` (with only `.default("sjc")`). There is no validation that the region is a valid Fly region. An attacker could pass an arbitrary string like `us-central1` or a very long string, which would be sent to the Fly API. While Fly would reject invalid regions, this wastes API calls and the error propagation may be confusing.
- **Fix:** Use `z.enum(["sjc", "lax", "ord", "ewr", "iad", ...])` with the allowed Fly regions, or at minimum `z.string().regex(/^[a-z]{3}$/).max(5)`.

### Finding 4.2 -- Worker size validated against cost map but no whitelist in schema

- **File:** `ember/devbox/src/routes/workers.ts` line 29
- **Pattern:** MISSING_VALIDATION
- **Severity:** LOW
- **Issue:** `createWorkerSchema` accepts `size: z.string().default("performance-4x")` -- any string. The cost lookup on line 49 rejects unknown sizes, which is correct. But the schema should enforce the whitelist upfront for clearer error messages.
- **Fix:** Use `z.enum(["performance-4x", "performance-8x", "performance-16x"])`.

### Finding 4.3 -- API key label not validated for length or content

- **File:** `ember/devbox/src/routes/api-keys.ts` line 27
- **Pattern:** MISSING_VALIDATION
- **Severity:** MEDIUM
- **Issue:** The `label` for API keys is taken directly from the request body with only a fallback to "default". There is no length limit or content validation. A user could submit a multi-megabyte label string, which would be stored in the database and returned in list responses.
- **Fix:** Add validation: `const label = String((body as any).label || "default").slice(0, 100);` or use a Zod schema.

### Finding 4.4 -- Stoke session tasks array accepts z.any() elements

- **File:** `ember/devbox/src/routes/sessions.ts` line 47
- **Pattern:** MISSING_VALIDATION
- **Severity:** MEDIUM
- **Issue:** `tasks: z.array(z.any()).optional()` in the update schema accepts any JSON structure. An attacker could send enormous nested objects (e.g., 100MB of JSON) that would be stored as JSONB and served back on the public GET endpoint (Finding 1.2). No size bound on the serialized JSON.
- **Fix:** Add `.max(1000)` on the array and constrain element size, or add a byte-length check on `JSON.stringify(tasks)` (e.g., max 1MB).

### Finding 4.5 -- Missing password max-length on register and reset

- **File:** `ember/devbox/src/routes/auth.ts` line 27, `ember/devbox/src/routes/account.ts` line 66
- **Pattern:** MISSING_VALIDATION
- **Severity:** MEDIUM
- **Issue:** Password fields use `z.string().min(8)` with no max length. Argon2 hashing of a multi-megabyte password string is a CPU-intensive operation that could be used for DoS.
- **Fix:** Add `.max(128)` or `.max(256)` to all password Zod schemas.

---

## 5. CONFIG_LEAK

### Finding 5.1 -- STRIPE_SECRET_KEY! assertion crashes at import time

- **File:** `ember/devbox/src/routes/billing.ts` line 14, `ember/devbox/src/routes/credits.ts` line 11, `ember/devbox/src/routes/account.ts` line 15
- **Pattern:** CONFIG_LEAK
- **Severity:** HIGH
- **Issue:** `new Stripe(process.env.STRIPE_SECRET_KEY!)` uses a non-null assertion. If `STRIPE_SECRET_KEY` is unset, this passes `undefined` to the Stripe constructor, which may crash or produce confusing errors at request time rather than startup. The `validateProductionConfig` in `index.ts` checks this in production, but in development/staging, missing Stripe keys will cause runtime crashes on billing routes.
- **Fix:** Either validate at module load (`if (!process.env.STRIPE_SECRET_KEY) throw new Error(...)`) or use a lazy getter that throws a clear error.

### Finding 5.2 -- CANONICAL_IMAGE_DIGEST! used without fallback

- **File:** `ember/devbox/src/routes/machines.ts` line 65
- **Pattern:** CONFIG_LEAK
- **Severity:** HIGH
- **Issue:** `process.env.CANONICAL_IMAGE_DIGEST!` will be `undefined` if unset. This value is passed to the Fly API as the Docker image reference. In non-production environments not covered by `validateProductionConfig`, this would create machines with an undefined image, causing Fly provisioning errors with confusing error messages.
- **Fix:** Validate at module load or before use:

```ts
const imageDigest = process.env.CANONICAL_IMAGE_DIGEST;
if (!imageDigest) return c.json({ error: "Server misconfiguration: no image digest" }, 500);
```

### Finding 5.3 -- DATABASE_URL! crashes without message

- **File:** `ember/devbox/src/connection.ts` line 7
- **Pattern:** CONFIG_LEAK
- **Severity:** HIGH
- **Issue:** `postgres(process.env.DATABASE_URL!)` will pass `undefined` to the postgres client if `DATABASE_URL` is not set. The resulting error message from the postgres library is opaque. Production validation catches this, but dev/staging environments would get an unhelpful crash.
- **Fix:** Add a guard: `if (!process.env.DATABASE_URL) throw new Error("DATABASE_URL is required");`

### Finding 5.4 -- OPENROUTER_API_KEY defaults to empty string silently

- **File:** `ember/devbox/src/routes/ai.ts` line 24
- **Pattern:** CONFIG_LEAK
- **Severity:** LOW
- **Issue:** `OPENROUTER_KEY` defaults to `""`. The route checks `if (!OPENROUTER_KEY)` and returns 503, which is correct. But it's read once at module load, so changing the env var at runtime won't take effect. This is acceptable but worth noting.
- **Fix:** None required. Current behavior is correct.

---

## 6. MISSING_ERROR_HANDLING

### Finding 6.1 -- GitHub API calls in OAuth flow have no timeout

- **File:** `ember/devbox/src/routes/auth.ts` lines 135-145, 248-251
- **Pattern:** MISSING_ERROR_HANDLING
- **Severity:** MEDIUM
- **Issue:** The `fetch` calls to `api.github.com/user` and `api.github.com/user/emails` during OAuth callback have no `AbortSignal.timeout()`. If GitHub's API hangs, the request will hang indefinitely, consuming a connection.
- **Fix:** Add `signal: AbortSignal.timeout(10000)` to all external fetch calls in OAuth flows.

### Finding 6.2 -- GitHub API calls in github.ts routes have no timeout

- **File:** `ember/devbox/src/routes/github.ts` lines 43-45, 125-127, 150-152, 165-167
- **Pattern:** MISSING_ERROR_HANDLING
- **Severity:** MEDIUM
- **Issue:** Same as 6.1. All `fetch("https://api.github.com/...")` calls in the GitHub routes lack timeouts.
- **Fix:** Add `signal: AbortSignal.timeout(10000)` to all fetch calls.

### Finding 6.3 -- Fly API calls have no timeout

- **File:** `ember/devbox/src/fly.ts` lines 54, 80
- **Pattern:** MISSING_ERROR_HANDLING
- **Severity:** MEDIUM
- **Issue:** `flyApi` and `flyGraphQL` functions do not set timeouts on fetch calls. If the Fly API hangs, machine creation, deletion, and reconciliation will hang indefinitely.
- **Fix:** Add `signal: AbortSignal.timeout(15000)` to the fetch calls in `flyApi` and `flyGraphQL`.

### Finding 6.4 -- Resend email API call has no timeout

- **File:** `ember/devbox/src/email.ts` line 27
- **Pattern:** MISSING_ERROR_HANDLING
- **Severity:** LOW
- **Issue:** The fetch to `api.resend.com/emails` has no timeout. A hanging email API would block password reset and email verification flows.
- **Fix:** Add `signal: AbortSignal.timeout(10000)`.

### Finding 6.5 -- JSON parse of layoutConfig has silent catch

- **File:** `ember/devbox/src/routes/settings.ts` line 69
- **Pattern:** MISSING_ERROR_HANDLING
- **Severity:** LOW
- **Issue:** `JSON.parse(row.layoutConfig)` is wrapped in try/catch returning `null`, which is correct. No issue here.

---

## 7. WRONG_ERROR_RESPONSE

### Finding 7.1 -- OpenRouter error status forwarded directly to client

- **File:** `ember/devbox/src/routes/ai.ts` line 68
- **Pattern:** WRONG_ERROR_RESPONSE
- **Severity:** MEDIUM
- **Issue:** `return c.json({ error: ... }, orRes.status as any)` forwards the upstream OpenRouter HTTP status code directly to the API client. If OpenRouter returns a 500, the client sees a 500 from Ember. If it returns a 401 (our API key is bad), the client sees a 401, which they might interpret as their own API key being invalid. Upstream errors should be mapped to 502 (Bad Gateway).
- **Fix:** Map upstream errors to 502:

```ts
return c.json({ error: `AI provider error (${orRes.status})` }, 502);
```

### Finding 7.2 -- Webhook returns 500 on processing error

- **File:** `ember/devbox/src/routes/billing.ts` line 537
- **Pattern:** WRONG_ERROR_RESPONSE
- **Severity:** MEDIUM
- **Issue:** When webhook processing throws, the endpoint returns 500. Stripe will retry 500 responses. If the error is persistent (e.g., a bug in `syncSubscription`), Stripe will keep retrying for up to 72 hours, creating noise. For idempotent events that have partially processed, re-delivery could cause issues.
- **Fix:** Consider returning 200 with error details logged, and rely on the reconciler to catch drift. Or return 200 only for known-permanent errors (e.g., unknown user) and 500 for transient errors (DB timeout).

### Finding 7.3 -- GitHub repos endpoint returns error with 200 status

- **File:** `ember/devbox/src/routes/github.ts` line 123
- **Pattern:** WRONG_ERROR_RESPONSE
- **Severity:** LOW
- **Issue:** `GET /api/github/repos` returns `{ error: "Connect GitHub first", needsConnect: true, repos: [] }` with a 200 status. The client has to check for the `error` field rather than relying on HTTP status. Similarly for orgs endpoints.
- **Fix:** Return appropriate 4xx status (e.g., 403 or 401) when GitHub is not connected.

---

## Summary

| Pattern | Critical | High | Medium | Low | Total |
|---------|----------|------|--------|-----|-------|
| MISSING_AUTH | 1 | 0 | 2 | 1 | 4 |
| SECURITY | 0 | 3 | 1 | 1 | 5 |
| RACE_CONDITION | 0 | 2 | 1 | 0 | 3 |
| MISSING_VALIDATION | 0 | 1 | 3 | 1 | 5 |
| CONFIG_LEAK | 0 | 3 | 0 | 1 | 4 |
| MISSING_ERROR_HANDLING | 0 | 0 | 3 | 1 | 4 |
| WRONG_ERROR_RESPONSE | 0 | 0 | 2 | 1 | 3 |
| **Total** | **1** | **9** | **12** | **6** | **28** |

### Top Priority Fixes (CRITICAL + HIGH)

1. **[CRITICAL] Finding 1.1** -- Reconcile endpoint open without RECONCILE_SECRET
2. **[HIGH] Finding 2.2** -- CSRF bypass for no-origin requests with session cookie
3. **[HIGH] Finding 2.3** -- HTTP header injection via upload filename
4. **[HIGH] Finding 2.1** -- SQL interval interpolation fragility in AI usage
5. **[HIGH] Finding 3.1** -- Stripe customer creation race (billing)
6. **[HIGH] Finding 3.2** -- Stripe customer creation race (credits)
7. **[HIGH] Finding 4.1** -- Machine region not validated
8. **[HIGH] Finding 5.1** -- STRIPE_SECRET_KEY! undefined in non-prod
9. **[HIGH] Finding 5.2** -- CANONICAL_IMAGE_DIGEST! undefined in non-prod
10. **[HIGH] Finding 5.3** -- DATABASE_URL! undefined in non-prod
