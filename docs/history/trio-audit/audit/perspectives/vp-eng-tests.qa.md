# VP Engineering — Test Coverage Audit
**Date:** 2026-04-01
**Scope:** ember (TypeScript/Hono), flare (Go), stoke (Go)
**Method:** Read all test files, read all source files, map coverage manually.

---

## Summary

| Repo | Test files | Critical gaps |
|------|-----------|--------------|
| ember | 3 (DB-layer only) | 15 untested HTTP route handlers |
| flare | 3 (1 unit, 1 trivial, 1 skipped integration) | 8 untested store/control-plane functions |
| stoke | 31 | 5 medium gaps in workflow/subscriptions/hooks |

---

## EMBER — Untested Routes and Business Logic

The ember test suite tests **database schema constraints only** (via raw SQL inserts). No HTTP route handler is tested. All route logic — auth middleware, Stripe webhook processing, machine lifecycle, OAuth flows — runs completely untested.

### Auth Routes (`routes/auth.ts`)

- [ ] [CRITICAL] [routes/auth.ts:30] `POST /register` untested — fix: test email duplicate rejection (409), password hash stored, session cookie set on success — effort: small

- [ ] [CRITICAL] [routes/auth.ts:58] `POST /login` untested — fix: test banned user returns 403, wrong password returns 401, session cookie set on success — effort: small

- [ ] [CRITICAL] [routes/auth.ts:79] `POST /logout` untested — fix: test that terminal sessions are revoked on logout (calls `revokeAllTerminalSessionsForUser`), exchange codes are burned, audit log written — effort: small

- [ ] [CRITICAL] [routes/auth.ts:124] `GET /github/callback` untested — fix: test state mismatch returns redirect to error, unverified email returns redirect, new user created from GitHub identity, existing account linked — effort: medium

- [ ] [HIGH] [routes/auth.ts:236] `GET /google/callback` untested — fix: test unverified email gate, new user creation, existing account lookup by provider ID — effort: medium

- [ ] [HIGH] [routes/auth.ts:101] `GET /me` untested — fix: test banned user gets session invalidated and returns `{user: null}`, valid session returns user — effort: trivial

### Machine Routes (`routes/machines.ts`)

- [ ] [CRITICAL] [routes/machines.ts:55] `POST / (create machine)` untested — fix: test slot reservation (402 when no slot), Fly provisioning failure cleans up DB row and releases slot, `slot_id=NULL` on error path — effort: medium

- [ ] [CRITICAL] [routes/machines.ts:206] `POST /:id/start` untested — fix: test entitlement check (slot must be active), slot rebinding on start, 402 when no active slot available — effort: small

- [ ] [CRITICAL] [routes/machines.ts:297] `POST /:id/terminal` untested — fix: test hostname-not-ready gate (503), slot entitlement check (403 when cancelled), exchange code created with session ID, terminal session record inserted — effort: small

- [ ] [CRITICAL] [routes/machines.ts:428] `POST /verify-code` untested — fix: test machine token auth, atomic code consumption, revoked session rejects, banned user rejects — effort: small

- [ ] [HIGH] [routes/machines.ts:351] `POST /:id/check-hostname` untested — fix: test slot-cancelled-during-provisioning path (machine transitions to stopped/needs_stop), promotion from creating to started — effort: small

- [ ] [HIGH] [routes/machines.ts:486] `revokeAllTerminalSessionsForUser` untested (as unit) — fix: test that exchange codes are burned when user logs out/is banned, all active sessions revoked — effort: trivial

### Billing Routes (`routes/billing.ts`)

- [ ] [CRITICAL] [routes/billing.ts:390] `POST /webhooks` untested — fix: test `checkout.session.completed` (both subscription and credit_purchase paths), `invoice.payment_failed` triggers `enforceSlotCancellations`, duplicate stripe_event_id rejected — effort: large

- [ ] [CRITICAL] [routes/billing.ts:263] `syncSubscription()` untested — fix: test REVOKE_STATUSES cancel local slots, item removed from subscription cancels corresponding slot, plan upgrade on same item ID updates size/price — effort: medium

- [ ] [CRITICAL] [routes/billing.ts:358] `enforceSlotCancellations()` untested — fix: test running machine bound to cancelled slot is stopped, creating machine with cancelled slot is stopped, needs_stop state set on stop failure — effort: small

- [ ] [CRITICAL] [routes/billing.ts:547] `reconcile()` function untested — fix: test needs_stop machine cleared when slot reactivated, orphan Stripe item detection logged, local slot without Stripe backing cancelled — effort: large

- [ ] [HIGH] [routes/billing.ts:89] `POST /checkout` untested — fix: test delinquent subscription redirects to portal, existing active subscription adds item, no subscription creates Checkout session, idempotency via purchase intent — effort: medium

### Account Routes (`routes/account.ts`)

- [ ] [CRITICAL] [routes/account.ts:69] `POST /reset-password` untested — fix: test all outstanding reset tokens invalidated on use, all sessions revoked, all terminal sessions revoked — effort: small

- [ ] [HIGH] [routes/account.ts:254] `POST /delete` untested — fix: test machines destroyed before user anonymized, Stripe subscriptions cancelled, slots marked cancelled, OAuth tokens deleted, user record anonymized (not hard deleted) — effort: medium

### Admin Routes (`routes/admin.ts`)

- [ ] [HIGH] [routes/admin.ts:90] `POST /users/:id/ban` untested — fix: test API keys revoked, terminal sessions revoked, machines destroyed on Fly, Lucia sessions invalidated, audit log written — effort: small

- [ ] [HIGH] [routes/admin.ts:62] `POST /users/:id/credit` untested — fix: test negative balance blocked by CHECK constraint, credit_transactions ledger entry written atomically with balance update — effort: trivial

---

## FLARE — Untested Store Operations and Control Plane Paths

The flare test suite has: one trivial unit test (`TestGenerateMAC`), one pure logic test (`TestComputeAction`), and one integration test that is **entirely skipped** (all `TestVMLifecycle*` cases call `t.Skip`). `TestReconcilerMarksMachinesLost` is the only integration test that actually runs, but it directly executes SQL rather than going through the store interface.

### Store (`internal/store/store.go`)

- [ ] [CRITICAL] [store/store.go:190] `PlaceAndReserve()` untested — fix: test atomically selects host with sufficient CPUs+mem, reserves capacity, returns error when no host fits; concurrent callers only one wins — effort: medium

- [ ] [CRITICAL] [store/store.go:461] `ClaimDriftedMachines()` untested — fix: test FOR UPDATE SKIP LOCKED prevents double-claiming, respects reconcile_after backoff, returns only claimed machines — effort: medium

- [ ] [HIGH] [store/store.go:341] `ResolveHostname()` untested — fix: test generated hostname lookup, custom hostname lookup, dead host excluded from result — effort: small

- [ ] [HIGH] [store/store.go:546] `RequeueWithBackoff()` untested — fix: test exponential backoff calculation (2^attempts, capped at 300s), claim released with error recorded — effort: trivial

### Firecracker Manager (`internal/firecracker/manager.go`)

- [ ] [CRITICAL] [firecracker/manager.go:221] `Manager.Start()` untested — fix: test idempotency (stale PID cleared and process re-launched), PID written to vm.json for recovery — effort: large (requires mock Firecracker or integration environment)

- [ ] [CRITICAL] [firecracker/manager.go:491] `Manager.RecoverFromDisk()` untested — fix: test vm.json files loaded, dead PIDs cleared (PID=0), live PIDs preserved — effort: medium (can use mock PIDs and temp dirs without real Firecracker)

- [ ] [HIGH] [firecracker/manager.go:441] `Manager.ResourceUsage()` untested — fix: test counts only running VMs (PID alive), ignores stopped VMs — effort: trivial

### Control Plane (`cmd/control-plane/main.go`)

- [ ] [CRITICAL] [cmd/control-plane/main.go:268] `createMachine` handler untested — fix: test capacity reservation released on placement failure, machine marked `failed` in DB on Fly error, app-status gate (409 for deleting app) — effort: medium

- [ ] [CRITICAL] [cmd/control-plane/main.go:662] `proxyVMTraffic` untested — fix: test hostname resolution from DB, dead host excluded, 404 for unknown hostname, path rewrite to `/_vm/{id}` — effort: small

- [ ] [HIGH] [cmd/control-plane/main.go:73] `runCycle()` reconciler phases untested — fix: test Phase 3 (deleting app: all machines destroyed then app marked deleted), Phase 1 dead-host cascade, Phase 2 machine convergence — effort: medium

---

## STOKE — Untested Workflow Paths and Engine Functions

Stoke has the best coverage of the three repos. The unit/integration test suite is broad. Gaps are in specific execution paths within the workflow engine and in security-critical symlink guards in hooks.

### Workflow Engine (`internal/workflow/workflow.go`)

- [ ] [CRITICAL] [workflow/workflow.go:399] Cross-model review pool rotation path untested — fix: test that rate-limited review triggers rotation to second pool, second pool used for re-review — effort: medium

- [ ] [CRITICAL] [workflow/workflow.go:489] Post-review revalidation (worktree mutation detection) untested — fix: test that any file change by reviewer causes task failure; file set comparison and tree SHA comparison both exercised — effort: medium

- [ ] [HIGH] [workflow/workflow.go:179] Execute retry: clean-worktree-per-retry invariant untested — fix: test that retry creates fresh worktree (not polluted with previous attempt files), hooks reinstalled — effort: medium

- [ ] [HIGH] [workflow/workflow.go:266] Rate-limit pool rotation during execute untested — fix: test `execResult.Subtype == "rate_limited"` triggers rotation to AcquireExcluding, clean worktree prepared for rotation — effort: medium

### Subscriptions Manager (`internal/subscriptions/manager.go`)

- [ ] [HIGH] [subscriptions/manager.go:162] `AcquireExcluding()` untested — fix: test skips listed pool IDs, returns next available non-excluded pool, returns error when all non-excluded pools are exhausted — effort: trivial

- [ ] [HIGH] [subscriptions/manager.go:207] `WaitForPool()` untested — fix: test blocks until pool released, respects context cancellation/timeout — effort: small

### Hooks (`internal/hooks/hooks.go`)

- [ ] [HIGH] [hooks/hooks.go:14] `safeWrite()` symlink rejection untested — fix: test symlink at target path is rejected, symlink in parent directory is rejected, successful write creates file atomically — effort: trivial (note: similar tests exist in `worktree/helpers_test.go` for `SafeWriteFile`, but `hooks.safeWrite` is a separate unexported function with no tests)

---

## Coverage by Risk Category

| Category | Covered | Gap |
|----------|---------|-----|
| Ember HTTP routes | 0% (DB schema only) | All route handlers |
| Ember webhook processing | 0% | syncSubscription, webhook switch, reconcile() |
| Ember auth flows (OAuth, password) | 0% | GitHub/Google callbacks, reset, logout |
| Flare store operations | ~10% (MarkDeadHosts via raw SQL only) | PlaceAndReserve, ClaimDriftedMachines, ResolveHostname |
| Flare FC manager lifecycle | ~5% (GenerateMAC only) | Start, RecoverFromDisk, Stop |
| Flare control plane handlers | 0% (all integration tests skipped) | createMachine, proxyVMTraffic, runCycle |
| Stoke workflow phases | ~60% (dry-run, retry prompt, routing) | Pool rotation, post-review revalidation |
| Stoke subscriptions | ~80% (Acquire/Release/Circuit) | AcquireExcluding, WaitForPool |
| Stoke hooks security | ~50% (Install, HooksConfig) | safeWrite symlink guard (unexported) |

---

## Highest-Priority Fixes (in order)

1. **Ember webhook handler** — Stripe webhook is the source of truth for billing state. Zero coverage means silent failures in `syncSubscription` and `enforceSlotCancellations` go undetected.
2. **Ember `verify-code` endpoint** — This is the security gate for terminal access. Untested means the "session revoked" and "banned user" checks may silently fail.
3. **Flare `PlaceAndReserve`** — This is the capacity reservation function. Race conditions here leak CPU/memory budget. Can be unit-tested with an in-memory SQLite or test Postgres.
4. **Flare `createMachine` handler** — Reservation-leak on Fly failure is untested. A misconfigured Fly key will permanently consume all available slots.
5. **Stoke post-review revalidation** — The reviewer must not mutate the worktree. This invariant is implemented but not tested. A reviewer bug could silently corrupt the merge artifact.
