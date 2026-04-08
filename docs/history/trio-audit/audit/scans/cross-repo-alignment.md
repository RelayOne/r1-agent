# Cross-Repo Alignment Audit: ember / flare / stoke

**Date:** 2026-04-01
**Scope:** API contracts, naming, auth flows, lifecycle states, dependency versions, missing integration

---

## 1. API Contract Alignment

### FINDING 1.1 -- stoke/ember.go calls worker endpoints that exist on ember but have critical response shape mismatches

**Repos:** stoke, ember
**Severity:** HIGH

stoke's `EmberBackend.Spawn()` (stoke/internal/compute/ember.go:59) sends `POST /v1/workers` and expects a response of:
```json
{ "id": "...", "hostname": "...", "state": "..." }
```

ember's `POST /v1/workers` handler (ember/devbox/src/routes/workers.ts:116-131) returns:
```json
{ "id": "...", "hostname": "...", "status": "active", "expires_at": "...", "cost_cents": ..., "credits_remaining": ... }
```

**Mismatches:**
- stoke reads `state`, ember returns `status`. stoke will always see `state` as empty string.
- stoke does not send `idempotency_key`, `ttl_minutes`, or `task_description` -- all accepted by ember but stoke misses features.
- stoke sends `repo_url`, `branch`, `env`, and `metadata` fields. ember's `createWorkerSchema` accepts `repo_url` and `metadata` but does NOT accept `branch` or `env` (these are silently dropped by zod parsing).
- stoke sends `name` as `"stoke-{taskID}"` and `size` as `"4x"` -- ember expects sizes like `"performance-4x"`, `"performance-8x"`, `"performance-16x"`. The size `"4x"` will be rejected with 400 error `"Invalid size"`.

**Fix:** stoke must prefix sizes with `"performance-"`, read `status` instead of `state`, and add `branch`/`env` support to ember's worker schema if needed.

### FINDING 1.2 -- stoke polls /v1/workers/:id/status but ember returns different shape

**Repos:** stoke, ember
**Severity:** HIGH

stoke's `flareWorker.getState()` (ember.go:154) calls `GET /v1/workers/{id}/status` and expects:
```json
{ "state": "..." }
```

ember's handler (workers.ts:147-156) returns the full `worker_allocations` row:
```json
{ "id": "...", "status": "...", "worker_type": "...", "cost_cents": ..., ... }
```

stoke reads `.state` but the field is `.status`. The poll loop will never see "running" -- it will spin for 60s and fail with "worker did not start within 60s".

**Fix:** stoke must read `status` field. Also, ember's worker statuses are `pending | active | completed | failed | expired | cancelled` -- stoke checks for `"running"` and `"error"` and `"destroyed"`, none of which ember ever returns. stoke should check for `"active"` (maps to running) and `"failed"` (maps to error).

### FINDING 1.3 -- stoke calls /v1/workers/:id/exec which does not exist on ember

**Repos:** stoke, ember
**Severity:** HIGH

stoke's `flareWorker.Exec()` (ember.go:169) calls `POST /v1/workers/{id}/exec`.
stoke's `flareWorker.Upload()` calls `POST /v1/workers/{id}/upload`.
stoke's `flareWorker.Download()` calls `GET /v1/workers/{id}/download`.
stoke's `flareWorker.Stop()` calls `POST /v1/workers/{id}/stop`.
stoke's `flareWorker.Destroy()` calls `DELETE /v1/workers/{id}`.

ember provides `DELETE /v1/workers/:id` and `POST /v1/workers/:id/stop` but does NOT have `/exec`, `/upload`, or `/download` endpoints. These are currently not implemented in ember's workers.ts.

For exec/upload/download, stoke needs to either:
1. Call these directly on the provisioned machine hostname (via the Fly app), or
2. ember needs to add proxy endpoints that forward to the machine.

Note: ember's `machines.ts` has an upload endpoint (`POST /api/machines/:id/upload`) that forwards to the machine, but this is on the `/api/` browser-session-auth path, not `/v1/` API-key path.

**Fix:** ember needs `/v1/workers/:id/exec`, `/v1/workers/:id/upload`, and `/v1/workers/:id/download` endpoints, or stoke needs to exec on the machine hostname directly.

### FINDING 1.4 -- flare's exec endpoint is disabled (501)

**Repos:** flare
**Severity:** MEDIUM

flare's control plane returns 501 for `POST /v1/apps/{app}/machines/{id}/exec` (main.go:544-547) with message "exec not available in v1". If stoke ever talks to flare directly (bypassing ember), exec will not work.

The flare TypeScript SDK exposes `machines.exec()` (index.ts:134-139), which will always fail against the real control plane.

**Fix:** Either implement exec in flare or remove it from the SDK to avoid misleading consumers.

---

## 2. Flare SDK vs Ember

### FINDING 2.1 -- ember does NOT use the flare TypeScript SDK

**Repos:** ember, flare
**Severity:** MEDIUM (architectural)

ember's `fly.ts` talks directly to the Fly.io Machines API (`https://api.machines.dev`). It does not import or reference the flare SDK at all. ember provisions machines via Fly.io, not via flare.

The flare SDK (flare/sdk/typescript/src/index.ts) exists but has zero consumers in the ember codebase.

**Implication:** ember and flare are currently independent paths to VM provisioning. ember uses Fly.io directly. stoke's `EmberBackend` talks to ember (which provisions on Fly.io). Flare is a separate microVM platform that is not yet integrated with ember.

### FINDING 2.2 -- stoke's EmberBackend name is misleading

**Repos:** stoke
**Severity:** LOW

`EmberBackend.Name()` returns `"flare"` (ember.go:35) but the backend talks to ember's `/v1/workers` endpoints, not to flare's API. The comment says "spawns Flare microVMs via the Ember API" but ember currently provisions Fly.io machines, not flare VMs.

**Fix:** Either rename to `Name() = "ember"` or update ember to provision via flare.

---

## 3. Machine/Session Lifecycle

### FINDING 3.1 -- Machine state vocabularies diverge across all three repos

**Repos:** ember, flare, stoke
**Severity:** HIGH

| State concept | ember (db.ts) | flare (types.go) | stoke (ember.go) |
|---|---|---|---|
| Being created | `creating` | `creating`, `created` | (not checked) |
| Running | `started` | `running` | `running` |
| Stopped | `stopped` | `stopped` | (not checked) |
| Error | `error` | `failed` | `error` |
| Destroyed | `deleted` | `destroyed` | `destroyed` |
| Special | `needs_stop` | `lost`, `unknown`, `starting`, `stopping` | -- |

Key conflicts:
- ember calls running machines `"started"`, flare calls them `"running"`. If ember migrates to flare, the state names will change.
- ember calls destroyed machines `"deleted"`, flare calls them `"destroyed"`.
- ember's worker_allocations table uses yet another set: `pending | active | completed | failed | expired | cancelled | needs_cleanup`.
- stoke checks for `"running"`, `"error"`, `"destroyed"` but ember workers return `"active"`, `"failed"`, `"completed"`.

### FINDING 3.2 -- ember workers are ephemeral, stoke expects persistent VMs

**Repos:** stoke, ember
**Severity:** MEDIUM

stoke's `flareWorker.Stop()` calls `/v1/workers/:id/stop`. ember's stop handler (workers.ts:192-216) destroys the Fly app entirely (calls `fly.destroyApp()`). There is no way to stop-and-restart a burst worker. After "stop", the worker is gone.

stoke's compute interface has both `Stop()` (preserve state, can restart) and `Destroy()` (terminate permanently). ember conflates these: both stop and delete destroy the worker.

**Fix:** Document that burst workers are ephemeral. stoke should treat `Stop()` and `Destroy()` as equivalent for ember workers.

---

## 4. Auth Token Flow

### FINDING 4.1 -- stoke authenticates to ember via API key (correct)

**Repos:** stoke, ember
**Severity:** OK (no issue)

stoke uses `EMBER_API_KEY` env var, sends as `Bearer` token. ember's `/v1/*` routes use `requireApiKeyV1()` which validates via SHA-256 hash lookup in `api_keys` table. This is correct and aligned.

### FINDING 4.2 -- ember does NOT authenticate to flare (no integration exists)

**Repos:** ember, flare
**Severity:** INFO

ember talks to Fly.io via `FLY_API_TOKEN`. It does not talk to flare at all. If/when ember migrates to flare, it would need `FLARE_API_KEY` and the flare SDK.

### FINDING 4.3 -- stoke's EmberBackend uses same API key for both worker management and exec/upload

**Repos:** stoke, ember
**Severity:** MEDIUM

stoke sends the same `EMBER_API_KEY` when calling `/v1/workers` (management) and when calling `/v1/workers/:id/exec` (execution on the worker). This is fine for management endpoints but won't work for direct machine communication, which uses `machineToken` (per-machine secret).

If exec/upload/download are implemented as direct calls to the machine, stoke would need the per-machine token, not the API key.

---

## 5. Naming Consistency

### FINDING 5.1 -- "machine" vs "worker" vs "VM" vs "instance"

**Repos:** all three
**Severity:** MEDIUM

| Concept | ember | flare | stoke |
|---|---|---|---|
| Compute unit (user-facing persistent) | `machine` | `machine` (API), `VM` (internal) | -- |
| Compute unit (burst/ephemeral) | `worker` (in workers.ts), `worker_allocations` table | -- | `worker` (compute interface) |
| The thing stoke creates | calls ember's worker API | -- | `Worker` interface |

The naming is mostly aligned but there's confusion because:
- ember has both "machines" (persistent, slot-based) and "workers" (ephemeral, credit-based)
- stoke's `EmberBackend` uses the worker (ephemeral) path
- stoke's internal type is `flareWorker` even though it talks to ember

### FINDING 5.2 -- "app" vs "project" inconsistency

**Repos:** ember, flare
**Severity:** LOW

flare has an `apps` concept (inherited from Fly.io API compatibility). ember creates a Fly app per machine (`fly.createFlyApp(flyAppName)`). There's a 1:1 relationship between ember machine and Fly app.

If ember migrates to flare, it would need to map this: either one flare app per user, or one flare app per machine.

---

## 6. Dependency Versions

### FINDING 6.1 -- Shared Go dependencies with minor version differences

**Repos:** stoke, flare
**Severity:** LOW

| Package | stoke (go.mod) | flare (go.mod) |
|---|---|---|
| Go version | 1.23.2 | 1.23 |
| golang.org/x/sync | v0.10.0 | v0.10.0 |
| golang.org/x/text | v0.3.8 | v0.21.0 |

- `golang.org/x/text`: stoke uses v0.3.8, flare uses v0.21.0. This is a significant gap (v0.3.8 is from ~2022). Not a runtime conflict since they're separate binaries, but stoke should upgrade.
- No shared direct dependencies (stoke uses SQLite + Bubble Tea, flare uses Postgres).

---

## 7. Missing Integration

### FINDING 7.1 -- No flare integration in ember at all

**Repos:** ember, flare
**Severity:** HIGH (architectural gap)

ember exclusively uses Fly.io for VM provisioning. flare is a complete microVM control plane with placement, reconciliation, and host management. These two systems serve the same purpose but are not connected.

The likely intended architecture is: ember -> flare -> bare metal hosts. Currently it is: ember -> Fly.io.

**What needs to happen:** ember needs a compute abstraction layer (similar to stoke's `Backend` interface) that can target either Fly.io or flare. The flare TypeScript SDK exists for this purpose but is unused.

### FINDING 7.2 -- stoke has no direct flare integration

**Repos:** stoke, flare
**Severity:** INFO

stoke talks to ember, which talks to Fly.io. stoke never talks to flare directly. This is likely correct for the current architecture (ember is the "control plane for developers", flare is the "control plane for VMs").

### FINDING 7.3 -- flare exec endpoint is disabled but stoke needs it

**Repos:** stoke, flare
**Severity:** HIGH

stoke's entire compute model depends on exec (running commands in workers). The exec path is: stoke -> ember /v1/workers/:id/exec -> (presumably) machine. But:
1. ember has no `/v1/workers/:id/exec` endpoint
2. flare has exec disabled (501)
3. The only exec path that works is stoke's `LocalBackend`

This means remote/burst execution (the core value prop of stoke + ember) cannot currently work end-to-end.

### FINDING 7.4 -- ember sessions API is aligned with stoke

**Repos:** stoke, ember
**Severity:** OK (well aligned)

stoke's `SessionReporter` (remote/session.go) calls:
- `POST /v1/sessions` with `{ "plan_id": "..." }` -- matches ember's sessions.ts schema
- `PUT /v1/sessions/:id` with `{ status, tasks, total_cost_usd, burst_workers }` -- matches ember's updateSchema
- `GET /v1/sessions/:id` -- matches ember's response

This is one of the few well-aligned integration points.

### FINDING 7.5 -- ember managed AI proxy is aligned with stoke

**Repos:** stoke, ember
**Severity:** OK (well aligned)

stoke's `managed.Proxy.Chat()` calls `POST /v1/ai/chat` with OpenAI-compatible format. ember's ai routes (behind `ENABLE_MANAGED_AI` flag) accept this. Auth via same `EMBER_API_KEY`.

### FINDING 7.6 -- Worker TTL enforcement is ember-only

**Repos:** stoke, ember
**Severity:** MEDIUM

ember workers have a `ttl_minutes` and `expires_at` field. stoke does not set `ttl_minutes` when spawning workers (defaults to 30 min in ember). stoke also does not track or handle TTL expiration -- if a worker expires mid-task, stoke will get errors on subsequent exec calls with no understanding of why.

**Fix:** stoke should set appropriate TTL based on expected task duration, and handle the case where a worker expires.

---

## Summary of Critical Issues

1. **stoke -> ember worker API is broken** (wrong size format, wrong response field names, missing exec/upload/download endpoints). Remote execution cannot work.
2. **State name misalignment** across all three repos will cause integration bugs.
3. **ember has no flare integration** -- the flare TypeScript SDK exists but is unused.
4. **flare exec is disabled** -- even if stoke talked directly to flare, exec wouldn't work.
5. **stoke doesn't handle worker TTL** -- workers will expire silently mid-task.

## Recommended Priority

1. Fix stoke->ember API contract (size format, response parsing) -- immediate blocker
2. Add exec/upload/download to ember workers or establish direct machine communication path
3. Standardize state names across repos
4. Plan ember->flare migration strategy
5. Add TTL awareness to stoke's compute layer
