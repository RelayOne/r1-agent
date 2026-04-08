# Scaling Consultant Audit

**Date**: 2026-04-01
**Scope**: ember/devbox, flare, stoke
**Focus**: What breaks at 100, 1000, 10000 concurrent users?

---

## Summary

**First scaling wall**: Ember's in-memory rate limiter fails the moment you run 2+ API instances (which you will do for availability). This is the immediate blocker.

**Second wall**: Ember's Postgres connection pool uses library defaults with no tuning. Under 100+ concurrent requests, connection starvation will cascade into 500s across all routes simultaneously.

**Third wall**: Flare's TAP networking is hard-capped at 253 VMs per host per /24 subnet. This is a design ceiling, not a bug, but it needs to be on the capacity planning radar.

**Good news**: Flare's control plane is already well-architected for horizontal scaling (leaderless reconciliation with SKIP LOCKED, proper DB indexing, host capacity reservations). Stoke is a local CLI tool and scaling concerns are mostly irrelevant.

---

## Findings

### EMBER (ember/devbox) -- The Hot Path

- [ ] **CRITICAL** [ember/devbox/src/rate-limit.ts:26-38] In-memory rate limiter is the default. `memStores` is a module-level `Map` -- when you deploy 2+ API instances (standard for availability), rate limits are per-process, not global. A user can multiply their rate limit by N instances. Auth brute-force protection (20 attempts/15min) becomes 20*N. -- fix: Set `RATE_LIMIT_BACKEND=postgres` in production config or switch default to postgres when `NODE_ENV=production`. The postgres backend already exists and works. -- effort: trivial

- [ ] **CRITICAL** [ember/devbox/src/connection.ts:7] `postgres(process.env.DATABASE_URL!)` uses the `postgres` library's default pool settings (no explicit `max`, `idle_timeout`, `connect_timeout`). Default max connections is 10. At 100 concurrent users hitting billing + machines + auth simultaneously, all 10 connections saturate and requests queue indefinitely. Every route shares this single pool, so one slow query (e.g., the detailed health check's 7 sequential COUNT queries) blocks all other routes. -- fix: Configure explicit pool: `postgres(process.env.DATABASE_URL!, { max: 25, idle_timeout: 20, connect_timeout: 5 })`. Match or exceed Flare's 25-connection setting. Add connection timeout so requests fail fast instead of hanging. -- effort: trivial

- [ ] **HIGH** [ember/devbox/src/rate-limit.ts:60-111] Postgres rate limiter does INSERT + COUNT + conditional DELETE on every single request. At 120 req/s (the API rate limit), that's 240-360 DB operations/second just for rate limiting. The `rate_limit_hits` table grows unbounded during traffic spikes; cleanup is async and best-effort (line 108: `.catch()`). -- fix: Replace with a sliding window counter using a single UPSERT per key per window bucket (e.g., 1-minute buckets). Or use Redis/Valkey for rate limiting -- it's the standard tool for this. This eliminates 2-3x DB round trips per request. -- effort: medium

- [ ] **HIGH** [ember/devbox/src/index.ts:165-226] Detailed health check runs 7 sequential `SELECT count(*)` queries against different tables, all in the request handler. Under load, this endpoint alone can hold a DB connection for 50-100ms. If monitoring polls this every 10s from multiple locations, it consumes connection pool capacity needed for user requests. -- fix: Cache the result for 30-60 seconds (simple in-memory TTL cache). Or run the queries in parallel with `Promise.all()`. -- effort: trivial

- [ ] **HIGH** [ember/devbox/src/db.ts] `sessions` table has no index on `userId`. Lucia's `validateSession()` is called on every authenticated request. Session lookup by `id` (PK) is fast, but any query filtering sessions by user (e.g., "invalidate all sessions for user") requires a full table scan. As the sessions table grows, this degrades. -- fix: Add `index("sessions_user_idx").on(sessions.userId)` -- effort: trivial

- [ ] **HIGH** [ember/devbox/src/db.ts] `slots` table has no index on `userId` or `subscriptionId`. Billing pages that list a user's slots (`WHERE user_id = ?`) do a sequential scan. With 1000 users * 2-3 slots each, this becomes measurably slow. -- fix: Add `index("slots_user_idx").on(slots.userId)` and `index("slots_subscription_idx").on(slots.subscriptionId)` -- effort: trivial

- [ ] **MEDIUM** [ember/devbox/src/db.ts] `machines` table has no index on `userId` or `state`. The dashboard lists a user's machines and the reconciler queries by state. Both are full table scans. -- fix: Add `index("machines_user_idx").on(machines.userId)` and `index("machines_state_idx").on(machines.state)` -- effort: trivial

- [ ] **MEDIUM** [ember/devbox/src/db.ts] `purchaseIntents` table has no index on `userId` or `status`. Finding a user's pending intents requires a full scan. -- fix: Add `index("purchase_intents_user_idx").on(purchaseIntents.userId, purchaseIntents.status)` -- effort: trivial

- [ ] **MEDIUM** [ember/devbox/src/db.ts] `exchangeCodes` table has no index on `machineId` or `expiresAt`. Expired code cleanup and code lookup by machine are both full scans. -- fix: Add `index("exchange_codes_machine_idx").on(exchangeCodes.machineId)` and `index("exchange_codes_expires_idx").on(exchangeCodes.expiresAt)` -- effort: trivial

- [ ] **MEDIUM** [ember/devbox/src/rate-limit.ts:65-66] Postgres rate limiter creates the `rate_limit_hits` table on first use via `CREATE TABLE IF NOT EXISTS` and `CREATE INDEX IF NOT EXISTS`. This is a DDL operation that acquires an AccessExclusive lock. In a race between 2 concurrent first requests, one will block. Not a production issue if the table already exists, but fragile for fresh deployments under load. -- fix: Move table creation to migrations. -- effort: trivial

### FLARE (flare) -- Well-Structured, Some Ceilings

- [ ] **HIGH** [flare/internal/networking/tap.go:78-79] TAP manager IP pool is limited to a single /24 subnet (IPs 2-254 = 253 VMs max per host). This is a hard ceiling. At 253 VMs, `Allocate()` returns "IP address pool exhausted" and the host is dead to new placements even if it has CPU/memory capacity. -- fix: Support multiple subnets per host. When current subnet is exhausted, allocate a new bridge + /24. Or use /16 addressing from the start. This is a design decision, not a quick fix -- document the 253-VM-per-host limit in capacity planning. -- effort: large

- [ ] **MEDIUM** [flare/internal/networking/tap.go:24-29] TAP manager state is entirely in-memory (`devices map`, `nextIP`, `freeIPs`). If the placement daemon restarts, this state is lost. `RecoverFromDisk()` in the firecracker manager restores VMs but the TAP manager's `freeIPs` pool isn't synchronized -- recovered VMs call `RecoverDevice()` which advances `nextIP` past recovered IPs, but freed IPs from destroyed VMs between the last recovery and now are lost forever (leaked from the pool). -- fix: The `RecoverDevice` method already handles the critical path (no IP collisions). The leaked IPs are a slow bleed (lose a few IPs per daemon restart). For now, document this. For production, persist TAP state to disk or rebuild `freeIPs` by diffing allocated IPs against the full 2-254 range on recovery. -- effort: small

- [ ] **MEDIUM** [flare/cmd/control-plane/main.go:148-156] VM ingress proxy creates a new `httputil.ReverseProxy` and `http.Client` for every request (inside `proxyVMTraffic`). At 1000 concurrent users with active terminals, this means 1000 short-lived TCP connections to placement daemons per second. Connection reuse is zero. -- fix: Cache reverse proxies per host. Use a `sync.Map` or LRU cache keyed by `hostInfo.IngressAddr`. Each proxy's `http.Transport` will then reuse connections via keep-alive. -- effort: small

- [ ] **MEDIUM** [flare/cmd/control-plane/main.go:670] Ingress proxy calls `ResolveHostname()` on every HTTP request, which executes up to 2 SQL queries (try generated hostname, then try custom hostname). At 100 req/s of terminal traffic, that's 100-200 queries/second just for routing. -- fix: Add an in-memory LRU cache with short TTL (5-10 seconds) for hostname resolution. Hostnames change rarely (only on VM create/destroy). -- effort: small

- [ ] **MEDIUM** [flare/cmd/control-plane/main.go:61] `db.SetMaxIdleConns(5)` with `SetMaxOpenConns(25)` means 20 connections are torn down and recreated repeatedly under moderate load. Each new connection requires a TCP handshake + TLS negotiation + Postgres auth. -- fix: Set `SetMaxIdleConns(25)` to match max open. Idle connections are cheap (just memory for the socket buffer). -- effort: trivial

### STOKE (stoke) -- Local Tool, Minimal Scaling Concerns

- [ ] **MEDIUM** [stoke/internal/scheduler/scheduler.go:86-167] Scheduler uses a busy-wait loop with `select/default` polling. When no tasks are dispatchable and workers are active, it correctly blocks on `<-results` (line 166). But the main dispatch loop (lines 86-101) drains results with a `select/default` pattern that spins the CPU when there are no results and no dispatchable tasks momentarily. For a CLI tool this is fine; if this scheduler ever runs server-side, it will burn CPU. -- fix: Not urgent for CLI usage. If server-side: replace the drain loop with a proper select on both `results` channel and a dispatch-ready signal. -- effort: small

- [ ] **MEDIUM** [stoke/internal/session/sqlstore.go:30] SQLite opened with `?_journal_mode=WAL&_busy_timeout=5000` but no `_synchronous=NORMAL` or connection pool limit. The default `sql.DB` pool can open multiple connections to the same SQLite file, but SQLite only supports one writer at a time. Under concurrent task completions, `SaveState` and `SaveAttempt` will serialize on SQLite's write lock with 5-second busy timeouts. -- fix: Add `db.SetMaxOpenConns(1)` after opening to serialize all access through one connection, eliminating busy-timeout contention. This is the standard pattern for SQLite with Go's `database/sql`. -- effort: trivial

---

## Scaling Wall Analysis

### At 100 concurrent users
- **Ember**: Connection pool (10 default) saturates. Rate limit DB overhead noticeable. Missing indexes cause slow queries on billing/machines pages.
- **Flare**: Handles fine. 25 DB connections sufficient. Reconciler batch size of 10 is adequate.
- **Stoke**: N/A (local CLI tool).

### At 1000 concurrent users
- **Ember**: Rate limiter at 240-360 DB ops/sec competes with application queries. In-memory rate limits useless with multiple instances. Health check endpoint becomes a DB bottleneck.
- **Flare**: Ingress proxy connection churn becomes measurable. Hostname resolution at 1000+ queries/sec without caching strains DB. Need to verify reconciler can keep up with machine churn.

### At 10000 concurrent users
- **Ember**: Needs Redis/Valkey for rate limiting and session caching. Postgres alone won't handle the query volume. Need read replicas.
- **Flare**: 253 VM per host limit means minimum 40 hosts for 10K VMs. Hostname cache is essential. May need multiple control plane instances (already supported via SKIP LOCKED).

---

## What's Done Well

1. **Flare reconciler**: Leaderless work-stealing with `FOR UPDATE SKIP LOCKED` is production-grade. Partial indexes on drifted machines are smart.
2. **Flare capacity management**: `PlaceAndReserve` with atomic SELECT FOR UPDATE prevents overcommit. Reservation release on failure is handled.
3. **Ember rate limiting**: Having both in-memory and Postgres backends with a config switch shows forethought. The postgres backend just needs to be the default.
4. **Stoke subscription manager**: Circuit breaker pattern with LRU spreading is sophisticated. The Acquire/Release pattern prevents double-dispatch.
5. **Ember idempotency**: Purchase intents as idempotency keys for Stripe operations is correct.
6. **Flare VM recovery**: `RecoverFromDisk()` + `RecoverDevice()` handles daemon restarts gracefully.
