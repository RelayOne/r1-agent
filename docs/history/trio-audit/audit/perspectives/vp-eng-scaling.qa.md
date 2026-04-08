# VP Engineering — Horizontal Scaling Audit
**Date:** 2026-04-01
**Scope:** ember/devbox, flare, stoke
**Focus:** Issues that break at 10x current load or prevent horizontal scaling

---

## CRITICAL

- [ ] [CRITICAL] [ember/devbox/src/rate-limit.ts:28] **In-memory rate limiter is default; silently ineffective across multiple instances** — `memStores` is a process-local `Map`. With N api instances, each instance only sees its own traffic, so the effective rate limit becomes `N × max`. A user can send N×120 requests/min globally before any single instance rejects. The comment says to set `RATE_LIMIT_BACKEND=postgres` but nothing enforces it in production validation. — fix: Add `RATE_LIMIT_BACKEND` to the required production env vars list in `validateProductionConfig()` in `index.ts`; make postgres backend the default. — effort: trivial

- [ ] [CRITICAL] [flare/internal/networking/tap.go:23-30] **TAP/IP allocator is a singleton in-process `Manager` struct — hard node affinity** — The `Manager` holds `nextIP`, `freeIPs`, and `devices` entirely in memory. Every placement daemon is a single-node process (correct by design), but this means the IP namespace is bounded to a single /24 (253 addresses). At 10x load, if a placement host runs >253 concurrent VMs the pool is exhausted with no overflow or multi-subnet support. Additionally, there is zero coordination across hosts for the shared bridge namespace (each host has its own `br0`), which is correct, but the data structure gives no observable signal before exhaustion. — fix: Add a capacity check at startup that logs/alerts when `nextIP > 200` (approaching /24 limit). Document max-VMs-per-host as a first-class config. For true 10x, plan multi-bridge or /22 subnet support in the placement daemon. — effort: small

- [ ] [CRITICAL] [flare/internal/firecracker/manager.go:51-58] **Firecracker VM map (`m.vms`) is entirely in-memory — placement daemon has no shared state, cannot be scaled horizontally** — The `Manager.vms` map is per-process. Two placement daemons on the same host would have independent state — each believes it owns 100% of capacity. This is currently correct because one daemon runs per physical host, but the absence of a database-backed record on the placement side means: (a) the control plane must track which host owns which VM via Postgres, and (b) if the placement daemon restarts mid-create, there is a window where the control plane thinks a VM exists but the daemon's in-memory map is empty until `RecoverFromDisk()` completes. The recovery path exists, but control-plane-side `observed_state` is not updated during recovery, so the reconciler may trigger a redundant re-start. — fix: After `RecoverFromDisk()`, call the control plane's heartbeat or a `/internal/hosts/sync` endpoint to push recovered VM states. — effort: medium

- [ ] [CRITICAL] [ember/devbox/src/index.ts:196-225] **`/api/health/detailed` issues 7 sequential full-table COUNT queries with no caching** — Each admin request hits the DB 7 times. This is currently benign but becomes a problem when this endpoint is called by monitoring systems at high frequency or when tables grow to millions of rows. At 10x scale, a monitoring poll every 10 seconds × 7 queries each = 42 queries/10s from health checks alone. — fix: Cache the result with a 30-second TTL using a shared Redis/Postgres cache, or run a single aggregation query. — effort: small

---

## HIGH

- [ ] [HIGH] [ember/devbox/src/connection.ts:7] **`postgres()` called with no explicit pool bounds — connection pool is uncapped** — The `postgres` npm client defaults to 10 connections. At 10x concurrency this saturates quickly; at 100x it crashes Postgres. There is no `max`, `idle_timeout`, or `connect_timeout` configured. — fix: `postgres(DATABASE_URL, { max: 20, idle_timeout: 30, connect_timeout: 10 })`. Tune to PgBouncer or database max_connections ceiling. — effort: trivial

- [ ] [HIGH] [ember/devbox/src/routes/admin.ts:103-128] **Ban handler iterates over all user machines in a sequential `for...of` loop, one Fly API call per machine** — `for (const m of userMachines)` with `await fly.destroyApp()` serializes network calls. A user with 20 machines → 20 sequential HTTP calls, each potentially timing out. This also holds the request open for the full duration, risking gateway timeout. — fix: Use `Promise.allSettled(userMachines.map(...))` for parallel fan-out, with per-item timeout guards. — effort: small

- [ ] [HIGH] [ember/devbox/src/routes/admin.ts:51] **`GET /admin/users/:id` issues 4 queries including one unbounded `SELECT * FROM machines WHERE user_id = ?` with no LIMIT** — A power user with hundreds of machines returns an arbitrarily large payload. — fix: Add `.limit(100)` and paginate, or project fewer columns. — effort: trivial

- [ ] [HIGH] [ember/devbox/src/routes/machines.ts:40-52] **`GET /api/machines` returns ALL non-deleted machines for a user with no pagination or LIMIT** — As a user accumulates deleted+recreated machines over time, this query grows unbounded. Drizzle Postgres driver will buffer the entire result set in memory on the API server. — fix: Add `.limit(200)` with cursor-based pagination; expose `?cursor=` parameter. — effort: small

- [ ] [HIGH] [flare/internal/store/store.go:53] **`ListDeletingApps` is an unbounded table scan with no LIMIT** — Used by the reconciler to find apps to clean up. At 10x scale with many deleting apps queued, this scans the entire `apps` table. If many apps are stuck in `deleting`, this becomes a full sequential scan on every reconciler tick (every 5 seconds). — fix: Add `LIMIT $1` with a batch size parameter, consistent with `ClaimDriftedMachines`. — effort: trivial

- [ ] [HIGH] [flare/internal/store/store.go:576-595] **`ListDeadHostMachines` is an unbounded scan across all machines joined to dead hosts** — Called on every reconciler loop tick. At 10x scale with many dead hosts or machines, this performs a join scan with no LIMIT. — fix: Add `LIMIT $1` (e.g., 50) and process in batches in the reconciler. — effort: trivial

- [ ] [HIGH] [stoke/internal/subscriptions/manager.go:52-54] **Pool `Manager` state is entirely in-process — no coordination across stoke instances** — All pool status (busy/idle/throttled/circuit-open), utilization, and `LastPolled` live in `Manager.mu`-protected memory. If stoke is run as multiple instances (e.g., multiple workers in a CI cluster), each instance has its own view of pool availability. The same pool can be simultaneously marked busy=false on two instances, causing concurrent double-acquisition and rate limit hammering. — fix: Persist pool acquisition state to a shared backend (Redis SETNX, or a Postgres row with SELECT FOR UPDATE). Alternatively, document that stoke is a single-instance orchestrator and enforce it. — effort: medium

- [ ] [HIGH] [ember/devbox/src/routes/credits.ts:81-84] **`GET /api/credits/balance` issues an unbounded `worker_allocations` query** — The `active_workers` query has no LIMIT on the `ORDER BY created_at DESC`. Although filtered to `status IN ('pending', 'active')`, a bug that fails to close allocations would cause this to grow without bound. — fix: Add `LIMIT 50`. — effort: trivial

---

## MEDIUM

- [ ] [MEDIUM] [ember/devbox/src/rate-limit.ts:60-71] **Postgres rate limit backend runs `CREATE TABLE IF NOT EXISTS` on every cold-start per request** — The DDL is inside `pgCheck()` and fires on the first request after process start. This serializes the first request through DDL execution and holds a lock. On a fleet of instances all starting simultaneously, multiple DDL executions will contend. — fix: Run schema migrations at startup (not lazily on first request). Move `CREATE TABLE IF NOT EXISTS` into the startup flow or a dedicated migration script. — effort: small

- [ ] [MEDIUM] [ember/devbox/src/rate-limit.ts:108] **Postgres rate limit cleanup is fire-and-forget with no back-pressure** — `pgSql\`DELETE FROM rate_limit_hits WHERE ts < ...\`.catch(...)` is unawaited. Under sustained load, cleanup queries can pile up, consuming connection pool slots and creating write amplification. — fix: Throttle cleanup to run at most once per minute using a module-level timestamp guard. — effort: trivial

- [ ] [MEDIUM] [flare/cmd/control-plane/main.go:60-64] **Control plane DB pool has only 5 idle connections configured** — `db.SetMaxIdleConns(5)` with `db.SetMaxOpenConns(25)`. Under burst traffic, connections are repeatedly opened/closed, each requiring a TLS handshake. At 10x load with concurrent reconciler + API traffic, the idle pool will be constantly exhausted. — fix: Raise `SetMaxIdleConns` to match or be close to `SetMaxOpenConns` (e.g., 20). Add `db.SetConnMaxLifetime(5 * time.Minute)`. — effort: trivial

- [ ] [MEDIUM] [flare/cmd/placement/main.go:204-227] **Heartbeat loop uses `http.DefaultClient` with no timeout** — The heartbeat sends to the control plane using Go's default HTTP client, which has no timeout. A slow control plane response blocks the heartbeat goroutine indefinitely, causing the host to appear dead from the control plane's perspective (no heartbeat ACK). The goroutine leaks silently. — fix: Replace `http.DefaultClient` with a `&http.Client{Timeout: 10 * time.Second}` for heartbeat requests. — effort: trivial

- [ ] [MEDIUM] [flare/cmd/control-plane/main.go:662-691] **VM ingress proxy allocates a new `httputil.ReverseProxy` on every request** — `httputil.NewSingleHostReverseProxy(target)` is called per-request inside `proxyVMTraffic`. Each proxy instance creates its own `http.Transport` with its own connection pool, so TCP connections to backend VMs are never reused across requests. At 10x traffic, this causes connection exhaustion and TLS handshake overhead on every proxied request. — fix: Cache `ReverseProxy` instances per target host (keyed by `hostInfo.IngressAddr`) in a `sync.Map`. — effort: small

- [ ] [MEDIUM] [stoke/internal/session/store.go:27-38] **Session `Store` uses a single `sync.Mutex` for all file writes — serializes all concurrent task completions** — In `scheduler.go`, tasks complete concurrently and each calls `SaveState` + `SaveAttempt`, both of which hold `s.mu`. At 10x parallelism (e.g., `maxWorkers=20`), all 20 goroutines serialize through this mutex when persisting results, creating a write bottleneck. The SQLite store (`sqlstore.go`) with WAL mode is the correct fix. — fix: Default to `SQLStore` (SQLite + WAL) when available; reserve the JSON `Store` for simple/single-task use cases. Document the performance tradeoff. — effort: small

- [ ] [MEDIUM] [ember/devbox/src/routes/machines.ts:496-504] **`revokeAllTerminalSessionsForUser` fetches all running machines and fans out Fly API calls without a timeout or concurrency limit** — `Promise.allSettled(userMachines.map(...))` with no concurrency cap. If a user has 50 machines, 50 simultaneous outbound HTTP calls are fired. Combined with Fly rate limits, this could cause cascading 429 errors and exhaust the connection pool. — fix: Add `p-limit` or equivalent to cap concurrency at 5–10; add per-call `AbortSignal.timeout(5000)`. — effort: small

- [ ] [MEDIUM] [stoke/internal/pools/pools.go:44-69] **`LoadManifest()` / `Save()` have no file locking — concurrent stoke processes on the same machine will corrupt the manifest** — The manifest is read, modified in memory, and written back without any advisory lock (`flock`). Two `stoke pool add` commands running simultaneously will produce a corrupted manifest (last write wins, losing the first pool). — fix: Use an `os.OpenFile` with exclusive lock (`syscall.LOCK_EX`) around read-modify-write, or use a lock file. — effort: small

---

## Summary by repo

| Repo | Critical | High | Medium |
|---|---|---|---|
| ember/devbox | 2 | 5 | 4 |
| flare | 2 | 2 | 3 |
| stoke | 0 | 1 | 2 |

## Top 3 actions for immediate 10x readiness

1. **ember rate limiter** — enforce `RATE_LIMIT_BACKEND=postgres` in production config validation (`index.ts:validateProductionConfig`). Zero architectural change, single-line fix, eliminates the most dangerous scaling hole.
2. **ember connection pool** — add explicit bounds to `postgres()` in `connection.ts`. Prevents DB connection exhaustion under burst traffic.
3. **flare ingress proxy** — cache `ReverseProxy` per backend host in `proxyVMTraffic`. Eliminates per-request transport allocation which becomes the dominant latency source at 10x ingress traffic.
