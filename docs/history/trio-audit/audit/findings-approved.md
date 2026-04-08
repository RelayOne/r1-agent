# Approved Findings for Repair
**[APPROVED] findings -> tasks, [DEFERRED] noted for later, [DROPPED] filtered out (low ROI)**

| Repo | Critical | High | Medium | Total |
|------|----------|------|--------|-------|
| EMBER | 12 | 18 | 14 | 44 |
| FLARE | 10 | 11 | 3 | 24 |
| STOKE | 6 | 9 | 4 | 19 |
| CROSS-REPO | 2 | 2 | 2 | 6 |
| **Total** | **30** | **40** | **23** | **93** |

---

## EMBER

### EMBER-001: Reconcile endpoint open when RECONCILE_SECRET is unset
- **Severity**: CRITICAL
- **Tier**: 1
- **File**: ember/devbox/src/routes/billing.ts:721-726
- **Issue**: The `/api/billing/reconcile` endpoint checks `if (secret && auth !== ...)`. When `RECONCILE_SECRET` is unset (falsy), the condition short-circuits and the endpoint is fully open to unauthenticated callers. Any anonymous request can trigger a full reconciliation pass including Stripe API calls.
- **Fix**: Invert the guard: `if (!secret) return c.json({ error: "Reconcile not configured" }, 503);` unconditionally before checking the bearer token.
- **Effort**: trivial

### EMBER-007: Webhook idempotency check is non-atomic TOCTOU race
- **Severity**: CRITICAL
- **Tier**: 1
- **File**: ember/devbox/src/routes/billing.ts:403-404
- **Issue**: The outer guard `SELECT id FROM billing_events WHERE stripe_event_id = ...` runs OUTSIDE any transaction. With Stripe's at-least-once delivery, two concurrent handlers can both pass this check. Only the `credit_purchase` branch is protected; all other branches (invoice.paid, customer.subscription.*) are NOT.
- **Fix**: Wrap the entire webhook body in a single transaction beginning with `INSERT INTO processed_stripe_events ... ON CONFLICT DO NOTHING RETURNING event_id`; bail out if INSERT returns nothing.
- **Effort**: small

### EMBER-008: invoice.paid billing_events INSERT has no ON CONFLICT guard
- **Severity**: CRITICAL
- **Tier**: 1
- **File**: ember/devbox/src/routes/billing.ts:472-473
- **Issue**: Unlike the `checkout.session.completed` branch which uses `ON CONFLICT (stripe_event_id) DO NOTHING`, the `invoice.paid` branch inserts directly with no conflict guard. Duplicate events produce duplicate `charge` rows, corrupting billing history.
- **Fix**: Add `ON CONFLICT (stripe_event_id) DO NOTHING` to the billing_events INSERT.
- **Effort**: trivial

### EMBER-009: Stripe subscriptionItems.create called before DB slot, compensation is non-idempotent
- **Severity**: CRITICAL
- **Tier**: 1
- **File**: ember/devbox/src/routes/billing.ts:167-208
- **Issue**: If the DB transaction fails after Stripe successfully creates the subscription item, the compensation path deletes the item. On client retry, Stripe returns the cached (now-deleted) item's response from the idempotency key, and the code writes a slot pointing to a deleted Stripe item.
- **Fix**: After compensation delete, also change the retry path to re-create the intent with a new ID, breaking Stripe's idempotency key cache tie.
- **Effort**: medium

### EMBER-013: In-memory rate limiter is default -- ineffective across multiple instances
- **Severity**: CRITICAL
- **Tier**: 1
- **File**: ember/devbox/src/rate-limit.ts:26-38
- **Issue**: `memStores` is a process-local `Map`. With N API instances, rate limits are per-process. A user can multiply their rate limit by N instances. Auth brute-force protection becomes 20*N attempts.
- **Fix**: Set `RATE_LIMIT_BACKEND=postgres` as default in production, or add to `validateProductionConfig()`.
- **Effort**: trivial

### EMBER-014: Postgres connection pool uses library defaults (10 connections, no timeouts)
- **Severity**: CRITICAL
- **Tier**: 1
- **File**: ember/devbox/src/connection.ts:7
- **Issue**: `postgres(process.env.DATABASE_URL!)` uses default pool settings (max 10, no timeouts). At 100+ concurrent requests, connection starvation cascades into 500s across all routes.
- **Fix**: Configure explicit pool: `postgres(process.env.DATABASE_URL!, { max: 25, idle_timeout: 20, connect_timeout: 5 })`. Add guard for missing DATABASE_URL.
- **Effort**: trivial

### EMBER-015: Stripe API version `as any` hides SDK mismatch
- **Severity**: CRITICAL
- **Tier**: 1
- **File**: ember/devbox/src/routes/account.ts:15, credits.ts:11
- **Issue**: `{ apiVersion: "2024-04-10" as any }` silences a compiler error. Using a stale API version string with `as any` means the SDK may silently send requests with a version the current binary doesn't understand, causing unexpected response shapes for payment-critical operations.
- **Fix**: Either upgrade `stripe` package so the version is recognized, or use `Stripe.latestApiVersion`. Remove the `as any`.
- **Effort**: trivial

### EMBER-016: `(stripeSub as any).current_period_start` risks Invalid Date in DB
- **Severity**: CRITICAL
- **Tier**: 1
- **File**: ember/devbox/src/routes/billing.ts:269-270
- **Issue**: The `as any` bypass means if Stripe SDK renames/removes the field, the access silently produces `undefined`, leading to `new Date(NaN * 1000)` stored as `"Invalid Date"` in period columns.
- **Fix**: Remove `as any`. Use `stripeSub.current_period_start` directly (typed on `Stripe.Subscription` in modern SDK).
- **Effort**: trivial

### EMBER-033: AI usage metering silently loses cost record on SSE parse failure
- **Severity**: CRITICAL
- **Tier**: 1
- **File**: ember/devbox/src/routes/ai.ts:116
- **Issue**: In the SSE stream `flush()` handler, JSON parse failure in `catch {}` silently discards the final usage data. The INSERT into `ai_usage` never fires, so the user gets free API calls.
- **Fix**: Add logging and attempt partial match fallback for `total_cost`.
- **Effort**: small

### EMBER-040: Dashboard start/stop/delete have no error feedback
- **Severity**: CRITICAL
- **Tier**: 1
- **File**: ember/devbox/web/src/pages/Dashboard.tsx:117-119
- **Issue**: `doStart`, `doStop`, and `doDel` fire async calls with no try/catch and no user-visible error state. Failures are completely silent. Delete failure leaves machine in error state with no explanation.
- **Fix**: Wrap in try/catch, surface toast or inline error messages.
- **Effort**: small

### EMBER-041: handleBuySlot billing state leak freezes button permanently
- **Severity**: CRITICAL (UX blocking workflow)
- **Tier**: 1
- **File**: ember/devbox/web/src/pages/NewMachineModal.tsx:67-92
- **Issue**: If the Stripe redirect path is reached, `setBuyingSlot(false)` is never called. On any page re-visit, the button is permanently frozen.
- **Fix**: Call `setBuyingSlot(false)` before `safeRedirect`.
- **Effort**: small

### EMBER-042: 401 redirect loop for /verify-email
- **Severity**: CRITICAL (UX blocking workflow)
- **Tier**: 1
- **File**: ember/devbox/web/src/lib/api.ts:22-29
- **Issue**: The `api()` helper redirects to `/login` on any 401. VerifyEmail.tsx is unprotected but calls the API. If token is expired and backend returns 401, user is navigated away mid-verification.
- **Fix**: Add `/verify-email` to the passthrough list in the 401 guard.
- **Effort**: trivial

### EMBER-002: CSRF bypass when Origin header absent but session cookie exists
- **Severity**: HIGH
- **Tier**: 1
- **File**: ember/devbox/src/middleware.ts:77-83
- **Issue**: CSRF protection allows requests without an `Origin` header if a valid session cookie is present. Form submissions from `<form method="POST">` do NOT include an Origin header in some browsers. An attacker can craft a cross-origin form that posts to the API and the browser will attach the session cookie, bypassing CSRF protection entirely.
- **Fix**: Require Origin header on ALL state-changing requests or also require a custom header when Origin is absent.
- **Effort**: small

### EMBER-003: HTTP header injection via upload filename
- **Severity**: HIGH
- **Tier**: 1
- **File**: ember/devbox/src/fly.ts:378-380
- **Issue**: `uploadFileToMachine` passes `filename` as HTTP header `X-Filename` without sanitization. A crafted filename containing `\r\n` could inject additional HTTP headers.
- **Fix**: Sanitize the filename: `const safeFilename = filename.replace(/[\r\n\x00-\x1f]/g, "");`
- **Effort**: trivial

### EMBER-004: SQL interval interpolation fragility in AI usage endpoint
- **Severity**: HIGH
- **Tier**: 1
- **File**: ember/devbox/src/routes/ai.ts:156-162
- **Issue**: The `interval` variable is interpolated into SQL with `::interval` cast. While currently validated against three string values, the pattern is fragile -- one new period value with a typo becomes SQL injection.
- **Fix**: Use a whitelist map returning only known constants, throw 400 on unknown period values.
- **Effort**: trivial

### EMBER-005: Stripe customer creation race (billing checkout)
- **Severity**: HIGH
- **Tier**: 1
- **File**: ember/devbox/src/routes/billing.ts:128-139
- **Issue**: Two concurrent checkout requests both see null stripe_customer_id, both create Stripe customers. The loser's Stripe customer becomes an orphan.
- **Fix**: Use `SELECT ... FOR UPDATE` or a DB advisory lock before Stripe customer creation within a transaction.
- **Effort**: small

### EMBER-010: syncSubscription SELECT+UPDATE is a TOCTOU race
- **Severity**: HIGH
- **Tier**: 1
- **File**: ember/devbox/src/routes/billing.ts:276-299
- **Issue**: Two concurrent webhook events for the same subscription both pass the SELECT with `existing = undefined`, then both try to INSERT, causing a unique-constraint violation.
- **Fix**: Replace SELECT + conditional INSERT/UPDATE with a single `INSERT ... ON CONFLICT (stripe_subscription_id) DO UPDATE SET ...` UPSERT.
- **Effort**: small

### EMBER-011: Machine create has no idempotency key -- retries create duplicates
- **Severity**: HIGH
- **Tier**: 2
- **File**: ember/devbox/src/routes/machines.ts:55-203
- **Issue**: No client-supplied idempotency key for `POST /api/machines`. A retry after timeout creates a second DB row and Fly app, double-consuming slots.
- **Fix**: Accept a client-provided `requestId` or use `(user_id, name)` as a natural idempotency key.
- **Effort**: medium

### EMBER-012: Machine region not validated against allowlist
- **Severity**: HIGH
- **Tier**: 1
- **File**: ember/devbox/src/routes/machines.ts:25
- **Issue**: The `createSchema` accepts any string for `region`. Arbitrary strings flow into Fly API calls.
- **Fix**: Use `z.enum(["sjc", "lax", "iad", ...])` with an explicit allowlist.
- **Effort**: trivial

### EMBER-017: Detailed health check runs 7 sequential COUNT queries with no caching
- **Severity**: HIGH
- **Tier**: 2
- **File**: ember/devbox/src/index.ts:165-226
- **Issue**: Each health check hits the DB 7 times sequentially. Under load, consumes connection pool capacity needed for user requests.
- **Fix**: Cache the result for 30-60 seconds or run queries in parallel with `Promise.all()`.
- **Effort**: trivial

### EMBER-018: Missing database indexes on sessions, slots, machines, purchaseIntents, exchangeCodes
- **Severity**: HIGH
- **Tier**: 1
- **File**: ember/devbox/src/db.ts (multiple locations)
- **Issue**: Common queries require full table scans. No indexes on `sessions.userId`, `slots.userId`, `slots.subscriptionId`, `machines.userId`, `machines.state`, etc.
- **Fix**: Add indexes via migration.
- **Effort**: trivial

### EMBER-020: Encryption key derived via SHA-256 (no KDF)
- **Severity**: HIGH
- **Tier**: 1
- **File**: ember/devbox/src/secrets.ts:20
- **Issue**: If `APP_ENCRYPTION_KEY` is not exactly 32 bytes, SHA-256 is used with no work factor, salt, or iteration count. A weak passphrase protecting GitHub OAuth tokens would be trivially brutable.
- **Fix**: Use a proper KDF like HKDF, or require the key to be exactly 32 bytes and refuse to start if invalid.
- **Effort**: small

### EMBER-021: Reconcile secret comparison uses `===` (non-constant-time)
- **Severity**: HIGH
- **Tier**: 1
- **File**: ember/devbox/src/index.ts:172, ember/devbox/src/routes/billing.ts:723
- **Issue**: Bearer token comparison uses `===` which is vulnerable to timing attacks.
- **Fix**: Use `crypto.timingSafeEqual(Buffer.from(auth || ''), Buffer.from(\`Bearer ${secret}\`))` with length check first.
- **Effort**: trivial

### EMBER-034: Machine stop/delete non-idempotent -- partial failure leaves inconsistent state
- **Severity**: HIGH
- **Tier**: 1
- **File**: ember/devbox/src/routes/machines.ts:248-290
- **Issue**: After `fly.stopMachine()` succeeds but before DB update, a crash leaves DB as `started`. DELETE failure path doesn't release `slot_id`, leaving the slot permanently consumed.
- **Fix**: For stop, guard with `WHERE state = 'started'`. For delete, null out `slot_id` on failure path.
- **Effort**: small

### EMBER-037: Stripe price IDs use non-null assertions without dev-mode fallback
- **Severity**: HIGH
- **Tier**: 2
- **File**: ember/devbox/src/routes/billing.ts:17-19
- **Issue**: `STRIPE_PRICE_4X!`, `STRIPE_PRICE_8X!`, `STRIPE_PRICE_16X!` and `STRIPE_SECRET_KEY!` are undefined in non-prod. Cryptic Stripe API errors result.
- **Fix**: Validate at module load or add dev-mode early error.
- **Effort**: trivial

### EMBER-038: CANONICAL_IMAGE_DIGEST! undefined in non-prod
- **Severity**: HIGH
- **Tier**: 2
- **File**: ember/devbox/src/routes/machines.ts:65
- **Issue**: `process.env.CANONICAL_IMAGE_DIGEST!` will be `undefined` if unset, passed to Fly API as Docker image reference.
- **Fix**: Validate at module load or before use with explicit 500 error.
- **Effort**: trivial

### EMBER-039: Dockerfile has no release_command for migrations
- **Severity**: HIGH
- **Tier**: 2
- **File**: ember/devbox/fly.toml
- **Issue**: The Dockerfile does not run migrations. fly.toml has no `[deploy.release_command]`. Schema changes require manual migration.
- **Fix**: Add `[deploy] release_command = "npx tsx src/migrate.ts"` to fly.toml.
- **Effort**: trivial

### EMBER-043: Credits purchase error silently swallowed
- **Severity**: HIGH
- **Tier**: 2
- **File**: ember/devbox/web/src/pages/Credits.tsx:35-51
- **Issue**: `handlePurchase` catches errors but shows no message. The button just re-enables with no indication of failure.
- **Fix**: Add error state and display a message on catch.
- **Effort**: trivial

### EMBER-044: Settings error displayed with success styling (green)
- **Severity**: HIGH
- **Tier**: 2
- **File**: ember/devbox/web/src/pages/Settings.tsx:33-43
- **Issue**: Errors are set to "Save failed" but displayed with green success styling.
- **Fix**: Add a separate `saveError` state with danger styling.
- **Effort**: trivial

### EMBER-051: GET /api/machines returns all non-deleted machines with no pagination
- **Severity**: HIGH
- **Tier**: 2
- **File**: ember/devbox/src/routes/machines.ts:40-52
- **Issue**: As users accumulate machines, this query grows unbounded. Drizzle buffers the entire result set in memory.
- **Fix**: Add `.limit(200)` with cursor-based pagination.
- **Effort**: small

### EMBER-006: Stripe customer creation race (credits checkout)
- **Severity**: MEDIUM
- **Tier**: 1
- **File**: ember/devbox/src/routes/credits.ts:37-43
- **Issue**: Same pattern as EMBER-005. Credits checkout also does read-then-write without a lock.
- **Fix**: Mirror the billing.ts pattern with idempotency key.
- **Effort**: trivial

### EMBER-025: GET /v1/sessions/:id is public with no auth
- **Severity**: MEDIUM
- **Tier**: 1
- **File**: ember/devbox/src/routes/sessions.ts:69-86
- **Issue**: Any caller who can guess a nanoid(12) session ID can read Stoke session data including tasks, cost, and worker count.
- **Fix**: Add auth or add a `public` boolean column so only user-chosen sessions are shareable. At minimum, redact `total_cost_usd`.
- **Effort**: small

### EMBER-026: Stoke session tasks array accepts z.any() elements
- **Severity**: MEDIUM
- **Tier**: 1
- **File**: ember/devbox/src/routes/sessions.ts:47
- **Issue**: `tasks: z.array(z.any()).optional()` accepts any JSON structure with no size bound. An attacker could send enormous nested objects stored as JSONB.
- **Fix**: Add `.max(1000)` on the array and constrain element size.
- **Effort**: small

### EMBER-027: Missing password max-length on register and reset
- **Severity**: MEDIUM
- **Tier**: 1
- **File**: ember/devbox/src/routes/auth.ts:27, ember/devbox/src/routes/account.ts:66
- **Issue**: Password fields use `z.string().min(8)` with no max. Argon2 hashing of a multi-megabyte password is CPU-intensive DoS.
- **Fix**: Add `.max(128)` to all password Zod schemas.
- **Effort**: trivial

### EMBER-028: API key label not validated for length or content
- **Severity**: MEDIUM
- **Tier**: 1
- **File**: ember/devbox/src/routes/api-keys.ts:27
- **Issue**: No length limit on API key label. A user could submit a multi-megabyte label.
- **Fix**: Add a Zod schema: `z.object({ label: z.string().max(100).default("default") })`.
- **Effort**: trivial

### EMBER-029: GitHub API calls have no timeout
- **Severity**: MEDIUM
- **Tier**: 2
- **File**: ember/devbox/src/routes/auth.ts:135-145, ember/devbox/src/routes/github.ts:43-167
- **Issue**: All `fetch("https://api.github.com/...")` calls lack timeouts. A hanging GitHub API blocks requests indefinitely.
- **Fix**: Add `signal: AbortSignal.timeout(10000)` to all external fetch calls.
- **Effort**: trivial

### EMBER-030: Fly API calls have no timeout
- **Severity**: MEDIUM
- **Tier**: 2
- **File**: ember/devbox/src/fly.ts:54,80
- **Issue**: `flyApi` and `flyGraphQL` do not set timeouts on fetch calls.
- **Fix**: Add `signal: AbortSignal.timeout(15000)` to the fetch calls.
- **Effort**: trivial

### EMBER-031: OpenRouter error status forwarded directly to client
- **Severity**: MEDIUM
- **Tier**: 2
- **File**: ember/devbox/src/routes/ai.ts:68
- **Issue**: Upstream HTTP status forwarded directly. If OpenRouter returns 401, the client thinks their own API key is invalid.
- **Fix**: Map upstream errors to 502.
- **Effort**: trivial

### EMBER-032: Webhook returns 500 on processing error (Stripe retries indefinitely)
- **Severity**: MEDIUM
- **Tier**: 2
- **File**: ember/devbox/src/routes/billing.ts:537
- **Issue**: Persistent errors cause Stripe to retry for 72 hours.
- **Fix**: Return 200 for known-permanent errors and 500 only for transient errors.
- **Effort**: small

### EMBER-045: Forgot-password endpoint has no rate limiting
- **Severity**: MEDIUM
- **Tier**: 1
- **File**: ember/devbox/src/routes/account.ts:30
- **Issue**: `/api/account/forgot-password` is not rate limited. Attackers can enumerate users or cause email flooding.
- **Fix**: Add `app.use("/api/account/forgot-password", authLimiter)`.
- **Effort**: trivial

### EMBER-048: Stripe webhook allows requests when STRIPE_WEBHOOK_SECRET is unset
- **Severity**: MEDIUM
- **Tier**: 1
- **File**: ember/devbox/src/routes/billing.ts:390-398
- **Issue**: If env var is missing, `constructEvent` gets `undefined`. Could skip signature verification.
- **Fix**: Guard: `if (!process.env.STRIPE_WEBHOOK_SECRET) return c.json({ error: "Webhook secret not configured" }, 500)`.
- **Effort**: trivial

### EMBER-049: syncSubscription slot cancellation loop is not atomic
- **Severity**: MEDIUM
- **Tier**: 2
- **File**: ember/devbox/src/routes/billing.ts:335-352
- **Issue**: Cancels slots one-by-one outside a transaction. Crash mid-loop leaves inconsistent state.
- **Fix**: Replace with a single `UPDATE slots SET status='cancelled' WHERE subscription_id=$1 AND stripe_subscription_item_id NOT IN (...)`.
- **Effort**: small

### EMBER-053: Postgres rate limit table created lazily with DDL on first request
- **Severity**: MEDIUM
- **Tier**: 2
- **File**: ember/devbox/src/rate-limit.ts:60-71
- **Issue**: `CREATE TABLE IF NOT EXISTS` fires on first request. Multiple instances starting simultaneously contend on DDL lock.
- **Fix**: Move table creation to migrations.
- **Effort**: trivial

### EMBER-055: Org name passed directly into GitHub API URL
- **Severity**: MEDIUM
- **Tier**: 1
- **File**: ember/devbox/src/routes/github.ts:165
- **Issue**: The `org` route parameter is interpolated into a fetch URL without validation.
- **Fix**: Validate `org` matches `^[a-zA-Z0-9_-]+$` or use `encodeURIComponent(org)`.
- **Effort**: trivial

---

## FLARE

### FLARE-001: Internal auth bypassed when FLARE_INTERNAL_KEY is empty
- **Severity**: CRITICAL
- **Tier**: 1
- **File**: flare/cmd/control-plane/main.go:87-95, flare/cmd/placement/main.go:77-84
- **Issue**: If `FLARE_INTERNAL_KEY` is unset, all internal endpoints are unauthenticated, including host registration and heartbeat. An attacker could register rogue hosts and receive VM traffic.
- **Fix**: Require `FLARE_INTERNAL_KEY` at startup (fatal if empty).
- **Effort**: small

### FLARE-002: Ingress proxy has no authentication
- **Severity**: CRITICAL
- **Tier**: 1
- **File**: flare/cmd/control-plane/main.go:147-156,662-692, flare/cmd/placement/main.go:96-98,114-152
- **Issue**: The VM ingress proxy on `INGRESS_PORT` has zero authentication. Any network-reachable client can proxy HTTP requests to any running VM.
- **Fix**: Add authentication (Cloudflare Access headers, mTLS, or shared secret). At minimum, bind ingress to localhost.
- **Effort**: medium

### FLARE-003: Command injection via Exec function
- **Severity**: CRITICAL
- **Tier**: 1
- **File**: flare/internal/firecracker/manager.go:458-486
- **Issue**: The `Exec` method concatenates user-provided `cmd` and `args` into a single string passed to SSH shell. Values like `; rm -rf /` execute arbitrary commands.
- **Fix**: Mark function deprecated with security warning. If re-enabled, use a protocol without shell expansion.
- **Effort**: small

### FLARE-004: Non-atomic desired state update + placement call
- **Severity**: CRITICAL
- **Tier**: 1
- **File**: flare/cmd/control-plane/main.go:459-479
- **Issue**: `startMachine` sets `desired_state = "running"` then calls placement. If placement fails, desired_state is committed but observed_state unchanged. The reconciler then creates an infinite retry loop.
- **Fix**: Only update desired_state on successful placement response, or add max-retry/circuit-breaker in reconciler.
- **Effort**: small

### FLARE-005: startMachine capacity check is not atomic
- **Severity**: CRITICAL
- **Tier**: 1
- **File**: flare/cmd/control-plane/main.go:450-458
- **Issue**: Capacity check reads host capacity without a transaction or reservation. Two concurrent start requests can overcommit.
- **Fix**: Use `SELECT ... FOR UPDATE` on the host row inside a transaction.
- **Effort**: small

### FLARE-006: Placement daemon health endpoint exposes host details without auth
- **Severity**: CRITICAL
- **Tier**: 1
- **File**: flare/cmd/placement/main.go:323-331
- **Issue**: `GET /health` is unauthenticated and returns `host_id`, CPU/memory usage, and total VM count.
- **Fix**: Wrap with `internalAuth` or strip sensitive fields from the unauthenticated response.
- **Effort**: trivial

### FLARE-007: Reconciler ClaimDriftedMachines is two-phase without transaction
- **Severity**: CRITICAL
- **Tier**: 1
- **File**: flare/internal/store/store.go:463-497
- **Issue**: UPDATE to claim machines and SELECT to read them back are not in a transaction. Claimed machines can be blocked for 2 minutes with zero processing.
- **Fix**: Use a single CTE: `WITH claimed AS (UPDATE ... RETURNING id) SELECT ... FROM machines m JOIN claimed c ON c.id = m.id`.
- **Effort**: small

### FLARE-008: API key comparison uses `!=` (non-constant-time)
- **Severity**: CRITICAL
- **Tier**: 1
- **File**: flare/cmd/control-plane/main.go:79
- **Issue**: `token != apiKey` is vulnerable to timing attacks.
- **Fix**: Use `crypto/subtle.ConstantTimeCompare([]byte(token), []byte(apiKey)) != 1`.
- **Effort**: trivial

### FLARE-009: persistVM silently discards errors -- orphans Firecracker processes
- **Severity**: CRITICAL
- **Tier**: 1
- **File**: flare/internal/firecracker/manager.go:384-392
- **Issue**: `persistVM()` silently returns on errors. If `vm.json` fails to write, RecoverFromDisk will not find this VM after daemon restart. Firecracker process keeps running but flare loses track of it.
- **Fix**: Return error from `persistVM()`, propagate to callers.
- **Effort**: small

### FLARE-021: No automated migration runner
- **Severity**: CRITICAL
- **Tier**: 1
- **File**: flare/ (project-level)
- **Issue**: SQL migration files exist but no runner. Control plane doesn't run migrations on startup. Fresh database deployment crashes immediately.
- **Fix**: Add auto-migration step to main.go or separate `migrate` subcommand.
- **Effort**: small

### FLARE-010: Multiple store operations ignore errors in hot paths
- **Severity**: HIGH
- **Tier**: 1
- **File**: flare/cmd/control-plane/main.go:459,477-478,489,507-508,519,533-535
- **Issue**: `UpdateMachineDesired`, `UpdateMachineObserved`, `RecordEvent`, `DeleteHostnamesByMachine`, `DeleteMachine` return errors that are silently discarded.
- **Fix**: Check all error returns. At minimum log failures.
- **Effort**: small

### FLARE-011: Reconciler UpdateMachineObserved/SetTransitionDeadline errors dropped
- **Severity**: HIGH
- **Tier**: 1
- **File**: flare/internal/reconcile/reconciler.go:159,166,172,185,191,210,215
- **Issue**: Failed state update means machine released with stale `observed_state`. Failed deadline write means machine stuck in transition permanently.
- **Fix**: Capture and log; on failure call `RequeueWithBackoff` instead of proceeding.
- **Effort**: small

### FLARE-012: Networking manager state is purely in-memory -- IPs leak on restart
- **Severity**: HIGH
- **Tier**: 1
- **File**: flare/internal/networking/tap.go:23-38
- **Issue**: Freed IPs from destroyed VMs are lost on restart, permanently leaked from the pool.
- **Fix**: Persist networking state to disk or rebuild `freeIPs` by diffing allocated IPs against the full range on recovery.
- **Effort**: small

### FLARE-014: Error messages leak internal state
- **Severity**: HIGH
- **Tier**: 1
- **File**: flare/cmd/control-plane/main.go:246,375,599
- **Issue**: Several error responses include raw database error messages revealing table names, column names, constraint names, and internal host IPs.
- **Fix**: Return generic error messages to clients. Log detailed errors server-side.
- **Effort**: small

### FLARE-015: No app name validation on createApp
- **Severity**: HIGH
- **Tier**: 1
- **File**: flare/cmd/control-plane/main.go:241-249
- **Issue**: `createApp` accepts any `app_name` (empty, extremely long, special characters). `OrgID` is hardcoded to `"org_ember"` for all apps.
- **Fix**: Validate `app_name`: non-empty, max length, alphanumeric+dash. Make `OrgID` derived from authenticated identity.
- **Effort**: small

### FLARE-016: Heartbeat swallows errors and returns 200
- **Severity**: HIGH
- **Tier**: 1
- **File**: flare/cmd/control-plane/main.go:649-658
- **Issue**: Always returns HTTP 200 even when store update fails. Ghost hosts appear registered while control plane doesn't track them.
- **Fix**: Return HTTP 500 or 404 on heartbeat failure.
- **Effort**: trivial

### FLARE-017: No validation on host registration fields
- **Severity**: HIGH
- **Tier**: 1
- **File**: flare/cmd/control-plane/main.go:615-647
- **Issue**: `registerHost` doesn't validate any fields. `ingressAddr` derived by string replacement that fails silently.
- **Fix**: Validate all fields. Parse address properly.
- **Effort**: small

### FLARE-018: VM struct accessed without lock during Destroy
- **Severity**: HIGH
- **Tier**: 1
- **File**: flare/internal/firecracker/manager.go:196-216,395-402
- **Issue**: Between `Stop` returning and `delete(m.vms, vm.ID)`, another goroutine could `Get(vmID)` and attempt to `Start` after its directory has been deleted.
- **Fix**: Hold `m.mu` (write lock) across entire Destroy, or mark VM as "destroying".
- **Effort**: small

### FLARE-020: Ingress proxy path traversal via remainder
- **Severity**: HIGH
- **Tier**: 1
- **File**: flare/cmd/placement/main.go:114-152
- **Issue**: The `remainder` variable is passed directly to the reverse proxy without sanitization. Encoded path traversal sequences could access unintended paths on the guest VM.
- **Fix**: Sanitize `remainder` with `path.Clean()` and reject paths containing `..`.
- **Effort**: trivial

### FLARE-024: RecoverFromDisk returns 0 on ReadDir failure with no logging
- **Severity**: HIGH
- **Tier**: 2
- **File**: flare/internal/firecracker/manager.go:492-494
- **Issue**: If VM directory has permission issues, all VMs appear lost after restart with no error message.
- **Fix**: Log the error and consider returning an error.
- **Effort**: trivial

### FLARE-028: ListDeletingApps and ListDeadHostMachines are unbounded scans
- **Severity**: HIGH
- **Tier**: 2
- **File**: flare/internal/store/store.go:53,576-595
- **Issue**: Called on every reconciler tick with no LIMIT. At scale, full table scans every 5 seconds.
- **Fix**: Add `LIMIT $1` with a batch size parameter.
- **Effort**: trivial

### FLARE-022: Ingress proxy creates new ReverseProxy per request
- **Severity**: MEDIUM
- **Tier**: 2
- **File**: flare/cmd/control-plane/main.go:662-691
- **Issue**: Each request creates a new `httputil.ReverseProxy` with its own `http.Transport`. TCP connections are never reused.
- **Fix**: Cache `ReverseProxy` instances per target host.
- **Effort**: small

### FLARE-023: DB pool has only 5 idle connections vs 25 max open
- **Severity**: MEDIUM
- **Tier**: 2
- **File**: flare/cmd/control-plane/main.go:60-64
- **Issue**: `SetMaxIdleConns(5)` with `SetMaxOpenConns(25)` means 20 connections are torn down and recreated repeatedly.
- **Fix**: Set `SetMaxIdleConns(25)` to match max open.
- **Effort**: trivial

### FLARE-025: Placement daemon heartbeat failure has no recovery
- **Severity**: MEDIUM
- **Tier**: 2
- **File**: flare/cmd/placement/main.go:70-71,175-227
- **Issue**: Continuous heartbeat failure after startup has no recovery. VMs placed during the window between failure and dead-host detection are lost.
- **Fix**: Add consecutive-failure counter to heartbeatLoop. After N failures, attempt re-registration.
- **Effort**: small

---

## STOKE

### STOKE-001: Hook bypass via JSON parsing fragility (grep-based)
- **Severity**: CRITICAL
- **Tier**: 1
- **File**: stoke/internal/hooks/hooks.go:96-101
- **Issue**: PreToolUse hook parses JSON using `grep -o '"tool_name":"[^"]*"'`. This regex-based parsing is bypassable via whitespace, unicode escapes, or field order manipulation. This is the primary security enforcement layer preventing the AI agent from running dangerous commands.
- **Fix**: Replace with `jq` or a compiled helper.
- **Effort**: medium

### STOKE-002: Scope guard shell injection via file path
- **Severity**: CRITICAL
- **Tier**: 1
- **File**: stoke/internal/hooks/hooks.go:288-318
- **Issue**: `installScopeWriteGuard` embeds paths into a bash script via string concatenation. File paths with backticks or `$(...)` could break out of context.
- **Fix**: Write the allowed path to a separate file and read it in the hook script, or use environment variables.
- **Effort**: small

### STOKE-003: safeEnvMode2 passes full environment to agents including secrets
- **Severity**: CRITICAL
- **Tier**: 1
- **File**: stoke/internal/engine/env.go:71-77
- **Issue**: Mode 2 copies the entire parent process environment to the AI agent subprocess. Any secrets (database credentials, AWS keys, internal tokens) are visible to the agent.
- **Fix**: Apply the same stripping logic as Mode 1 for known-dangerous variables even in Mode 2.
- **Effort**: small

### STOKE-004: Token prefix leaked as account ID dedup key
- **Severity**: CRITICAL
- **Tier**: 1
- **File**: stoke/internal/pools/pools.go:163
- **Issue**: `readAccountID()` uses the first 16 characters of an OAuth access token as a dedup key. This token fragment is stored in `manifest.json` (world-readable 0644).
- **Fix**: Hash the token with SHA-256 before using as dedup key.
- **Effort**: trivial

### STOKE-005: Manifest file written world-readable (0644)
- **Severity**: CRITICAL
- **Tier**: 1
- **File**: stoke/internal/pools/pools.go:68
- **Issue**: `manifest.Save()` writes `manifest.json` with mode `0644`. Contains pool config dirs pointing to credential directories plus token-derived account IDs.
- **Fix**: Use `0600` permissions for `os.WriteFile`.
- **Effort**: trivial

### STOKE-006: Scheduler state access with inconsistent/fragile locking
- **Severity**: HIGH
- **Tier**: 2
- **File**: stoke/internal/scheduler/scheduler.go:62-65
- **Issue**: Pre-population of `s.completed` happens without holding `stateMu`. Pattern is correct but fragile.
- **Fix**: Make `depsOK` and `hasConflict` private and document they require `stateMu`. Move pre-population inside lock.
- **Effort**: trivial

### STOKE-007: JSON session store not safe for concurrent workers (TOCTOU + deadlock)
- **Severity**: HIGH
- **Tier**: 1
- **File**: stoke/internal/session/store.go:50-55,114-131,145-146
- **Issue**: `addLearnedPattern` calls `LoadLearning` and `SaveLearning` while `s.mu` is already held. Since `sync.Mutex` is non-reentrant in Go, this deadlocks.
- **Fix**: Separate learning persistence mutex, or inline learning logic within the existing lock.
- **Effort**: small

### STOKE-008: Goroutine leak in stream parser on context cancellation
- **Severity**: HIGH
- **Tier**: 1
- **File**: stoke/internal/stream/parser.go:93-95
- **Issue**: Inner goroutine reads from subprocess stdout and sends to `lines` channel. If parent exits due to timeout, the channel is never drained.
- **Fix**: Use a select with a done channel in the inner goroutine.
- **Effort**: small

### STOKE-009: Codex runner 60s hardcoded timeout too short
- **Severity**: HIGH
- **Tier**: 2
- **File**: stoke/internal/engine/codex.go:172
- **Issue**: Hardcoded `time.After(60 * time.Second)` will kill legitimate long-running Codex tasks.
- **Fix**: Make timeout configurable or derive from phase spec.
- **Effort**: small

### STOKE-010: killProcessGroup ignores errors and uses blocking sleep
- **Severity**: HIGH
- **Tier**: 2
- **File**: stoke/internal/engine/claude.go:185-197
- **Issue**: Neither `syscall.Kill` call checks error. SIGKILL to a dead PGID could kill a recycled PID group.
- **Fix**: Check if process is alive after SIGTERM before sending SIGKILL.
- **Effort**: small

### STOKE-011: copyCredentials silently swallows all errors
- **Severity**: HIGH
- **Tier**: 2
- **File**: stoke/internal/pools/pools.go:396-413
- **Issue**: Returns no error. If credential files fail to copy, caller proceeds and removes the old pool dir, leaving stale/missing credentials.
- **Fix**: Return error from `copyCredentials` and check before removing source directory.
- **Effort**: small

### STOKE-012: No input validation on Ember worker ID in URL paths
- **Severity**: HIGH
- **Tier**: 1
- **File**: stoke/internal/compute/ember.go:154,168-169,191-214,233-234,251-260
- **Issue**: `w.id` from Ember API response is concatenated directly into URL paths. A crafted ID with `../` could redirect requests to unintended endpoints.
- **Fix**: Validate `w.id` matches `^[a-zA-Z0-9-]+$`. Use `url.PathEscape(w.id)`.
- **Effort**: trivial

### STOKE-013: No TLS certificate verification on Ember/managed API calls
- **Severity**: HIGH
- **Tier**: 2
- **File**: stoke/internal/compute/ember.go:31, stoke/internal/managed/proxy.go:59, stoke/internal/remote/session.go:52
- **Issue**: If endpoint URL is set to HTTP (non-TLS), all API keys and tokens sent in cleartext.
- **Fix**: Validate that `endpoint` starts with `https://` before use.
- **Effort**: trivial

### STOKE-015: Review verdict tree-SHA truncation can panic
- **Severity**: MEDIUM
- **Tier**: 2
- **File**: stoke/internal/workflow/workflow.go:528
- **Issue**: `preReviewTree[:12]` will panic if SHA is shorter than 12 characters.
- **Fix**: Use safe truncation: `[:min(12, len(s))]`.
- **Effort**: trivial

### STOKE-016: CGO dependency (go-sqlite3) blocks builds without gcc
- **Severity**: CRITICAL
- **Tier**: 1
- **File**: stoke/go.mod:8
- **Issue**: `github.com/mattn/go-sqlite3` requires a C compiler. `go build` fails without gcc.
- **Fix**: Add `apt-get install gcc` to install.sh, or replace with pure-Go `modernc.org/sqlite`.
- **Effort**: small

### STOKE-025: SQLite opened without MaxOpenConns(1) -- busy timeout contention
- **Severity**: MEDIUM
- **Tier**: 2
- **File**: stoke/internal/session/sqlstore.go:30
- **Issue**: Default `sql.DB` pool opens multiple connections to SQLite, but SQLite only supports one writer.
- **Fix**: Add `db.SetMaxOpenConns(1)` after opening.
- **Effort**: trivial

### STOKE-014: Codex stderrDone channel not drained on timeout (goroutine leak)
- **Severity**: HIGH
- **Tier**: 2
- **File**: stoke/internal/engine/codex.go:176-180
- **Issue**: On timeout, `select` with `default` means stderr goroutine is leaked.
- **Fix**: After `killProcessGroup(cmd)`, wait for `stderrDone` with a short timeout.
- **Effort**: small

### STOKE-022: Pool manifest load/save has no file locking
- **Severity**: MEDIUM
- **Tier**: 2
- **File**: stoke/internal/pools/pools.go:44-69
- **Issue**: No advisory lock on read-modify-write. Two concurrent `stoke pool add` commands corrupt the manifest.
- **Fix**: Use `os.OpenFile` with exclusive lock.
- **Effort**: small

### STOKE-023: MapSecuritySurface error silently discarded
- **Severity**: MEDIUM
- **Tier**: 2
- **File**: stoke/cmd/stoke/main.go:1469
- **Issue**: `secMap, _ = scanpkg.MapSecuritySurface(...)`. If it fails, secMap is nil and downstream code may panic.
- **Fix**: Log the error.
- **Effort**: trivial

---

## CROSS-REPO

### CROSS-001: stoke->ember worker API is broken (wrong size format, wrong field names)
- **Severity**: CRITICAL
- **Tier**: 1
- **File**: stoke/internal/compute/ember.go:59, ember/devbox/src/routes/workers.ts:116-131
- **Issue**: stoke sends `size: "4x"` but ember expects `"performance-4x"` (rejected with 400). stoke reads `.state` but ember returns `.status`. stoke checks for `"running"` and `"error"` but ember uses `"active"` and `"failed"`. Remote burst execution cannot work end-to-end.
- **Fix**: stoke must prefix sizes with `"performance-"`, read `status` instead of `state`, map `"active"` to running and `"failed"` to error.
- **Effort**: small

### CROSS-002: stoke calls exec/upload/download endpoints that don't exist on ember
- **Severity**: CRITICAL
- **Tier**: 1
- **File**: stoke/internal/compute/ember.go:169, ember/devbox/src/routes/workers.ts
- **Issue**: `POST /v1/workers/:id/exec`, `POST .../upload`, `GET .../download` are called by stoke but do not exist in ember. Remote task execution (core value proposition) cannot work.
- **Fix**: ember needs these endpoints, or stoke needs to exec on the machine hostname directly.
- **Effort**: large

### CROSS-003: Machine state vocabularies diverge across all three repos
- **Severity**: HIGH
- **Tier**: 2
- **File**: ember/devbox/src/db.ts, flare/internal/store/store.go, stoke/internal/compute/ember.go
- **Issue**: ember calls running `"started"`, flare calls them `"running"`. Destroyed is `"deleted"` vs `"destroyed"`. Worker allocations use yet another set. stoke checks for none of the correct ember values.
- **Fix**: Standardize state names. Create a shared mapping document.
- **Effort**: medium

### CROSS-004: stoke doesn't handle worker TTL -- workers expire silently mid-task
- **Severity**: HIGH
- **Tier**: 1
- **File**: stoke/internal/compute/ember.go, ember/devbox/src/routes/workers.ts
- **Issue**: ember workers default to 30min TTL. stoke doesn't set `ttl_minutes` and doesn't track expiration.
- **Fix**: stoke should set appropriate TTL based on expected task duration and handle expiration gracefully.
- **Effort**: small

### CROSS-007: stoke->ember cross-service env vars undocumented
- **Severity**: MEDIUM
- **Tier**: 2
- **File**: stoke/internal/managed/proxy.go, stoke/internal/remote/session.go
- **Issue**: stoke requires `EMBER_API_KEY` and `EMBER_API_URL`. Ember requires `ENABLE_V1_WORKERS=true` and `ENABLE_MANAGED_AI=true`. Neither repo documents this. Users get 501 errors with no understanding of the cause.
- **Fix**: Document in both READMEs. In stoke, detect 501 and return user-friendly error.
- **Effort**: small

### CROSS-009: stoke install script references non-existent GitHub org
- **Severity**: MEDIUM
- **Tier**: 2
- **File**: stoke/README.md:160-162
- **Issue**: Install script clones `https://github.com/good-ventures/stoke.git` but the module is `github.com/ericmacdougall/stoke`. The URL 404s.
- **Fix**: Update install.sh to correct repo URL.
- **Effort**: trivial
