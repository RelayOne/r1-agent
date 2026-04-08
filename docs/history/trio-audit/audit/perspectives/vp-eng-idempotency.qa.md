# VP Engineering — Idempotency Audit
**Date:** 2026-04-01
**Scope:** ember/devbox (billing, machines, credits), flare (store, reconciler), stoke (scheduler, session/store, workflow)
**Filter:** Operations where retry causes data corruption, double-charging, or state inconsistency.

---

## CRITICAL

- [ ] [CRITICAL] [ember/devbox/src/routes/billing.ts:403–404] **Webhook idempotency check is a non-atomic read-then-write (TOCTOU race)**
  The outer guard `SELECT id FROM billing_events WHERE stripe_event_id = ${event.id}` runs OUTSIDE any transaction. If Stripe delivers the same event concurrently (duplicate delivery is guaranteed by Stripe's at-least-once model), two webhook handlers can both pass this check simultaneously. Only the `checkout.session.completed → credit_purchase` branch is protected by an inner transaction with `FOR UPDATE`; all other branches (`invoice.paid`, `invoice.payment_failed`, `customer.subscription.*`) are NOT protected. `invoice.paid` in particular inserts a `billing_events` row with no `ON CONFLICT` guard, meaning a duplicate event delivery produces a duplicate ledger row. That row does not double-charge money but it corrupts the billing history and can confuse reconcilers that count `charge` events.
  — fix: Wrap the entire webhook body (not just the credit branch) in a single transaction that begins with `INSERT INTO processed_stripe_events ... ON CONFLICT DO NOTHING RETURNING event_id`; bail out immediately if the INSERT returns nothing. Remove the outer `SELECT` guard.
  — effort: small

- [ ] [CRITICAL] [ember/devbox/src/routes/billing.ts:472–473] **`invoice.paid` billing_events INSERT has no ON CONFLICT guard — duplicate events double-write ledger rows**
  Unlike the `checkout.session.completed` branch (which uses `ON CONFLICT (stripe_event_id) DO NOTHING` on its billing_events insert), the `invoice.paid` branch inserts directly with no conflict guard. The same Stripe event ID can produce multiple `charge` rows. This corrupts the billing history view (`/api/billing/history`) and any downstream accounting that sums `charge` amounts.
  — fix: Add `ON CONFLICT (stripe_event_id) DO NOTHING` to the billing_events INSERT on line 472, or fold the insert inside the `processed_stripe_events` transaction described above.
  — effort: trivial

- [ ] [CRITICAL] [ember/devbox/src/routes/billing.ts:167–208] **Stripe `subscriptionItems.create` is called BEFORE the DB slot is written, with compensation that is itself non-idempotent**
  If the DB transaction at line 187 fails after Stripe has successfully created the subscription item (line 171), the compensation path (line 203) calls `stripe.subscriptionItems.del`. But on a client retry of `POST /billing/checkout`, the intent is still `pending`, so the code calls `stripe.subscriptionItems.create` again with the same idempotency key `slot:${intent.id}`. Stripe returns the ORIGINAL (now deleted) item's response cached for 24 hours — the item no longer exists in Stripe but the code proceeds to write a local slot pointing to a deleted Stripe item. Result: orphaned active slot with no Stripe backing.
  — fix: After the compensation delete, also mark the intent as `failed` (already done) AND change the retry path to re-create the intent with a new ID, breaking Stripe's idempotency key cache tie.
  — effort: medium

---

## HIGH

- [ ] [HIGH] [ember/devbox/src/routes/billing.ts:276–299] **`syncSubscription` is not idempotent when called concurrently — SELECT + UPDATE is a TOCTOU race**
  `syncSubscription` does a `SELECT id FROM subscriptions WHERE stripe_subscription_id = ...` then either `UPDATE` or `INSERT` in separate statements. Two concurrent webhook events for the same subscription (e.g. `customer.subscription.updated` and `invoice.paid` arriving within milliseconds, which is common) both pass the SELECT with `existing = undefined`, then both try to INSERT, causing a unique-constraint violation on `stripe_subscription_id`. The error is swallowed by the outer `try/catch`, the second event returns 500, and Stripe retries it — triggering the same race again.
  — fix: Replace the SELECT + conditional INSERT/UPDATE with a single `INSERT ... ON CONFLICT (stripe_subscription_id) DO UPDATE SET ...` UPSERT, removing the race entirely.
  — effort: small

- [ ] [HIGH] [ember/devbox/src/routes/machines.ts:55–203] **Machine create is not idempotent — a client retry after a network timeout creates duplicate machines**
  There is no client-supplied idempotency key for `POST /api/machines`. A client that times out and retries will create a second DB row and a second Fly app. Both rows consume a slot (via the `FOR UPDATE SKIP LOCKED` reservation). The user ends up double-billed in slot usage. The Fly app name is generated with `fly.flyAppId()` fresh each call so two Fly apps will exist.
  — fix: Accept a client-provided `requestId` (like the billing checkout endpoint does). Before inserting, do `SELECT id FROM machines WHERE user_id = $1 AND name = $2 AND deleted_at IS NULL` and return the existing machine if found, or better: use an intent/idempotency record keyed on `(user_id, name)` or on a client-provided UUID.
  — effort: medium

- [ ] [HIGH] [ember/devbox/src/routes/machines.ts:186–201] **Fly provisioning failure partial-cleanup is non-atomic — slot can remain consumed after a crash**
  When Fly provisioning fails (line 186), the code updates the machine row to `state='error', slot_id=NULL` outside a transaction. If the process crashes between the Fly call failing and the `UPDATE machines SET ... slot_id = NULL` statement, the machine row stays in state `creating` with `slot_id` set. The user's slot is permanently consumed with no running machine. The reconciler does not recover this case.
  — fix: The machine create should be split into a proper saga with compensating DB state. At minimum, the error cleanup `UPDATE` (line 189) should set `state='error', slot_id=NULL, deleted_at=NOW()` in a single atomic write (which it does), but the process also needs crash recovery: a background job should mark any machine stuck in `state='creating'` for more than N minutes as `state='error'` and release the slot.
  — effort: medium

- [ ] [HIGH] [ember/devbox/src/routes/machines.ts:248–260] **`POST /:id/stop` is non-idempotent — a second call after partial failure re-invokes Fly stop on an already-stopped machine**
  After `fly.stopMachine()` succeeds but before `db.update(machines).set({ state: "stopped" })` completes, a crash or network timeout leaves DB state as `started`. The next retry calls `fly.stopMachine()` again. While redundant Fly stop calls are typically benign, the bigger problem is that `revokeTerminalSessionsForMachine` is called again, which double-revokes sessions (idempotent since `WHERE status = 'active'` filters, so this is safe), but the stop also does not check current machine state — it will attempt to stop a machine that may already be stopped at Fly, possibly returning an error that causes the endpoint to return 502 while the machine is actually stopped.
  — fix: Read current Fly machine state before issuing the stop command, or guard with `WHERE state = 'started'` on the DB update to detect concurrent stop calls.
  — effort: small

- [ ] [HIGH] [ember/devbox/src/routes/machines.ts:264–290] **`DELETE /:id` is non-idempotent — the `isNull(machines.deletedAt)` guard means a second retry returns 404 after partial success**
  If `fly.destroyApp()` succeeds but the subsequent DB update fails (network partition between app server and DB), the Fly app is gone. A client retry hits the 404 guard `isNull(machines.deletedAt)` since the DB row still has `deletedAt=null`, but that's actually wrong — the resource no longer exists. The user sees 404 and has no way to recover. Additionally, the `ok=false` path (line 286) sets `state='error'` but does not release the slot (`slotId=null` is NOT set on failure), so the slot remains bound to a machine in `error` state that can never be used or released through the normal flow.
  — fix: On the `ok=false` Fly destroy path, also null out `slot_id` so the slot is freed. For idempotency of the overall delete: accept a 404 on retry as a success condition (the resource is gone, which is what DELETE means).
  — effort: small

- [ ] [HIGH] [flare/internal/store/store.go:463–496] **`ClaimDriftedMachines` has a two-phase claim gap — machines claimed in Phase 1 may not be visible in Phase 2 under load**
  Phase 1 issues an `UPDATE ... WHERE id IN (SELECT ... FOR UPDATE SKIP LOCKED)` which claims machines. Phase 2 reads them back with `WHERE claimed_by = $1 AND claimed_at > now() - interval '10 seconds'`. If Phase 1 takes longer than 10 seconds (e.g. large batch + heavy DB load), Phase 2 returns an empty result set. The machines are claimed in the DB (blocking other reconcilers) but the current reconciler processes zero of them. They will be re-claimable after 2 minutes (`claimed_at < now() - interval '2 minutes'`), producing a 2-minute stall for those machines.
  — fix: Use a single CTE: `WITH claimed AS (UPDATE ... RETURNING id) SELECT ... FROM machines m JOIN claimed c ON c.id = m.id`. This makes claim + read atomic and eliminates the 10-second window.
  — effort: small

---

## MEDIUM

- [ ] [MEDIUM] [ember/devbox/src/routes/billing.ts:335–352] **`syncSubscription` slot cancellation loop is not atomic — a crash mid-loop leaves some slots cancelled and others active**
  The loop at line 346 cancels slots one-by-one with individual `UPDATE` statements outside a transaction. If the process crashes mid-loop, some slots are cancelled and others are not. On the next webhook delivery, `syncSubscription` re-enters and the already-cancelled slots are skipped (they don't appear in the `activeItemIds` check), so the result is eventually consistent — but the window of partial cancellation can allow machines to continue running on a partially-cancelled subscription.
  — fix: Wrap the slot cancellation loop in a transaction, or replace with a single `UPDATE slots SET status='cancelled' WHERE subscription_id=$1 AND stripe_subscription_item_id NOT IN (...)` set operation.
  — effort: small

- [ ] [MEDIUM] [ember/devbox/src/routes/credits.ts:37–43] **Stripe customer creation in `/api/credits/checkout` is not atomic — race between two concurrent checkout requests for the same user creates two Stripe customers**
  The code does `SELECT stripe_customer_id FROM users WHERE id = $1`, then if null, calls `stripe.customers.create(...)`. Two concurrent requests for the same user both see `customerId = null` and both create Stripe customers. The subsequent `UPDATE users SET stripe_customer_id = $1 WHERE id = $2` means whichever update runs second wins in the DB, but the first Stripe customer becomes orphaned (no DB reference). Note: the billing checkout endpoint at billing.ts:129–138 guards this correctly with `WHERE stripe_customer_id IS NULL` and then re-reads; credits.ts does not.
  — fix: Mirror the billing.ts pattern: use `UPDATE users SET stripe_customer_id = $1 WHERE id = $2 AND stripe_customer_id IS NULL`, then re-read to get the canonical ID. Also pass `idempotencyKey: 'customer:' + user.id` to `stripe.customers.create` as billing.ts does.
  — effort: trivial

- [ ] [MEDIUM] [ember/devbox/src/routes/machines.ts:297–346] **`POST /:id/terminal` creates a new terminal session and exchange code on every call with no deduplication**
  A client that double-submits (e.g. user double-clicks "Open Terminal") creates two `terminal_sessions` rows and two `exchange_codes` rows. Only one code is used; the other expires after 60 seconds. This is low-severity on its own, but the `terminal_sessions` rows accumulate and are never cleaned up except by the bulk revocation path. There is no garbage collection for sessions created but never confirmed. Under a pathological retry loop, a user could create hundreds of active session records.
  — fix: Before inserting, check for an unexpired exchange code for this `(machine_id, user_id)` and return it if still valid, or add a `LIMIT 1` active session guard per machine per user.
  — effort: small

- [ ] [MEDIUM] [flare/internal/reconcile/reconciler.go:219–239] **`actionCleanupDestroyed`: `DeleteHostnamesByMachine` and `DeleteMachine` are not atomic — a crash between them leaves the hostname record orphaned**
  Line 233 calls `DeleteHostnamesByMachine` (fire-and-forget, ignores errors), then line 234 calls `DeleteMachine`. If the process crashes after `DeleteHostnamesByMachine` succeeds but before `DeleteMachine`, the machine row persists in DB with `desired_state='destroyed'` but the hostname rows are gone. The reconciler will retry `actionCleanupDestroyed` and call `DestroyVM` on the placement daemon again — a redundant but typically idempotent operation — then call `DeleteMachine` again. This specific sequence is safe in practice but relies on `DestroyVM` being idempotent at the placement layer. The hostname delete being non-transactional with the machine delete is still a code smell.
  — fix: Wrap `DeleteHostnamesByMachine` + `DeleteMachine` in a DB transaction, or call `DestroyVM` first then do both DB operations atomically.
  — effort: small

- [ ] [MEDIUM] [stoke/internal/scheduler/scheduler.go:62–65] **Pre-populating `completed` for resumed tasks skips the `stateMu` lock — race with the dispatch loop**
  Lines 62–65 write to `s.completed` directly without holding `s.stateMu`. The dispatch loop (line 103) acquires `s.stateMu` before reading `s.completed`. If `Run` is somehow called from multiple goroutines (unlikely in practice, but the scheduler is a struct with no guard against it), the unsynchronized pre-population is a data race. More practically: `allResults` (line 57) is also written outside the lock at line 64.
  — fix: Move the pre-population loop inside a `s.stateMu.Lock()` block, or document clearly that `Run` must not be called concurrently on the same scheduler instance.
  — effort: trivial

- [ ] [MEDIUM] [stoke/internal/session/store.go:114–130] **`SaveAttempt` reads `history/{taskID}.json`, appends, and rewrites — concurrent calls for the same task ID corrupt the history file**
  `SaveAttempt` acquires `s.mu` (line 115), reads the existing attempts JSON, appends the new attempt, and rewrites. This is safe for single-process use. However, `addLearnedPattern` (line 125) calls `s.LoadLearning()` and `s.SaveLearning()` while `s.mu` is already held (line 115). `SaveLearning` calls `s.writeJSON` which does NOT acquire `s.mu` — but `LoadLearning` calls `s.readJSON` also without the lock. This creates a reentrant access pattern. If `s.mu` is a `sync.Mutex` (not `sync.RWMutex`), calling `LoadLearning` from within the locked `SaveAttempt` will deadlock on any Go runtime that does not re-enter mutexes (which is all of them).
  — fix: Either use a `sync.RWMutex` and restructure `addLearnedPattern` to release the write lock before calling internal helpers, or separate the learning persistence from the attempt persistence with its own mutex. The SQLStore version (`sqlstore.go`) does not have this issue since SQLite with WAL handles concurrent writes.
  — effort: small

- [ ] [MEDIUM] [stoke/internal/workflow/workflow.go:179–196] **Retry loop re-uses the same `name` base for worktree naming — a process restart re-enters the same worktree path**
  On attempt 2+, the worktree is named `{name}-attempt-{N}`. If the Stoke process crashes mid-attempt and is restarted, `Worktrees.Prepare(ctx, retryName)` may find a stale worktree with the same name from the previous run. The `Cleanup` called at the top of the retry loop (`line 184`) operates on the PREVIOUS attempt's handle, not the new one. Depending on `WorktreeManager.Prepare` implementation, this can either reuse dirty state (if it doesn't clean up on name collision) or fail outright.
  — fix: Include a run-unique nonce (e.g. timestamp or UUID) in the worktree name, or ensure `Prepare` always creates a fresh worktree when the name already exists (force-cleanup first).
  — effort: small

---

## Summary

| Severity | Count | Key Risk |
|----------|-------|----------|
| CRITICAL | 3 | Double credit disbursement, duplicate billing ledger rows, orphaned Stripe items |
| HIGH | 6 | Duplicate machine creation, stuck slots, non-atomic delete/stop, claim gap |
| MEDIUM | 6 | Concurrent customer creation, session accumulation, history deadlock, retry state collision |

**Highest-priority fixes (in order):**
1. Wrap all webhook branches in `processed_stripe_events` transaction (eliminates CRITICAL TOCTOU + double-insert).
2. Add `ON CONFLICT` to `invoice.paid` billing_events INSERT (trivial, independent fix).
3. Fix `syncSubscription` SELECT+INSERT race → single UPSERT.
4. Add idempotency key to `POST /api/machines` (prevents double-slot consumption on retry).
5. Release slot on DELETE failure path (prevents permanently stranded slots).
