# De-duplicated Audit Findings

**Date:** 2026-04-01
**Sources:** 4 semantic scans + 14 persona perspectives = 18 audit reports
**Method:** Grouped by file + issue, kept most specific version per group

## Summary Table

| Repo | Critical | High | Medium | Total |
|------|----------|------|--------|-------|
| ember | 12 | 22 | 25 | 59 |
| flare | 8 | 14 | 10 | 32 |
| stoke | 5 | 10 | 10 | 25 |
| cross | 2 | 4 | 4 | 10 |
| **Total** | **27** | **50** | **49** | **126** |

**[~250+] findings across all sources -> [126] unique findings after merging.**

---

## EMBER Findings

### EMBER-001: Reconcile endpoint open when RECONCILE_SECRET is unset
- **Severity**: CRITICAL
- **File**: ember/devbox/src/routes/billing.ts:721-726
- **Sources**: ember-security-semantic, lead-security, sneaky-finder
- **Issue**: The `/api/billing/reconcile` endpoint checks `if (secret && auth !== ...)`. When `RECONCILE_SECRET` is unset (falsy), the condition short-circuits and the endpoint is fully open to unauthenticated callers. Any anonymous request can trigger a full reconciliation pass including Stripe API calls.
- **Fix**: Invert the guard: `if (!secret) return c.json({ error: "Reconcile not configured" }, 503);` unconditionally before checking the bearer token.
- **Effort**: trivial

### EMBER-002: CSRF bypass when Origin header absent but session cookie exists
- **Severity**: HIGH
- **File**: ember/devbox/src/middleware.ts:77-83
- **Sources**: ember-security-semantic, lead-security
- **Issue**: CSRF protection allows requests without an `Origin` header if a valid session cookie is present. Form submissions from `<form method="POST">` do NOT include an Origin header in some browsers. An attacker can craft a cross-origin form that posts to the API and the browser will attach the session cookie, bypassing CSRF protection entirely.
- **Fix**: Require Origin header on ALL state-changing requests. For same-origin non-fetch requests, the frontend should switch to `fetch()` which always sends Origin. Alternatively, also require a custom header (e.g., `X-Requested-With`) when Origin is absent.
- **Effort**: small

### EMBER-003: HTTP header injection via upload filename
- **Severity**: HIGH
- **File**: ember/devbox/src/fly.ts:378-380
- **Sources**: ember-security-semantic, lead-security
- **Issue**: `uploadFileToMachine` passes `filename` as HTTP header `X-Filename` without sanitization. The filename comes from user-uploaded form data. A crafted filename containing `\r\n` could inject additional HTTP headers (HTTP header injection / response splitting).
- **Fix**: Sanitize the filename: `const safeFilename = filename.replace(/[\r\n\x00-\x1f]/g, "");` Also strip path separators and limit length.
- **Effort**: trivial

### EMBER-004: SQL interval interpolation fragility in AI usage endpoint
- **Severity**: HIGH
- **File**: ember/devbox/src/routes/ai.ts:156-162
- **Sources**: ember-security-semantic
- **Issue**: The `interval` variable is interpolated into SQL with `::interval` cast. While currently validated against three string values, the pattern is fragile. If someone adds a new period value and makes a typo, it becomes direct SQL injection.
- **Fix**: Use a whitelist map returning only known constants, throw 400 on unknown period values.
- **Effort**: trivial

### EMBER-005: Stripe customer creation race (billing checkout)
- **Severity**: HIGH
- **File**: ember/devbox/src/routes/billing.ts:128-139
- **Sources**: ember-security-semantic, vp-eng-idempotency
- **Issue**: The create-customer flow does read -> if no `stripe_customer_id` -> `stripe.customers.create` -> UPDATE. Two concurrent checkout requests both see null, both create Stripe customers. The loser's Stripe customer becomes an orphan.
- **Fix**: Use `SELECT ... FOR UPDATE` or a DB advisory lock before Stripe customer creation within a transaction.
- **Effort**: small

### EMBER-006: Stripe customer creation race (credits checkout)
- **Severity**: MEDIUM
- **File**: ember/devbox/src/routes/credits.ts:37-43
- **Sources**: ember-security-semantic, vp-eng-idempotency
- **Issue**: Same pattern as EMBER-005. Credits checkout also does read-then-write without a lock. Two concurrent credit purchase requests can create duplicate Stripe customers.
- **Fix**: Mirror the billing.ts pattern: use `UPDATE users SET stripe_customer_id = $1 WHERE id = $2 AND stripe_customer_id IS NULL`, then re-read. Pass `idempotencyKey: 'customer:' + user.id` to `stripe.customers.create`.
- **Effort**: trivial

### EMBER-007: Webhook idempotency check is non-atomic TOCTOU race
- **Severity**: CRITICAL
- **File**: ember/devbox/src/routes/billing.ts:403-404
- **Sources**: vp-eng-idempotency
- **Issue**: The outer guard `SELECT id FROM billing_events WHERE stripe_event_id = ...` runs OUTSIDE any transaction. With Stripe's at-least-once delivery, two concurrent handlers can both pass this check. Only the `credit_purchase` branch is protected; all other branches (invoice.paid, customer.subscription.*) are NOT.
- **Fix**: Wrap the entire webhook body in a single transaction beginning with `INSERT INTO processed_stripe_events ... ON CONFLICT DO NOTHING RETURNING event_id`; bail out if INSERT returns nothing.
- **Effort**: small

### EMBER-008: invoice.paid billing_events INSERT has no ON CONFLICT guard
- **Severity**: CRITICAL
- **File**: ember/devbox/src/routes/billing.ts:472-473
- **Sources**: vp-eng-idempotency
- **Issue**: Unlike the `checkout.session.completed` branch which uses `ON CONFLICT (stripe_event_id) DO NOTHING`, the `invoice.paid` branch inserts directly with no conflict guard. Duplicate events produce duplicate `charge` rows, corrupting billing history.
- **Fix**: Add `ON CONFLICT (stripe_event_id) DO NOTHING` to the billing_events INSERT.
- **Effort**: trivial

### EMBER-009: Stripe subscriptionItems.create called before DB slot, compensation is non-idempotent
- **Severity**: CRITICAL
- **File**: ember/devbox/src/routes/billing.ts:167-208
- **Sources**: vp-eng-idempotency
- **Issue**: If the DB transaction fails after Stripe successfully creates the subscription item, the compensation path deletes the item. On client retry, Stripe returns the cached (now-deleted) item's response from the idempotency key, and the code writes a slot pointing to a deleted Stripe item.
- **Fix**: After compensation delete, also change the retry path to re-create the intent with a new ID, breaking Stripe's idempotency key cache tie.
- **Effort**: medium

### EMBER-010: syncSubscription SELECT+UPDATE is a TOCTOU race
- **Severity**: HIGH
- **File**: ember/devbox/src/routes/billing.ts:276-299
- **Sources**: vp-eng-idempotency
- **Issue**: Two concurrent webhook events for the same subscription both pass the SELECT with `existing = undefined`, then both try to INSERT, causing a unique-constraint violation. The error is swallowed, the second event returns 500, and Stripe retries.
- **Fix**: Replace SELECT + conditional INSERT/UPDATE with a single `INSERT ... ON CONFLICT (stripe_subscription_id) DO UPDATE SET ...` UPSERT.
- **Effort**: small

### EMBER-011: Machine create has no idempotency key -- retries create duplicates
- **Severity**: HIGH
- **File**: ember/devbox/src/routes/machines.ts:55-203
- **Sources**: vp-eng-idempotency
- **Issue**: No client-supplied idempotency key for `POST /api/machines`. A retry after timeout creates a second DB row and Fly app, double-consuming slots.
- **Fix**: Accept a client-provided `requestId` or use `(user_id, name)` as a natural idempotency key.
- **Effort**: medium

### EMBER-012: Machine region not validated against allowlist
- **Severity**: HIGH
- **File**: ember/devbox/src/routes/machines.ts:25
- **Sources**: ember-security-semantic, lead-security
- **Issue**: The `createSchema` accepts any string for `region` with only `.default("sjc")`. Arbitrary strings flow into Fly API calls.
- **Fix**: Use `z.enum(["sjc", "lax", "iad", ...])` with an explicit allowlist of valid Fly regions.
- **Effort**: trivial

### EMBER-013: In-memory rate limiter is default -- ineffective across multiple instances
- **Severity**: CRITICAL
- **File**: ember/devbox/src/rate-limit.ts:26-38
- **Sources**: scaling-consultant, vp-eng-scaling, build-deploy
- **Issue**: `memStores` is a process-local `Map`. With N API instances, rate limits are per-process. A user can multiply their rate limit by N instances. Auth brute-force protection becomes 20*N attempts.
- **Fix**: Set `RATE_LIMIT_BACKEND=postgres` as default in production, or add to `validateProductionConfig()`.
- **Effort**: trivial

### EMBER-014: Postgres connection pool uses library defaults (10 connections, no timeouts)
- **Severity**: CRITICAL
- **File**: ember/devbox/src/connection.ts:7
- **Sources**: scaling-consultant, vp-eng-scaling, build-deploy
- **Issue**: `postgres(process.env.DATABASE_URL!)` uses default pool settings (max 10, no timeouts). At 100+ concurrent requests, connection starvation cascades into 500s across all routes. Also uses non-null assertion that crashes without helpful message if DATABASE_URL is unset.
- **Fix**: Configure explicit pool: `postgres(process.env.DATABASE_URL!, { max: 25, idle_timeout: 20, connect_timeout: 5 })`. Add guard: `if (!process.env.DATABASE_URL) throw new Error("DATABASE_URL is required")`.
- **Effort**: trivial

### EMBER-015: Stripe API version `as any` hides SDK mismatch
- **Severity**: CRITICAL
- **File**: ember/devbox/src/routes/account.ts:15, credits.ts:11
- **Sources**: lead-compliance, vp-eng-types
- **Issue**: `{ apiVersion: "2024-04-10" as any }` silences a compiler error. Using a stale API version string with `as any` means the SDK may silently send requests with a version the current binary doesn't understand, causing unexpected response shapes for payment-critical operations.
- **Fix**: Either upgrade `stripe` package so the version is recognized, or use `Stripe.latestApiVersion`. Remove the `as any`. Fix in both account.ts and credits.ts.
- **Effort**: trivial

### EMBER-016: `(stripeSub as any).current_period_start` risks Invalid Date in DB
- **Severity**: CRITICAL
- **File**: ember/devbox/src/routes/billing.ts:269-270
- **Sources**: lead-compliance, vp-eng-types
- **Issue**: The `as any` bypass means if Stripe SDK renames/removes the field, the access silently produces `undefined`, leading to `new Date(NaN * 1000)` stored as `"Invalid Date"` in period columns. Webhook processing continues with corrupted data.
- **Fix**: Remove `as any`. Use `stripeSub.current_period_start` directly (typed on `Stripe.Subscription` in modern SDK).
- **Effort**: trivial

### EMBER-017: Detailed health check runs 7 sequential COUNT queries with no caching
- **Severity**: HIGH
- **File**: ember/devbox/src/index.ts:165-226
- **Sources**: scaling-consultant, vp-eng-scaling
- **Issue**: Each health check hits the DB 7 times sequentially. Under load or high-frequency monitoring, this consumes connection pool capacity needed for user requests.
- **Fix**: Cache the result for 30-60 seconds or run queries in parallel with `Promise.all()`.
- **Effort**: trivial

### EMBER-018: Missing database indexes on sessions, slots, machines, purchaseIntents, exchangeCodes
- **Severity**: HIGH
- **File**: ember/devbox/src/db.ts (multiple locations)
- **Sources**: scaling-consultant
- **Issue**: `sessions` table has no index on `userId`. `slots` table has no index on `userId` or `subscriptionId`. `machines` table has no index on `userId` or `state`. `purchaseIntents` and `exchangeCodes` also lack necessary indexes. All require full table scans for common queries.
- **Fix**: Add indexes: `sessions_user_idx`, `slots_user_idx`, `slots_subscription_idx`, `machines_user_idx`, `machines_state_idx`, `purchase_intents_user_idx`, `exchange_codes_machine_idx`, `exchange_codes_expires_idx`.
- **Effort**: trivial

### EMBER-019: Postgres rate limiter runs INSERT+COUNT+DELETE per request
- **Severity**: HIGH
- **File**: ember/devbox/src/rate-limit.ts:60-111
- **Sources**: scaling-consultant
- **Issue**: At 120 req/s, that's 240-360 DB operations/second just for rate limiting. The `rate_limit_hits` table grows unbounded during spikes; cleanup is async and best-effort.
- **Fix**: Replace with a sliding window counter using a single UPSERT per key per window bucket, or use Redis/Valkey.
- **Effort**: medium

### EMBER-020: Encryption key derived via SHA-256 (no KDF)
- **Severity**: HIGH
- **File**: ember/devbox/src/secrets.ts:20
- **Sources**: lead-security
- **Issue**: If `APP_ENCRYPTION_KEY` is not exactly 32 bytes, SHA-256 is used as a key derivation function. SHA-256 has no work factor, salt, or iteration count. A weak passphrase would be trivially brutable. This key protects GitHub OAuth tokens at rest.
- **Fix**: Use a proper KDF like HKDF, or require the key to be exactly 32 bytes and refuse to start if invalid.
- **Effort**: small

### EMBER-021: Reconcile secret comparison uses `===` (non-constant-time)
- **Severity**: HIGH
- **File**: ember/devbox/src/index.ts:172, ember/devbox/src/routes/billing.ts:723
- **Sources**: lead-security
- **Issue**: Bearer token comparison uses `===` which is vulnerable to timing attacks. These secrets gate access to operational data and billing reconciliation.
- **Fix**: Use `crypto.timingSafeEqual(Buffer.from(auth || ''), Buffer.from(\`Bearer ${secret}\`))` with length check first.
- **Effort**: trivial

### EMBER-022: Hono Variables typed as `user: any` across all route files
- **Severity**: HIGH
- **File**: ember/devbox/src/routes/billing.ts:36, account.ts:17, machines.ts:15, settings.ts:16, admin.ts:11, etc.
- **Sources**: lead-compliance, vp-eng-types, sneaky-finder
- **Issue**: All route files declare `{ Variables: { user: any; session: any } }` instead of using the correctly typed `Env` from middleware.ts. This defeats all downstream type checking. Settings.ts has a double-cast `(c as any).get("user") as any` that is especially egregious.
- **Fix**: Extract the `Env` type from middleware.ts into a shared `types.ts`, import in every route file. Remove all `as any` casts on user.
- **Effort**: small

### EMBER-023: flyApi returns `Promise<any>` with no typed responses
- **Severity**: HIGH
- **File**: ember/devbox/src/fly.ts:53-75
- **Sources**: vp-eng-types, vp-eng-comments, sneaky-finder
- **Issue**: `flyApi` returns `Promise<any>`. Every caller accesses properties with no type safety. Non-JSON responses are silently treated as `{ ok: true }`, meaning the caller thinks operations succeeded when they failed (e.g., Fly returns an HTML 502 error page).
- **Fix**: Define typed response interfaces for Fly API objects. Make `flyApi<T>` generic. For non-JSON responses, throw or return an error discriminant rather than `{ ok: true }`.
- **Effort**: medium

### EMBER-024: Admin credit adjustment idempotency key uses Date.now()
- **Severity**: MEDIUM
- **File**: ember/devbox/src/routes/admin.ts:77
- **Sources**: ember-security-semantic
- **Issue**: Idempotency key is `'admin_' + Date.now() + '_' + userId`. Date.now() has 1ms resolution, making collisions plausible under automation.
- **Fix**: Use `nanoid()` or `crypto.randomUUID()` as the idempotency key.
- **Effort**: trivial

### EMBER-025: GET /v1/sessions/:id is public with no auth
- **Severity**: MEDIUM
- **File**: ember/devbox/src/routes/sessions.ts:69-86
- **Sources**: ember-security-semantic
- **Issue**: Any caller who can guess a nanoid(12) session ID can read Stoke session data including tasks, cost, and worker count. The comment says "public for shareable links" but there is no user opt-in.
- **Fix**: Add `requireApiKeyV1()` or add a `public` boolean column so only user-chosen sessions are shareable. At minimum, redact `total_cost_usd`.
- **Effort**: small

### EMBER-026: Stoke session tasks array accepts z.any() elements
- **Severity**: MEDIUM
- **File**: ember/devbox/src/routes/sessions.ts:47
- **Sources**: ember-security-semantic, vp-eng-types
- **Issue**: `tasks: z.array(z.any()).optional()` accepts any JSON structure with no size bound. An attacker could send enormous nested objects stored as JSONB.
- **Fix**: Add `.max(1000)` on the array and constrain element size, or add a byte-length check (max 1MB). Define a minimal `taskSchema`.
- **Effort**: small

### EMBER-027: Missing password max-length on register and reset
- **Severity**: MEDIUM
- **File**: ember/devbox/src/routes/auth.ts:27, ember/devbox/src/routes/account.ts:66
- **Sources**: ember-security-semantic
- **Issue**: Password fields use `z.string().min(8)` with no max. Argon2 hashing of a multi-megabyte password is CPU-intensive DoS.
- **Fix**: Add `.max(128)` or `.max(256)` to all password Zod schemas.
- **Effort**: trivial

### EMBER-028: API key label not validated for length or content
- **Severity**: MEDIUM
- **File**: ember/devbox/src/routes/api-keys.ts:27
- **Sources**: ember-security-semantic, lead-compliance
- **Issue**: The `label` for API keys has no length limit or content validation. A user could submit a multi-megabyte label.
- **Fix**: Add a Zod schema: `z.object({ label: z.string().max(100).default("default") })`.
- **Effort**: trivial

### EMBER-029: GitHub API calls have no timeout
- **Severity**: MEDIUM
- **File**: ember/devbox/src/routes/auth.ts:135-145, ember/devbox/src/routes/github.ts:43-167
- **Sources**: ember-security-semantic
- **Issue**: All `fetch("https://api.github.com/...")` calls in OAuth callbacks and GitHub routes lack timeouts. A hanging GitHub API blocks requests indefinitely.
- **Fix**: Add `signal: AbortSignal.timeout(10000)` to all external fetch calls.
- **Effort**: trivial

### EMBER-030: Fly API calls have no timeout
- **Severity**: MEDIUM
- **File**: ember/devbox/src/fly.ts:54,80
- **Sources**: ember-security-semantic
- **Issue**: `flyApi` and `flyGraphQL` do not set timeouts on fetch calls. Hanging Fly API blocks machine operations indefinitely.
- **Fix**: Add `signal: AbortSignal.timeout(15000)` to the fetch calls.
- **Effort**: trivial

### EMBER-031: OpenRouter error status forwarded directly to client
- **Severity**: MEDIUM
- **File**: ember/devbox/src/routes/ai.ts:68
- **Sources**: ember-security-semantic, lead-compliance
- **Issue**: `c.json({ error: ... }, orRes.status as any)` forwards upstream HTTP status directly. If OpenRouter returns 401, the client thinks their own API key is invalid. The `as any` also risks malformed HTTP responses.
- **Fix**: Map upstream errors to 502: `return c.json({ error: \`AI provider error (${orRes.status})\` }, 502)`.
- **Effort**: trivial

### EMBER-032: Webhook returns 500 on processing error (Stripe retries indefinitely)
- **Severity**: MEDIUM
- **File**: ember/devbox/src/routes/billing.ts:537
- **Sources**: ember-security-semantic
- **Issue**: Persistent errors cause Stripe to retry for 72 hours. For idempotent events that have partially processed, re-delivery could cause issues.
- **Fix**: Return 200 for known-permanent errors and 500 only for transient errors.
- **Effort**: small

### EMBER-033: AI usage metering silently loses cost record on SSE parse failure
- **Severity**: CRITICAL
- **File**: ember/devbox/src/routes/ai.ts:116
- **Sources**: sneaky-finder
- **Issue**: In the SSE stream `flush()` handler, JSON parse failure in `catch {}` silently discards the final usage data. The INSERT into `ai_usage` never fires, so the user gets free API calls.
- **Fix**: Add logging and attempt partial match fallback for `total_cost`.
- **Effort**: small

### EMBER-034: Machine stop/delete non-idempotent -- partial failure leaves inconsistent state
- **Severity**: HIGH
- **File**: ember/devbox/src/routes/machines.ts:248-290
- **Sources**: vp-eng-idempotency
- **Issue**: `POST /:id/stop`: After `fly.stopMachine()` succeeds but before DB update, a crash leaves DB as `started`. Retry redundantly stops. `DELETE /:id`: If `fly.destroyApp()` succeeds but DB update fails, client retry gets 404. The failure path also doesn't release `slot_id`, leaving the slot permanently consumed.
- **Fix**: For stop, guard with `WHERE state = 'started'`. For delete, null out `slot_id` on failure path. Accept 404 on retry as success for DELETE.
- **Effort**: small

### EMBER-035: Fly provisioning failure partial-cleanup is non-atomic
- **Severity**: HIGH
- **File**: ember/devbox/src/routes/machines.ts:186-201
- **Sources**: vp-eng-idempotency
- **Issue**: If the process crashes between Fly failing and the `UPDATE machines SET slot_id = NULL` statement, the machine row stays in `creating` with slot consumed forever. No crash recovery exists.
- **Fix**: Add a background job to mark machines stuck in `creating` for more than N minutes as `error` and release the slot.
- **Effort**: medium

### EMBER-036: No .env.example file -- 15+ env vars undocumented
- **Severity**: CRITICAL
- **File**: ember/devbox/ (project-level)
- **Sources**: build-deploy
- **Issue**: Ember requires 15+ env vars in production. No `.env.example` or template exists. A new developer must read every source file to discover required vars.
- **Fix**: Create `.env.example` with all required vars and comments, extracted from `validateProductionConfig()`.
- **Effort**: trivial

### EMBER-037: Stripe price IDs use non-null assertions without dev-mode fallback
- **Severity**: HIGH
- **File**: ember/devbox/src/routes/billing.ts:17-19
- **Sources**: build-deploy, ember-security-semantic
- **Issue**: `STRIPE_PRICE_4X!`, `STRIPE_PRICE_8X!`, `STRIPE_PRICE_16X!` and `STRIPE_SECRET_KEY!` are undefined in non-prod. Cryptic Stripe API errors result.
- **Fix**: Validate at module load or add dev-mode early error.
- **Effort**: trivial

### EMBER-038: CANONICAL_IMAGE_DIGEST! undefined in non-prod
- **Severity**: HIGH
- **File**: ember/devbox/src/routes/machines.ts:65
- **Sources**: ember-security-semantic
- **Issue**: `process.env.CANONICAL_IMAGE_DIGEST!` will be `undefined` if unset, passed to Fly API as Docker image reference. Causes confusing Fly provisioning errors.
- **Fix**: Validate at module load or before use with explicit 500 error.
- **Effort**: trivial

### EMBER-039: Dockerfile has no release_command for migrations
- **Severity**: HIGH
- **File**: ember/devbox/fly.toml
- **Sources**: build-deploy
- **Issue**: The Dockerfile does not run migrations. fly.toml has no `[deploy.release_command]`. Schema changes require manual migration.
- **Fix**: Add `[deploy] release_command = "npx tsx src/migrate.ts"` to fly.toml.
- **Effort**: trivial

### EMBER-040: Dashboard start/stop/delete have no error feedback
- **Severity**: CRITICAL
- **File**: ember/devbox/web/src/pages/Dashboard.tsx:117-119
- **Sources**: lead-ux, sneaky-finder
- **Issue**: `doStart`, `doStop`, and `doDel` fire async calls with no try/catch and no user-visible error state. Failures are completely silent. Delete failure leaves machine in error state with no explanation.
- **Fix**: Wrap in try/catch, surface toast or inline error messages.
- **Effort**: small

### EMBER-041: handleBuySlot billing state leak freezes button permanently
- **Severity**: CRITICAL
- **File**: ember/devbox/web/src/pages/NewMachineModal.tsx:67-92
- **Sources**: lead-ux
- **Issue**: If the Stripe redirect path is reached, `setBuyingSlot(false)` is never called. On any page re-visit, the button is permanently frozen.
- **Fix**: Call `setBuyingSlot(false)` before `safeRedirect`.
- **Effort**: small

### EMBER-042: 401 redirect loop for /verify-email
- **Severity**: CRITICAL
- **File**: ember/devbox/web/src/lib/api.ts:22-29
- **Sources**: lead-ux
- **Issue**: The `api()` helper redirects to `/login` on any 401. VerifyEmail.tsx is unprotected but calls the API. If token is expired and backend returns 401, user is navigated away mid-verification.
- **Fix**: Add `/verify-email` to the passthrough list in the 401 guard.
- **Effort**: trivial

### EMBER-043: Credits purchase error silently swallowed
- **Severity**: HIGH
- **File**: ember/devbox/web/src/pages/Credits.tsx:35-51
- **Sources**: lead-ux
- **Issue**: `handlePurchase` catches errors but shows no message. The button just re-enables with no indication of failure.
- **Fix**: Add error state and display a message on catch.
- **Effort**: trivial

### EMBER-044: Settings error displayed with success styling (green)
- **Severity**: HIGH
- **File**: ember/devbox/web/src/pages/Settings.tsx:33-43
- **Sources**: lead-ux
- **Issue**: `saveProfile` and `saveScript` reuse the same `saved` state string. Errors are set to "Save failed" but displayed with green success styling.
- **Fix**: Add a separate `saveError` state with danger styling.
- **Effort**: trivial

### EMBER-045: Forgot-password endpoint has no rate limiting
- **Severity**: MEDIUM
- **File**: ember/devbox/src/routes/account.ts:30
- **Sources**: lead-security
- **Issue**: `/api/account/forgot-password` is not covered by any rate limiter. An attacker can spam with different emails to enumerate users or cause email flooding.
- **Fix**: Add `app.use("/api/account/forgot-password", authLimiter)`.
- **Effort**: trivial

### EMBER-046: Admin user listing has no pagination limit
- **Severity**: MEDIUM
- **File**: ember/devbox/src/routes/admin.ts:23
- **Sources**: lead-security, vp-eng-scaling
- **Issue**: Page parameter has `Math.max(1, ...)` but no upper cap. Large page numbers cause large OFFSET (DoS). Also, `GET /admin/users/:id` has unbounded `SELECT * FROM machines`.
- **Fix**: Cap page number at 1000 and add `.limit(100)` to sub-queries.
- **Effort**: small

### EMBER-047: CSRF exemption list uses exact string match (fragile)
- **Severity**: MEDIUM
- **File**: ember/devbox/src/middleware.ts:64-67
- **Sources**: lead-security
- **Issue**: `Set.has(c.req.path)` won't match paths with trailing slashes, query strings, or URL-encoded variants.
- **Fix**: Use path normalization or `startsWith()` matching.
- **Effort**: small

### EMBER-048: Stripe webhook allows requests when STRIPE_WEBHOOK_SECRET is unset
- **Severity**: MEDIUM
- **File**: ember/devbox/src/routes/billing.ts:390-398
- **Sources**: lead-security
- **Issue**: `process.env.STRIPE_WEBHOOK_SECRET!` means if env var is missing, `constructEvent` gets `undefined`. Could skip signature verification depending on SDK version.
- **Fix**: Guard: `if (!process.env.STRIPE_WEBHOOK_SECRET) return c.json({ error: "Webhook secret not configured" }, 500)`.
- **Effort**: trivial

### EMBER-049: syncSubscription slot cancellation loop is not atomic
- **Severity**: MEDIUM
- **File**: ember/devbox/src/routes/billing.ts:335-352
- **Sources**: vp-eng-idempotency
- **Issue**: Cancels slots one-by-one with individual UPDATE statements outside a transaction. A crash mid-loop leaves some slots cancelled, others active.
- **Fix**: Replace with a single `UPDATE slots SET status='cancelled' WHERE subscription_id=$1 AND stripe_subscription_item_id NOT IN (...)`.
- **Effort**: small

### EMBER-050: Terminal session creation has no deduplication
- **Severity**: MEDIUM
- **File**: ember/devbox/src/routes/machines.ts:297-346
- **Sources**: vp-eng-idempotency
- **Issue**: Double-click creates duplicate `terminal_sessions` and `exchange_codes` rows with no garbage collection.
- **Fix**: Check for unexpired exchange code for `(machine_id, user_id)` and return it if valid.
- **Effort**: small

### EMBER-051: GET /api/machines returns all non-deleted machines with no pagination
- **Severity**: HIGH
- **File**: ember/devbox/src/routes/machines.ts:40-52
- **Sources**: vp-eng-scaling
- **Issue**: As users accumulate machines, this query grows unbounded. Drizzle buffers the entire result set in memory.
- **Fix**: Add `.limit(200)` with cursor-based pagination.
- **Effort**: small

### EMBER-052: Ban handler iterates machines sequentially -- one Fly API call per machine
- **Severity**: HIGH
- **File**: ember/devbox/src/routes/admin.ts:103-128
- **Sources**: vp-eng-scaling
- **Issue**: A user with 20 machines causes 20 sequential HTTP calls, risking gateway timeout.
- **Fix**: Use `Promise.allSettled()` for parallel fan-out with per-item timeouts.
- **Effort**: small

### EMBER-053: Postgres rate limit table created lazily with DDL on first request
- **Severity**: MEDIUM
- **File**: ember/devbox/src/rate-limit.ts:60-71
- **Sources**: scaling-consultant, vp-eng-scaling
- **Issue**: `CREATE TABLE IF NOT EXISTS` fires on first request. Multiple instances starting simultaneously contend on DDL lock.
- **Fix**: Move table creation to migrations.
- **Effort**: trivial

### EMBER-054: Worker region hardcoded to "sjc"
- **Severity**: HIGH
- **File**: ember/devbox/src/routes/workers.ts:101
- **Sources**: vp-eng-completeness
- **Issue**: Burst workers are always placed in SJC with no configuration path. Non-US customers get high latency.
- **Fix**: Add `region` to `createWorkerSchema` and thread through to Fly.
- **Effort**: small

### EMBER-055: Org name passed directly into GitHub API URL
- **Severity**: MEDIUM
- **File**: ember/devbox/src/routes/github.ts:165
- **Sources**: ember-security-semantic
- **Issue**: The `org` route parameter is interpolated into a fetch URL. Path injection is limited but the pattern is unsafe.
- **Fix**: Validate `org` matches `^[a-zA-Z0-9_-]+$` or use `encodeURIComponent(org)`.
- **Effort**: trivial

### EMBER-056: Workers and Managed AI feature flags undocumented
- **Severity**: HIGH
- **File**: ember/devbox/README.md
- **Sources**: product-owner, build-deploy
- **Issue**: `ENABLE_V1_WORKERS`, `ENABLE_MANAGED_AI`, `OPENROUTER_API_KEY`, `AI_MARKUP_PERCENT`, `AI_MONTHLY_CAP_USD` appear nowhere in README. Operators deploying for stoke integration have no way to know these exist.
- **Fix**: Add all to the environment variables table in README.
- **Effort**: trivial

### EMBER-057: rawSql.begin callbacks typed as `tx: any`
- **Severity**: MEDIUM
- **File**: ember/devbox/src/routes/billing.ts:187,420,745, admin.ts:73, machines.ts:110,223
- **Sources**: vp-eng-types
- **Issue**: Transaction callbacks typed as `any` means SQL queries inside transactions are not type-checked.
- **Fix**: Import `TransactionSql` from `postgres` and type the callback parameter.
- **Effort**: trivial

### EMBER-058: GitHub API responses cast to `any` -- error bodies produce `undefined` fields
- **Severity**: MEDIUM
- **File**: ember/devbox/src/routes/auth.ts:138,144, github.ts:79,135,155,170,206,246
- **Sources**: vp-eng-types, sneaky-finder
- **Issue**: If a GitHub API call returns an error body, property accesses like `ghUser.id` silently return `undefined`, which can produce `"undefined"` strings in the DB.
- **Fix**: Define typed interfaces for GitHub user/repo objects. Add runtime guard before accessing properties.
- **Effort**: small

### EMBER-059: encryptSecret silently returns plaintext when key unset
- **Severity**: MEDIUM
- **File**: ember/devbox/src/secrets.ts:25-35
- **Sources**: vp-eng-comments
- **Issue**: If `APP_ENCRYPTION_KEY` is not configured, the function returns plaintext unchanged. A developer storing a sensitive token won't know encryption was skipped. No JSDoc.
- **Fix**: Add JSDoc explaining the behavior. Log a warning when encryption is skipped.
- **Effort**: trivial

---

## FLARE Findings

### FLARE-001: Internal auth bypassed when FLARE_INTERNAL_KEY is empty
- **Severity**: CRITICAL
- **File**: flare/cmd/control-plane/main.go:87-95, flare/cmd/placement/main.go:77-84
- **Sources**: flare-security-semantic, lead-security
- **Issue**: `internalAuth` middleware checks `cp.internalKey != ""` before enforcing the key. If `FLARE_INTERNAL_KEY` is unset, all internal endpoints are unauthenticated, including host registration and heartbeat. An attacker could register rogue hosts and receive VM traffic.
- **Fix**: Require `FLARE_INTERNAL_KEY` at startup (fatal if empty), or reject requests explicitly when no key is configured.
- **Effort**: small

### FLARE-002: Ingress proxy has no authentication
- **Severity**: CRITICAL
- **File**: flare/cmd/control-plane/main.go:147-156,662-692, flare/cmd/placement/main.go:96-98,114-152
- **Sources**: flare-security-semantic
- **Issue**: The VM ingress proxy on `INGRESS_PORT` has zero authentication. Any network-reachable client can proxy HTTP requests to any running VM by knowing its hostname or machine ID.
- **Fix**: Add authentication (Cloudflare Access headers, mTLS, or shared secret). At minimum, bind ingress to localhost.
- **Effort**: medium

### FLARE-003: Command injection via Exec function
- **Severity**: CRITICAL
- **File**: flare/internal/firecracker/manager.go:458-486
- **Sources**: flare-security-semantic
- **Issue**: The `Exec` method concatenates user-provided `cmd` and `args` into a single string passed to SSH shell. Values like `; rm -rf /` execute arbitrary commands. Although exec returns 501, the function exists and could be re-enabled.
- **Fix**: If exec is re-enabled, use a protocol without shell expansion. Validate `vm.IPAddress`. Mark function deprecated with security warning.
- **Effort**: small

### FLARE-004: Non-atomic desired state update + placement call
- **Severity**: CRITICAL
- **File**: flare/cmd/control-plane/main.go:459-479 (startMachine), 489-509 (stopMachine)
- **Sources**: flare-security-semantic
- **Issue**: `startMachine` sets `desired_state = "running"` then calls placement. If placement fails, desired_state is committed but observed_state unchanged. The reconciler then creates an infinite retry loop.
- **Fix**: Only update desired_state on successful placement response, or add max-retry/circuit-breaker in reconciler.
- **Effort**: small

### FLARE-005: startMachine capacity check is not atomic
- **Severity**: CRITICAL
- **File**: flare/cmd/control-plane/main.go:450-458
- **Sources**: flare-security-semantic
- **Issue**: Capacity check reads host capacity without a transaction or reservation. Two concurrent start requests for VMs on the same host can both pass and overcommit.
- **Fix**: Use `SELECT ... FOR UPDATE` on the host row inside a transaction, then atomically increment used capacity.
- **Effort**: small

### FLARE-006: Placement daemon health endpoint exposes host details without auth
- **Severity**: CRITICAL
- **File**: flare/cmd/placement/main.go:323-331
- **Sources**: flare-security-semantic
- **Issue**: `GET /health` is unauthenticated and returns `host_id`, CPU/memory usage, and total VM count -- useful reconnaissance.
- **Fix**: Wrap with `internalAuth` or strip sensitive fields from the unauthenticated response.
- **Effort**: trivial

### FLARE-007: Reconciler ClaimDriftedMachines is two-phase without transaction
- **Severity**: CRITICAL
- **File**: flare/internal/store/store.go:463-497
- **Sources**: flare-security-semantic, vp-eng-idempotency
- **Issue**: UPDATE to claim machines and SELECT to read them back are not in a transaction. If Phase 1 takes >10 seconds, Phase 2 returns empty results. Claimed machines are blocked for 2 minutes with zero processing.
- **Fix**: Use a single CTE: `WITH claimed AS (UPDATE ... RETURNING id) SELECT ... FROM machines m JOIN claimed c ON c.id = m.id`.
- **Effort**: small

### FLARE-008: API key comparison uses `!=` (non-constant-time)
- **Severity**: CRITICAL
- **File**: flare/cmd/control-plane/main.go:79
- **Sources**: lead-security
- **Issue**: `token != apiKey` is vulnerable to timing attacks. An attacker can progressively guess the API key one byte at a time. Same issue applies to internal key comparison at line 88-89.
- **Fix**: Use `crypto/subtle.ConstantTimeCompare([]byte(token), []byte(apiKey)) != 1`.
- **Effort**: trivial

### FLARE-009: persistVM silently discards errors -- orphans Firecracker processes
- **Severity**: CRITICAL (from sneaky-finder) / HIGH (from flare-semantic)
- **File**: flare/internal/firecracker/manager.go:384-392
- **Sources**: flare-security-semantic, sneaky-finder
- **Issue**: `persistVM()` silently returns on JSON marshal failure and discards `os.WriteFile` errors. If `vm.json` fails to write, RecoverFromDisk will not find this VM after daemon restart. The Firecracker process keeps running but flare loses track of it -- orphaned VM consuming resources indefinitely.
- **Fix**: Return error from `persistVM()`, propagate to callers. Log at minimum.
- **Effort**: small

### FLARE-010: Multiple store operations ignore errors in hot paths
- **Severity**: HIGH
- **File**: flare/cmd/control-plane/main.go:459,477-478,489,507-508,519,533-535
- **Sources**: flare-security-semantic, lead-compliance
- **Issue**: `UpdateMachineDesired`, `UpdateMachineObserved`, `RecordEvent`, `DeleteHostnamesByMachine`, `DeleteMachine` return errors that are silently discarded. Database state diverges from reality.
- **Fix**: Check all error returns. At minimum log failures. For critical state transitions, return error to caller.
- **Effort**: small

### FLARE-011: Reconciler UpdateMachineObserved/SetTransitionDeadline errors dropped
- **Severity**: HIGH
- **File**: flare/internal/reconcile/reconciler.go:159,166,172,185,191,210,215
- **Sources**: lead-compliance
- **Issue**: `UpdateMachineObserved` and `SetTransitionDeadline` return values silently dropped in reconciliation loop. Failed state update means machine released with stale `observed_state`. Failed deadline write means machine stuck in transition permanently.
- **Fix**: Capture and log; on failure call `RequeueWithBackoff` instead of proceeding.
- **Effort**: small

### FLARE-012: Networking manager state is purely in-memory -- IPs leak on restart
- **Severity**: HIGH
- **File**: flare/internal/networking/tap.go:23-38
- **Sources**: flare-security-semantic, scaling-consultant
- **Issue**: TAP device allocations, IP assignments, and sequence counters exist in memory only. Freed IPs from destroyed VMs are lost on restart, permanently leaked from the pool.
- **Fix**: Persist networking state to disk or rebuild `freeIPs` by diffing allocated IPs against the full 2-254 range on recovery.
- **Effort**: small

### FLARE-013: TAP networking hard-capped at 253 VMs per host
- **Severity**: HIGH
- **File**: flare/internal/networking/tap.go:78-79
- **Sources**: scaling-consultant, vp-eng-scaling, vp-eng-completeness
- **Issue**: IP pool limited to single /24 subnet (253 VMs max). At capacity, host is dead to new placements even with CPU/memory available. Bridge config hardcoded with no env var override.
- **Fix**: Document the 253-VM limit. Support multi-subnet or /16 addressing for production. Read bridge config from env vars.
- **Effort**: large

### FLARE-014: Error messages leak internal state
- **Severity**: HIGH
- **File**: flare/cmd/control-plane/main.go:246,375,599
- **Sources**: flare-security-semantic
- **Issue**: Several error responses include raw database error messages or placement response bodies that can reveal table names, column names, constraint names, and internal host IPs.
- **Fix**: Return generic error messages to clients. Log detailed errors server-side.
- **Effort**: small

### FLARE-015: No app name validation on createApp
- **Severity**: HIGH
- **File**: flare/cmd/control-plane/main.go:241-249
- **Sources**: flare-security-semantic, vp-eng-completeness
- **Issue**: `createApp` accepts any `app_name` (empty, extremely long, special characters). `OrgID` is hardcoded to `"org_ember"` for all apps, defeating multi-tenancy.
- **Fix**: Validate `app_name`: non-empty, max length, alphanumeric+dash. Make `OrgID` derived from authenticated identity.
- **Effort**: small

### FLARE-016: Heartbeat swallows errors and returns 200
- **Severity**: HIGH
- **File**: flare/cmd/control-plane/main.go:649-658
- **Sources**: flare-security-semantic
- **Issue**: Always returns HTTP 200 even when store update fails. Ghost hosts appear registered while control plane doesn't track them. VMs placed on these hosts will fail.
- **Fix**: Return HTTP 500 or 404 on heartbeat failure.
- **Effort**: trivial

### FLARE-017: No validation on host registration fields
- **Severity**: HIGH
- **File**: flare/cmd/control-plane/main.go:615-647
- **Sources**: flare-security-semantic
- **Issue**: `registerHost` doesn't validate `host_id`, `capacity_cpus`, `capacity_memory_mb`, `zone`, or `address` format. `ingressAddr` derived by string replacement that fails silently.
- **Fix**: Validate all fields. Parse address properly instead of string replacement.
- **Effort**: small

### FLARE-018: VM struct accessed without lock during Destroy
- **Severity**: HIGH
- **File**: flare/internal/firecracker/manager.go:196-216,395-402
- **Sources**: flare-security-semantic
- **Issue**: Between `Stop` returning and `delete(m.vms, vm.ID)`, another goroutine could `Get(vmID)` and attempt to `Start` after its directory has been deleted.
- **Fix**: Hold `m.mu` (write lock) across entire Destroy, or mark VM as "destroying".
- **Effort**: small

### FLARE-019: Exec endpoint disabled (501) but SDK exposes methods
- **Severity**: HIGH
- **File**: flare/cmd/control-plane/main.go:544-547, flare/sdk/typescript/src/index.ts:134-139
- **Sources**: flare-security-semantic, vp-eng-completeness
- **Issue**: `POST /v1/apps/{app}/machines/{id}/exec` returns 501. SDK also exposes `volumes.create/list/destroy` that always fail. Users get confusing errors.
- **Fix**: Implement exec via placement daemon, or explicitly drop from SDK with deprecation warnings.
- **Effort**: medium

### FLARE-020: Ingress proxy path traversal via remainder
- **Severity**: HIGH
- **File**: flare/cmd/placement/main.go:114-152
- **Sources**: flare-security-semantic
- **Issue**: The `remainder` variable is passed directly to the reverse proxy without sanitization. Encoded path traversal sequences could access unintended paths on the guest VM.
- **Fix**: Sanitize `remainder` with `path.Clean()` and reject paths containing `..`.
- **Effort**: trivial

### FLARE-021: No automated migration runner
- **Severity**: CRITICAL
- **File**: flare/ (project-level)
- **Sources**: build-deploy
- **Issue**: SQL migration files exist but no runner. Control plane doesn't run migrations on startup. Fresh database deployment crashes immediately.
- **Fix**: Add auto-migration step to main.go or separate `migrate` subcommand.
- **Effort**: small

### FLARE-022: Ingress proxy creates new ReverseProxy per request
- **Severity**: MEDIUM
- **File**: flare/cmd/control-plane/main.go:662-691
- **Sources**: scaling-consultant, vp-eng-scaling
- **Issue**: Each request creates a new `httputil.ReverseProxy` with its own `http.Transport`. TCP connections are never reused. At high traffic, connection exhaustion and TLS overhead dominate.
- **Fix**: Cache `ReverseProxy` instances per target host in a `sync.Map` or LRU.
- **Effort**: small

### FLARE-023: DB pool has only 5 idle connections vs 25 max open
- **Severity**: MEDIUM
- **File**: flare/cmd/control-plane/main.go:60-64
- **Sources**: scaling-consultant, vp-eng-scaling
- **Issue**: `SetMaxIdleConns(5)` with `SetMaxOpenConns(25)` means 20 connections are torn down and recreated repeatedly.
- **Fix**: Set `SetMaxIdleConns(25)` to match max open.
- **Effort**: trivial

### FLARE-024: RecoverFromDisk returns 0 on ReadDir failure with no logging
- **Severity**: HIGH
- **File**: flare/internal/firecracker/manager.go:492-494
- **Sources**: sneaky-finder
- **Issue**: If VM directory has permission issues, all VMs appear lost after restart with no error message.
- **Fix**: Log the error and consider returning an error.
- **Effort**: trivial

### FLARE-025: Placement daemon register() fatals but heartbeat failure is silent
- **Severity**: MEDIUM
- **File**: flare/cmd/placement/main.go:70-71,175-227
- **Sources**: flare-security-semantic, build-deploy
- **Issue**: Registration failure on startup fatals, but continuous heartbeat failure after startup has no recovery. VMs placed during the window between failure and dead-host detection are lost.
- **Fix**: Add consecutive-failure counter to heartbeatLoop. After N failures, attempt re-registration. Use `&http.Client{Timeout: 10 * time.Second}`.
- **Effort**: small

### FLARE-026: Hostname resolution queries DB on every proxied HTTP request
- **Severity**: MEDIUM
- **File**: flare/cmd/control-plane/main.go:670
- **Sources**: scaling-consultant
- **Issue**: `ResolveHostname()` executes up to 2 SQL queries per request. At 100 req/s of terminal traffic, 100-200 queries/second just for routing.
- **Fix**: Add an in-memory LRU cache with short TTL (5-10 seconds).
- **Effort**: small

### FLARE-027: Reconciler cleanup (DeleteHostnames + DeleteMachine) not atomic
- **Severity**: MEDIUM
- **File**: flare/internal/reconcile/reconciler.go:219-239
- **Sources**: vp-eng-idempotency
- **Issue**: A crash between `DeleteHostnamesByMachine` and `DeleteMachine` leaves hostname orphaned. Safe in practice (retry is idempotent) but a code smell.
- **Fix**: Wrap both operations in a DB transaction.
- **Effort**: small

### FLARE-028: ListDeletingApps and ListDeadHostMachines are unbounded scans
- **Severity**: HIGH
- **File**: flare/internal/store/store.go:53,576-595
- **Sources**: vp-eng-scaling
- **Issue**: Called on every reconciler tick with no LIMIT. At scale with many deleting apps or dead-host machines, full table scans every 5 seconds.
- **Fix**: Add `LIMIT $1` with a batch size parameter.
- **Effort**: trivial

### FLARE-029: No CI pipeline
- **Severity**: MEDIUM
- **File**: flare/ (project-level)
- **Sources**: build-deploy
- **Issue**: Zero CI configuration. No automated testing on push. Broken commits can be deployed via Packer without gates.
- **Fix**: Add `.github/workflows/ci.yml` with `go build`, `go vet`, and integration test with Postgres service container.
- **Effort**: small

### FLARE-030: All integration tests are permanently t.Skip'd
- **Severity**: MEDIUM
- **File**: flare/cmd/control-plane/integration_test.go:112,188,239,265
- **Sources**: lead-qa, sneaky-finder, vp-eng-tests
- **Issue**: Four out of five integration tests skip unconditionally. The four critical invariants documented in the file header have no passing test. Creates false impression of coverage.
- **Fix**: Implement at minimum `TestDestroyFailedCreate` using httptest.NewServer. Move permanently skipped tests to `_manual_test.go`.
- **Effort**: large

### FLARE-031: Manager has only one test (GenerateMAC)
- **Severity**: MEDIUM
- **File**: flare/internal/firecracker/manager_test.go
- **Sources**: lead-qa, vp-eng-tests
- **Issue**: Manager has Create, Start, Stop, Destroy, RecoverFromDisk, List, Get, State methods. Only GenerateMAC is tested. Path-traversal guard, idempotency check, PID-zero check, persistVM round-trip all untested.
- **Fix**: Add unit tests requiring only filesystem: invalid ImageRef, valid Create writes vm.json, RecoverFromDisk repopulates map.
- **Effort**: medium

---

## STOKE Findings

### STOKE-001: Hook bypass via JSON parsing fragility (grep-based)
- **Severity**: CRITICAL
- **File**: stoke/internal/hooks/hooks.go:96-101
- **Sources**: stoke-security-semantic, lead-security
- **Issue**: PreToolUse hook parses JSON using `grep -o '"tool_name":"[^"]*"'`. This regex-based parsing is bypassable via whitespace, unicode escapes, or field order manipulation. This is the primary security enforcement layer preventing the AI agent from running dangerous commands.
- **Fix**: Replace with `jq` or a compiled helper: `TOOL_NAME=$(echo "$INPUT" | jq -r '.tool_name // empty')`.
- **Effort**: medium

### STOKE-002: Scope guard shell injection via file path
- **Severity**: CRITICAL
- **File**: stoke/internal/hooks/hooks.go:288-318
- **Sources**: stoke-security-semantic
- **Issue**: `installScopeWriteGuard` embeds `absAllowed` and `escFile` into a bash script via string concatenation. Escaping only handles single quotes. File paths with backticks or `$(...)` could break out of context.
- **Fix**: Write the allowed path to a separate file and read it in the hook script, or use environment variables.
- **Effort**: small

### STOKE-003: safeEnvMode2 passes full environment to agents including secrets
- **Severity**: CRITICAL
- **File**: stoke/internal/engine/env.go:71-77
- **Sources**: stoke-security-semantic, lead-security
- **Issue**: Mode 2 copies the entire parent process environment to the AI agent subprocess. Any secrets (database credentials, AWS keys, internal tokens) are visible to the agent. Mode 1 correctly strips sensitive vars.
- **Fix**: Apply the same stripping logic as Mode 1 for known-dangerous variables even in Mode 2.
- **Effort**: small

### STOKE-004: Token prefix leaked as account ID dedup key
- **Severity**: CRITICAL
- **File**: stoke/internal/pools/pools.go:163
- **Sources**: stoke-security-semantic, lead-security
- **Issue**: `readAccountID()` uses the first 16 characters of an OAuth access token as a dedup key. This token fragment is stored in `manifest.json` (world-readable 0644). Fragments significantly narrow brute-force space.
- **Fix**: Hash the token with SHA-256 before using as dedup key: `"tok-" + sha256hex(token)[:16]`.
- **Effort**: trivial

### STOKE-005: Manifest file written world-readable (0644)
- **Severity**: CRITICAL
- **File**: stoke/internal/pools/pools.go:68
- **Sources**: stoke-security-semantic, lead-security
- **Issue**: `manifest.Save()` writes `manifest.json` with mode `0644`. Contains pool config dirs pointing to credential directories plus token-derived account IDs.
- **Fix**: Use `0600` permissions for `os.WriteFile`.
- **Effort**: trivial

### STOKE-006: Scheduler state access with inconsistent/fragile locking
- **Severity**: HIGH
- **File**: stoke/internal/scheduler/scheduler.go:62-65
- **Sources**: stoke-security-semantic, vp-eng-idempotency
- **Issue**: Pre-population of `s.completed` happens without holding `stateMu`. `depsOK` reads state maps and is currently only called under `stateMu`, but any future caller outside the lock would be a race. The pattern is correct but fragile.
- **Fix**: Make `depsOK` and `hasConflict` private and document they require `stateMu`. Move pre-population inside a `stateMu.Lock()` block.
- **Effort**: trivial

### STOKE-007: JSON session store not safe for concurrent workers (TOCTOU + deadlock)
- **Severity**: HIGH
- **File**: stoke/internal/session/store.go:50-55,114-131,145-146
- **Sources**: stoke-security-semantic, vp-eng-idempotency, lead-compliance
- **Issue**: `addLearnedPattern` calls `LoadLearning` and `SaveLearning` while `s.mu` is already held by `SaveAttempt`. Since `sync.Mutex` is non-reentrant in Go, this deadlocks. Also, `readJSON` error in `SaveAttempt` is silently discarded, risking history overwrite on corruption.
- **Fix**: Separate learning persistence mutex, or inline learning logic within the existing lock. Check `readJSON` error: treat corruption as error rather than silently overwriting.
- **Effort**: small

### STOKE-008: Goroutine leak in stream parser on context cancellation
- **Severity**: HIGH
- **File**: stoke/internal/stream/parser.go:93-95
- **Sources**: stoke-security-semantic
- **Issue**: Inner goroutine reads from subprocess stdout and sends to `lines` channel. If parent exits due to timeout, the channel is never drained. Goroutine leaks until subprocess is killed.
- **Fix**: Use a select with a done channel in the inner goroutine, or close the reader on parser exit.
- **Effort**: small

### STOKE-009: Codex runner 60s hardcoded timeout too short
- **Severity**: HIGH
- **File**: stoke/internal/engine/codex.go:172
- **Sources**: stoke-security-semantic
- **Issue**: Hardcoded `time.After(60 * time.Second)` will kill legitimate long-running Codex tasks. Claude runner uses configurable timeout.
- **Fix**: Make timeout configurable or derive from phase spec.
- **Effort**: small

### STOKE-010: killProcessGroup ignores errors and uses blocking sleep
- **Severity**: HIGH
- **File**: stoke/internal/engine/claude.go:185-197
- **Sources**: stoke-security-semantic
- **Issue**: Neither `syscall.Kill` call checks error. SIGKILL to a dead PGID could kill a recycled PID group. 3-second sleep is blocking and not cancellable.
- **Fix**: Check if process is alive after SIGTERM before sending SIGKILL. Use `cmd.Process.Wait()` with timeout.
- **Effort**: small

### STOKE-011: copyCredentials silently swallows all errors
- **Severity**: HIGH
- **File**: stoke/internal/pools/pools.go:396-413
- **Sources**: stoke-security-semantic
- **Issue**: Returns no error. If credential files fail to copy, caller proceeds and removes the old pool dir, leaving stale/missing credentials.
- **Fix**: Return error from `copyCredentials` and check before removing source directory.
- **Effort**: small

### STOKE-012: No input validation on Ember worker ID in URL paths
- **Severity**: HIGH
- **File**: stoke/internal/compute/ember.go:154,168-169,191-214,233-234,251-260
- **Sources**: stoke-security-semantic
- **Issue**: `w.id` from Ember API response is concatenated directly into URL paths. A crafted ID with `../` could redirect requests to unintended endpoints.
- **Fix**: Validate `w.id` matches `^[a-zA-Z0-9-]+$`. Use `url.PathEscape(w.id)`.
- **Effort**: trivial

### STOKE-013: No TLS certificate verification on Ember/managed API calls
- **Severity**: HIGH
- **File**: stoke/internal/compute/ember.go:31, stoke/internal/managed/proxy.go:59, stoke/internal/remote/session.go:52
- **Sources**: stoke-security-semantic
- **Issue**: All three files create default `http.Client`. Ember endpoint URL comes from env var. If set to HTTP (non-TLS), all API keys and tokens sent in cleartext.
- **Fix**: Validate that `endpoint` starts with `https://` before use, or log warning for non-HTTPS in production.
- **Effort**: trivial

### STOKE-014: Codex stderrDone channel not drained on timeout (goroutine leak)
- **Severity**: HIGH
- **File**: stoke/internal/engine/codex.go:176-180
- **Sources**: stoke-security-semantic
- **Issue**: On timeout, `select` with `default` means stderr goroutine is leaked. Comment says "drain to prevent leak" but `default` just moves on.
- **Fix**: After `killProcessGroup(cmd)`, wait for `stderrDone` with a short timeout instead of using `default`.
- **Effort**: small

### STOKE-015: Review verdict tree-SHA truncation can panic
- **Severity**: MEDIUM
- **File**: stoke/internal/workflow/workflow.go:528
- **Sources**: stoke-security-semantic
- **Issue**: `preReviewTree[:12]` and `postReviewTree[:12]` will panic if SHA is shorter than 12 characters.
- **Fix**: Use safe truncation: `[:min(12, len(s))]`.
- **Effort**: trivial

### STOKE-016: CGO dependency (go-sqlite3) blocks builds without gcc
- **Severity**: CRITICAL
- **File**: stoke/go.mod:8
- **Sources**: build-deploy
- **Issue**: `github.com/mattn/go-sqlite3` requires a C compiler. `go build` fails without gcc. install.sh does not check for this.
- **Fix**: Add `apt-get install gcc` to install.sh, or replace with pure-Go `modernc.org/sqlite`.
- **Effort**: small

### STOKE-017: AuthModeMode1/Mode2 are opaque names with no documentation
- **Severity**: MEDIUM
- **File**: stoke/internal/engine/types.go:10-13
- **Sources**: vp-eng-comments
- **Issue**: Constants used in 20+ call sites with no comments. Wrong mode silently passes credentials or breaks auth.
- **Fix**: Add doc comments explaining Mode 1 (subscription auth, strips keys) vs Mode 2 (API key auth, inherits full env).
- **Effort**: trivial

### STOKE-018: workflow.Engine struct has zero doc comments
- **Severity**: MEDIUM
- **File**: stoke/internal/workflow/workflow.go:32-51
- **Sources**: vp-eng-comments
- **Issue**: Central orchestrator struct with fields like `DryRun`, `PlanOnly`, `AllowedFiles`, `State`, `Verifier`, `Runners` has no documentation. Constructed in 3+ places.
- **Fix**: Add struct-level doc comment plus field-level comments for key fields.
- **Effort**: small

### STOKE-019: EmberBackend.Name() returns "flare" but talks to Ember API
- **Severity**: MEDIUM
- **File**: stoke/internal/compute/ember.go:35
- **Sources**: vp-eng-completeness, cross-repo-alignment
- **Issue**: Name appears in logs, errors, and TUI. Mismatches what user configured (`EMBER_API_KEY`).
- **Fix**: Return `"ember"` or `"ember/flare"`.
- **Effort**: trivial

### STOKE-020: flareWorker.Stdout() returns empty reader -- no live output
- **Severity**: HIGH
- **File**: stoke/internal/compute/ember.go:131, stoke/internal/compute/local.go:40
- **Sources**: vp-eng-completeness
- **Issue**: Both `Stdout()` implementations return empty readers. TUI shows zero live output from workers. Users see hung display during remote and local execution.
- **Fix**: Pipe stdout from exec subprocess through `io.Pipe` and return the reader end.
- **Effort**: medium

### STOKE-021: Retry loop re-uses same worktree name -- stale state on crash restart
- **Severity**: MEDIUM
- **File**: stoke/internal/workflow/workflow.go:179-196
- **Sources**: vp-eng-idempotency
- **Issue**: After crash restart, `Worktrees.Prepare` may find stale worktree with same name from previous run.
- **Fix**: Include run-unique nonce (timestamp/UUID) in worktree name.
- **Effort**: small

### STOKE-022: Pool manifest load/save has no file locking
- **Severity**: MEDIUM
- **File**: stoke/internal/pools/pools.go:44-69
- **Sources**: vp-eng-scaling
- **Issue**: No advisory lock on read-modify-write. Two concurrent `stoke pool add` commands corrupt the manifest.
- **Fix**: Use `os.OpenFile` with exclusive lock (`syscall.LOCK_EX`).
- **Effort**: small

### STOKE-023: MapSecuritySurface error silently discarded
- **Severity**: MEDIUM
- **File**: stoke/cmd/stoke/main.go:1469
- **Sources**: lead-compliance
- **Issue**: `secMap, _ = scanpkg.MapSecuritySurface(...)`. If it fails, secMap is nil and downstream code may panic. User believes security surface was mapped when it wasn't.
- **Fix**: Log the error: `if err != nil { fmt.Fprintf(os.Stderr, "warning: security surface mapping failed: %v\n", err) }`.
- **Effort**: trivial

### STOKE-024: advanceState errors systematically discarded in failure paths
- **Severity**: MEDIUM
- **File**: stoke/internal/workflow/workflow.go (multiple lines)
- **Sources**: lead-compliance
- **Issue**: `_ = e.advanceState(taskstate.Failed, ...)` discarded in every failure path. Invalid-transition errors silently lost, meaning state machine audit trail is incomplete.
- **Fix**: Log the advance error even when discarding: `if advErr := e.advanceState(...); advErr != nil { log.Printf(...) }`.
- **Effort**: small

### STOKE-025: SQLite opened without MaxOpenConns(1) -- busy timeout contention
- **Severity**: MEDIUM
- **File**: stoke/internal/session/sqlstore.go:30
- **Sources**: scaling-consultant
- **Issue**: Default `sql.DB` pool opens multiple connections to SQLite, but SQLite only supports one writer. Concurrent saves serialize on 5-second busy timeouts.
- **Fix**: Add `db.SetMaxOpenConns(1)` after opening.
- **Effort**: trivial

---

## CROSS-REPO Findings

### CROSS-001: stoke->ember worker API is broken (wrong size format, wrong field names)
- **Severity**: CRITICAL
- **File**: stoke/internal/compute/ember.go:59, ember/devbox/src/routes/workers.ts:116-131
- **Sources**: cross-repo-alignment
- **Issue**: stoke sends `size: "4x"` but ember expects `"performance-4x"` (rejected with 400). stoke reads `.state` but ember returns `.status`. stoke checks for `"running"` and `"error"` but ember uses `"active"` and `"failed"`. Remote burst execution cannot work end-to-end.
- **Fix**: stoke must prefix sizes with `"performance-"`, read `status` instead of `state`, map `"active"` to running and `"failed"` to error.
- **Effort**: small

### CROSS-002: stoke calls exec/upload/download endpoints that don't exist on ember
- **Severity**: CRITICAL
- **File**: stoke/internal/compute/ember.go:169, ember/devbox/src/routes/workers.ts
- **Sources**: cross-repo-alignment
- **Issue**: `POST /v1/workers/:id/exec`, `POST .../upload`, `GET .../download` are called by stoke but do not exist in ember. Only `DELETE` and `POST .../stop` exist. Remote task execution (core value proposition) cannot work.
- **Fix**: ember needs these endpoints, or stoke needs to exec on the machine hostname directly.
- **Effort**: large

### CROSS-003: Machine state vocabularies diverge across all three repos
- **Severity**: HIGH
- **File**: ember/devbox/src/db.ts, flare/internal/store/store.go, stoke/internal/compute/ember.go
- **Sources**: cross-repo-alignment
- **Issue**: ember calls running `"started"`, flare calls them `"running"`. ember calls destroyed `"deleted"`, flare `"destroyed"`. Worker allocations use yet another set (`pending|active|completed|failed|expired|cancelled`). stoke checks for none of the correct ember values.
- **Fix**: Standardize state names. Create a shared mapping document.
- **Effort**: medium

### CROSS-004: stoke doesn't handle worker TTL -- workers expire silently mid-task
- **Severity**: HIGH
- **File**: stoke/internal/compute/ember.go, ember/devbox/src/routes/workers.ts
- **Sources**: cross-repo-alignment
- **Issue**: ember workers default to 30min TTL. stoke doesn't set `ttl_minutes` and doesn't track expiration. If a worker expires mid-task, stoke gets errors with no understanding of why.
- **Fix**: stoke should set appropriate TTL based on expected task duration and handle expiration gracefully.
- **Effort**: small

### CROSS-005: Root README is unfilled template
- **Severity**: HIGH
- **File**: /home/eric/repos/trio/README.md
- **Sources**: product-owner
- **Issue**: The root README is entirely placeholder text. Any user or investor landing on the repo sees a template.
- **Fix**: Fill with actual product description, architecture overview, and links to sub-repos.
- **Effort**: small

### CROSS-006: Trio integration story is entirely undocumented
- **Severity**: HIGH
- **File**: docs/ARCHITECTURE.md, docs/HOW-IT-WORKS.md, docs/FEATURE-MAP.md
- **Sources**: product-owner
- **Issue**: All trio-level docs are empty templates. No documentation explains how stoke+ember+flare work together, what env vars to set, or the deployment model.
- **Fix**: Write actual content for all three documents.
- **Effort**: large

### CROSS-007: stoke->ember cross-service env vars undocumented
- **Severity**: MEDIUM
- **File**: stoke/internal/managed/proxy.go, stoke/internal/remote/session.go
- **Sources**: build-deploy, product-owner
- **Issue**: stoke requires `EMBER_API_KEY` and `EMBER_API_URL`. Ember requires `ENABLE_V1_WORKERS=true` and `ENABLE_MANAGED_AI=true`. Neither repo documents this cross-service dependency. Users get 501 errors with no understanding of the cause.
- **Fix**: Document in both READMEs. In stoke, detect 501 and return user-friendly error.
- **Effort**: small

### CROSS-008: ember does not use flare SDK -- no integration exists
- **Severity**: MEDIUM
- **File**: ember/devbox/src/fly.ts, flare/sdk/typescript/src/index.ts
- **Sources**: cross-repo-alignment
- **Issue**: ember talks directly to Fly.io Machines API. The flare TypeScript SDK exists but has zero consumers. ember and flare are independent paths to VM provisioning with no connection.
- **Fix**: Plan ember->flare migration. Ember needs a compute abstraction layer that can target either Fly.io or flare.
- **Effort**: large

### CROSS-009: stoke install script references non-existent GitHub org
- **Severity**: MEDIUM
- **File**: stoke/README.md:160-162
- **Sources**: product-owner
- **Issue**: Install script clones `https://github.com/good-ventures/stoke.git` but the module is `github.com/ericmacdougall/stoke`. The URL 404s.
- **Fix**: Update install.sh to correct repo URL.
- **Effort**: trivial

### CROSS-010: stoke README command count and package count are stale
- **Severity**: MEDIUM
- **File**: stoke/README.md:123,146
- **Sources**: product-owner
- **Issue**: README says 9 commands but 16 are implemented. Claims 19 packages but 26 exist. Source line counts also stale.
- **Fix**: Update README with accurate counts and add missing packages/commands.
- **Effort**: trivial
