<!-- STATUS: done -->
<!-- BUILD_COMPLETED: 2026-04-22 -->
<!-- CREATED: 2026-04-21 -->
<!-- DEPENDS_ON: executor-foundation, event-log-proper (for durable task state) -->
<!-- BUILD_ORDER: 19 -->

# Agent Serve Async — Worker Pool, Persistence, Webhooks

## 1. Overview

`internal/agentserve/` today ships a synchronous MVP: `POST /api/task` decodes the request, looks up an `executor.Executor` by `TaskType`, calls `ex.Execute` inline, and returns the final `TaskState` JSON. The HTTP connection is held for the whole duration — anything longer than a few seconds ties up a socket and risks upstream timeouts. Task state lives in a `map[string]*TaskState` under a mutex; a server restart loses every in-flight task. There is no cancellation endpoint, no progress stream, no push notification. Third-party callers must poll `GET /api/task/{id}` if they want status, and if the server crashes they have no way to know the task is gone.

This spec upgrades the facade to async-capable while preserving the sync path as a backwards-compatible opt-in. Async mode returns `202 Accepted` with `{status:"queued", id:"t-..."}` immediately; the task runs in a bounded worker pool; callers can poll `GET /api/task/{id}`, subscribe to `GET /api/task/{id}/events` (SSE), register a webhook via `POST /api/webhooks/register` for push delivery on completion, or cancel in-flight work via `POST /api/task/{id}/cancel`. Task state is persisted to the `internal/eventlog/` log from the event-log-proper spec so server restarts recover in-flight work and resume execution (bounded by a `retry_count` to prevent crash loops). Webhook delivery is retried with exponential backoff and signed with HMAC-SHA256; failed deliveries dead-letter to a local JSONL file. Sync mode stays the default for now (`STOKE_SERVE_ASYNC=1` flips the switch); the new endpoints are live in both modes, only the `POST /api/task` response shape changes based on flag + `X-Stoke-Sync` header.

## 2. New endpoints

All additions live in `internal/agentserve/server.go`. Routes wired in `(*Server).Handler`.

- `POST /api/task/{id}/cancel` — cancel a running task. Body: empty or `{"reason":"..."}`. Response: `200 OK` + current `TaskState` on success, `404` if task unknown, `409` if task is already in a terminal state (`completed`, `failed`, `cancelled`). Sends a cancel signal via the task's `context.CancelFunc`; worker honors `ctx.Done()` to exit early. Emits an `agentserve.task.cancelled` event to the bus + eventlog.
- `GET /api/task/{id}/events` — Server-Sent Events stream scoped to events with `Scope.TaskID == id`. Content-Type: `text/event-stream`; each frame is `data: <json>\n\n`. Backed by a new subscriber on `internal/bus/` that fans out per task. Closes when the task reaches a terminal state (server writes a final `event: end\ndata: {}\n\n` and closes). Honors `Last-Event-ID` header to replay from the eventlog's `sequence` column — lets a reconnecting client resume without gaps.
- `POST /api/webhooks/register` — operator registers a URL + bearer + event-type filter. Body: `{url, bearer, types: ["task.completed","task.failed"], secret}`. Server POSTs matching `TaskState` payloads to that URL. Response: `201 Created` + `{webhook_id, verified_at}`. Caller must pass their existing `X-Stoke-Bearer` (reuse server auth); webhook `secret` is for HMAC payload signing (see §5). List via `GET /api/webhooks`, delete via `DELETE /api/webhooks/{id}`.

## 3. Worker pool

File: `internal/agentserve/pool.go`.

- Configurable size via `Config.WorkerCount` (default `runtime.NumCPU()`).
- Job queue: buffered channel of `*taskJob` with capacity 1000. On full queue, `POST /api/task` returns `503 Service Unavailable` + `Retry-After: 5` header.
- Each worker goroutine loops: `select { case job := <-queue: run(job); case <-shutdown: drain-and-exit }`.
- `run(job)` wraps `ex.Execute(ctx, plan, effort)`, catches panics (recover → mark `failed` with `panic: <msg>` error), updates `TaskState` under `s.mu`, and emits lifecycle events (`task.started`, `task.completed` or `task.failed`) via `eventlog.EmitBus`.
- Graceful shutdown: `(*Pool).Shutdown(ctx)` stops accepting new jobs (`close(queue)`), waits for in-flight workers up to `Config.ShutdownDeadline` (default 30 s), then cancels the shared parent context so remaining workers observe `ctx.Done()` and exit. Returns `ErrShutdownDeadline` if any worker was still running at deadline.

## 4. Persistence

All task lifecycle events go through `eventlog.EmitBus`, so task state is reconstructable from the events table. The in-memory `s.tasks` map is now a cache, not the source of truth.

- On every task transition (`queued → running → completed|failed|cancelled`), emit an `agentserve.task.<state>` event with payload = the full `TaskState` JSON + `retry_count`.
- On server startup: call `eventlog.Log.ReadFrom(0)` filtered to `type LIKE 'agentserve.task.%'`; fold events per `task_id` to rebuild the `s.tasks` map in memory.
- **Recovery policy**: any task whose last observed state is `queued` or `running` gets requeued with `retry_count++`. Cap at 3; past that, mark `failed` with `error: "crash recovery retry cap exceeded"` and leave it.
- Emit `agentserve.task.recovered{task_id, retry_count}` on requeue so operators can see recovery activity.
- Persist webhook registrations to eventlog as `agentserve.webhook.registered` / `agentserve.webhook.deleted` events; fold on startup same way. Webhook secrets are stored in the event payload — callers must treat the eventlog DB as sensitive (document this in the deploy guide; no code change).

## 5. Webhook delivery

File: `internal/agentserve/webhook.go`.

- Delivery runs in a dedicated goroutine per webhook registration (simple; our registration count is small — typically ≤ 10).
- Triggered by a bus subscriber on matching `type` filters. Each delivery builds:
  - HTTP POST to the registered URL.
  - `Content-Type: application/json`.
  - `X-Stoke-Signature: sha256=<hex>` header where `<hex> = HMAC-SHA256(secret, body)`. Secret is the per-registration value.
  - `X-Stoke-Delivery-ID: <ulid>` for idempotent retry.
  - Body: the `TaskState` JSON as it appears in `GET /api/task/{id}` at the moment of emission.
- Retry: 5 attempts, exponential backoff `1s, 5s, 30s, 2min, 10min`. Jitter ±20 %. Attempts happen in the delivery goroutine; not worker-blocking.
- Success: HTTP 2xx. Everything else (non-2xx, timeout, TCP error) counts as failure.
- Dead letter: after 5 failed attempts, append one JSONL line to `internal/agentserve/deadletter.jsonl` (path configurable via `Config.DeadLetterPath`, default `.stoke/agentserve-deadletter.jsonl`). Line shape: `{timestamp, webhook_id, task_id, url, attempts, last_error, payload}`. File is append-only; no rotation in this spec.
- Emit `agentserve.webhook.delivered{webhook_id, task_id, attempts}` or `agentserve.webhook.deadlettered{webhook_id, task_id, reason}` via EmitBus so the eventlog records every delivery outcome.

## 6. Backwards compat

Sync mode is the default until rollout flips `STOKE_SERVE_ASYNC=1` (see §10). Behavior matrix:

| `STOKE_SERVE_ASYNC` | `X-Stoke-Sync` header | Response                                           |
|---------------------|-----------------------|----------------------------------------------------|
| unset               | unset                 | **Sync**: block until done, return final TaskState |
| unset               | `true`                | **Sync**: block until done, return final TaskState |
| `1`                 | unset                 | **Async**: enqueue, return 202 + queued TaskState  |
| `1`                 | `true`                | **Sync**: block until done, return final TaskState |

Sync path keeps the existing `runTask` function as-is (in-handler `Execute` call). Async path routes through the worker pool. Both paths share the same lifecycle-event emission so the eventlog sees identical event types regardless of mode. The CLI flag `--sync-mode` on `stoke serve` sets `X-Stoke-Sync: true` for every request — useful for deployments that want hard sync even if async is globally enabled.

## 7. Implementation checklist

1. [ ] Add `Config.WorkerCount int`, `Config.ShutdownDeadline time.Duration`, `Config.DeadLetterPath string`, `Config.EventLog *eventlog.Log`, `Config.Bus *bus.Bus` to `internal/agentserve/server.go`. Zero-values get sensible defaults in `NewServer`.
2. [ ] Create `internal/agentserve/pool.go` defining `type Pool struct { ... }`, `type taskJob struct { ... }`, `NewPool(cfg PoolConfig) *Pool`, `(*Pool).Submit(job *taskJob) error`, `(*Pool).Shutdown(ctx) error`.
3. [ ] Implement `NewPool` — spawns `WorkerCount` goroutines, each running a `for range queue { run(job) }` loop. Workers share a single root context cancelable on shutdown.
4. [ ] Implement `(*Pool).Submit` — non-blocking send on the buffered queue; returns `ErrQueueFull` if the channel is full.
5. [ ] Implement `(*Pool).Shutdown` — close queue, wait for in-flight workers up to ShutdownDeadline, then cancel root ctx; workers observe and exit. Wait group for clean goroutine termination.
6. [ ] Define `ErrQueueFull`, `ErrShutdownDeadline` exported errors in `pool.go`.
7. [ ] Update `(*Server).handleCreateTask`: determine sync vs async via `STOKE_SERVE_ASYNC` + `X-Stoke-Sync`; async path creates `*taskJob`, calls `s.pool.Submit`, writes 202 + queued state; sync path keeps calling `s.runTask` inline.
8. [ ] Extract the worker body into `(*Server).runJob(ctx, job *taskJob)` that mirrors `runTask` but updates state under `s.mu` + emits lifecycle events through `eventlog.EmitBus`.
9. [ ] Emit `agentserve.task.queued` on async enqueue, `agentserve.task.started` on worker pickup, `agentserve.task.completed` / `agentserve.task.failed` on terminal transition. All via `eventlog.EmitBus`. Event `Scope.TaskID = state.ID`.
10. [ ] Add `(*Server).handleCancelTask(w, r)` wired to `POST /api/task/{id}/cancel`. Looks up the task's per-job `context.CancelFunc`, invokes it, updates state to `cancelled`, emits `agentserve.task.cancelled`.
11. [ ] Track per-task `context.CancelFunc` in a new `s.cancels map[string]context.CancelFunc` cleared on terminal transition.
12. [ ] Add `(*Server).handleTaskEvents(w, r)` wired to `GET /api/task/{id}/events`. Sets SSE headers (`Content-Type: text/event-stream`, `Cache-Control: no-cache`, `Connection: keep-alive`). Subscribes to `bus.Pattern{TypePrefix:"", Scope: bus.Scope{TaskID: id}}`; for each event writes `data: <json>\n\n` + flushes (`http.Flusher`). Close when task terminal.
13. [ ] Implement `Last-Event-ID` replay: on SSE handler entry, if header present, call `eventlog.Log.ReadFrom(ctx, parseSeq(header))` filtered by `TaskID` and replay those before live subscription begins.
14. [ ] Create `internal/agentserve/webhook.go` with `type Webhook struct { ID, URL, Bearer, Secret string; Types []string }` and a registry `type WebhookRegistry struct { ... }`.
15. [ ] Implement `(*WebhookRegistry).Register(w Webhook) error` — mints ULID, validates URL via `validateWebhookURL` (see §8), persists to eventlog as `agentserve.webhook.registered`, starts a delivery subscription.
16. [ ] Implement `(*WebhookRegistry).Delete(id string) error` — emits `agentserve.webhook.deleted`, stops subscription.
17. [ ] Implement `(*WebhookRegistry).List() []Webhook` under read lock.
18. [ ] Implement `validateWebhookURL(u string) error` — parses URL, rejects non-HTTP(S), resolves host, blocks RFC1918 / loopback / link-local / IPv6 ULA addresses. Allowlist override via `STOKE_WEBHOOK_ALLOW_PRIVATE=1` for local dev only.
19. [ ] Implement `deliverWebhook(ctx, w Webhook, payload []byte) error` — POST, signs body with HMAC-SHA256(secret), adds `X-Stoke-Signature` + `X-Stoke-Delivery-ID`, 30 s timeout per attempt. Returns non-nil on non-2xx or transport error.
20. [ ] Implement retry loop: 5 attempts with backoff `1s, 5s, 30s, 2min, 10min`, jitter ±20 %. Cancellable via ctx. On final failure, call `deadletter.Append(entry)`.
21. [ ] Create `internal/agentserve/deadletter.go` with `Append(path string, entry DeadLetterEntry) error` — opens `O_CREATE|O_APPEND|O_WRONLY`, writes one JSON line + `\n`, syncs.
22. [ ] Add `(*Server).handleRegisterWebhook`, `handleListWebhooks`, `handleDeleteWebhook` wired to `/api/webhooks*` routes.
23. [ ] Update `(*Server).Handler` to register all new routes. Apply existing `withAuth` middleware to all task + webhook endpoints (capabilities stays public).
24. [ ] Persistence recovery: create `(*Server).recoverFromEventLog(ctx) error`. On NewServer, iterate `eventlog.ReadFrom(0)` filtered to `agentserve.*` types, fold into `s.tasks` + `s.webhooks.registry` in memory.
25. [ ] In recovery, any task last seen as `queued` or `running` gets requeued via `s.pool.Submit` with `retry_count++`. Cap at 3; past cap, mark `failed` + emit `agentserve.task.failed{error:"crash recovery retry cap exceeded"}`.
26. [ ] Emit `agentserve.task.recovered{task_id, retry_count}` when requeuing during recovery.
27. [ ] Add `Config.RetryCap int` (default 3) so tests can lower the cap for fast iteration.
28. [ ] Create `internal/agentserve/server_test.go` covering sync path: POST /api/task with fake executor, assert 200 + final state; POST with unknown task_type returns 400; POST without bearer when bearers configured returns 401.
29. [ ] Create `internal/agentserve/server_async_test.go` covering: `STOKE_SERVE_ASYNC=1` → POST returns 202 + queued state; GET /api/task/{id} polls through queued → running → completed; POST cancel on a running task transitions to cancelled and the worker observes ctx.Done.
30. [ ] Add `TestPool_ConcurrencyUnderRace` runnable with `-race` — 100 jobs submitted concurrently, all complete, no data race warnings.
31. [ ] Add `TestPool_QueueFull` — fill queue to capacity, assert `ErrQueueFull` and 503 response.
32. [ ] Add `TestPool_ShutdownWithInflightTasks` — submit N long-running jobs, call Shutdown with 100 ms deadline, assert `ErrShutdownDeadline` returned and root ctx is cancelled.
33. [ ] Add `TestSSE_EventStream` — subscribe to `/api/task/{id}/events`, trigger task lifecycle, assert SSE frames for queued/started/completed in order.
34. [ ] Add `TestSSE_LastEventIDReplay` — subscribe with `Last-Event-ID: 42`, assert events with sequence > 42 arrive (historical replay + live merge).
35. [ ] Add `TestWebhook_DeliverySuccess` using `httptest.NewServer` mock endpoint that returns 200; assert one POST with correct HMAC signature.
36. [ ] Add `TestWebhook_Retry` with mock that fails first 3 attempts then succeeds; assert exactly 4 POSTs, all with identical `X-Stoke-Delivery-ID`, and `agentserve.webhook.delivered{attempts:4}` event.
37. [ ] Add `TestWebhook_DeadLetter` with mock that always 500s; assert 5 POSTs, then one entry in `deadletter.jsonl` with correct shape.
38. [ ] Add `TestWebhook_ValidateURL_RejectsPrivate` — assert `validateWebhookURL("http://127.0.0.1/...")`, `http://10.0.0.1/...`, `http://169.254.1.1/...`, `http://[fd00::1]/...` all return errors.
39. [ ] Add `TestWebhook_ValidateURL_AllowsPrivateWithOverride` — with `STOKE_WEBHOOK_ALLOW_PRIVATE=1`, the four above return nil.
40. [ ] Add `TestPersistence_TaskRoundtrip` — submit task, read eventlog, verify `agentserve.task.queued/started/completed` events with matching `task_id`.
41. [ ] Add `TestPersistence_RecoveryRequeuesInflightTasks` — seed eventlog with `queued` + `started` events (no completion), start server, assert task is requeued and runs to completion; assert `agentserve.task.recovered{retry_count:1}` event.
42. [ ] Add `TestPersistence_RecoveryRespectsRetryCap` — seed eventlog with 3 prior `recovered` events for same task, start server, assert task is marked `failed` not requeued again.
43. [ ] Add `TestBackwardsCompat_XStokeSyncHeader` — with `STOKE_SERVE_ASYNC=1`, POST with `X-Stoke-Sync: true` returns 200 + final state (not 202).
44. [ ] Add `TestBackwardsCompat_DefaultIsSync` — with `STOKE_SERVE_ASYNC` unset, POST returns 200 + final state regardless of header.
45. [ ] Add `TestGracefulShutdown_DrainsInflight` — submit 5 slow jobs, call Shutdown with 10 s deadline, assert all 5 complete before shutdown returns.
46. [ ] Add `TestGracefulShutdown_HardCancelPastDeadline` — submit 1 infinite job, Shutdown with 50 ms deadline, assert `ErrShutdownDeadline` and job's ctx.Err() is ctx.Canceled.
47. [ ] Wire `Pool.Shutdown` into `cmd/r1/serve_cmd.go` (or existing serve command) on SIGINT/SIGTERM signal handler.
48. [ ] Document `STOKE_SERVE_ASYNC`, `STOKE_WEBHOOK_ALLOW_PRIVATE`, `Config.WorkerCount`, `Config.ShutdownDeadline`, `Config.DeadLetterPath` env + config surface in `internal/agentserve/server.go` package doc comment.
49. [ ] Run `gofmt -w ./internal/agentserve/` and `go vet ./...`; fix any reported issues.
50. [ ] Run `go build ./cmd/r1 && go test ./internal/agentserve/... -race && go vet ./...`; all must exit 0.

## 8. Security

- **Bearer auth** (existing). All task + webhook endpoints keep the `X-Stoke-Bearer` check from `server.go:withAuth`. Capabilities stays public. Reject unauth with `401`.
- **Webhook registration auth**: caller must pass a valid `X-Stoke-Bearer` to hit `/api/webhooks/register`. No anonymous registration — a hostile client cannot hijack a running server's outbound webhooks.
- **SSRF / DNS rebinding mitigation**: `validateWebhookURL` (checklist item 18) runs at registration AND at every delivery attempt. On registration we resolve the host and cache the IP to detect later changes; on delivery we re-resolve and reject if the resolved IP has moved into a blocked range. Blocked ranges: `127.0.0.0/8`, `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `169.254.0.0/16`, `::1/128`, `fc00::/7`, `fe80::/10`. Override with `STOKE_WEBHOOK_ALLOW_PRIVATE=1` for dev only; emit a loud warning to stderr when the override is active.
- **HMAC signing**: every webhook POST includes `X-Stoke-Signature: sha256=<hex>` over the raw body with the per-registration secret. Receivers MUST verify before trusting the payload. Secret never leaves the eventlog / memory; never logged.
- **Eventlog sensitivity**: webhook secrets live in the eventlog rows. Document in the deploy guide (not code) that `.stoke/events.db` must have `0600` permissions and be excluded from backups that cross a trust boundary. No code-level enforcement in this spec.
- **Rate limiting**: not in this spec. The existing worker-pool queue cap (1000) is the only backpressure mechanism; operators who need per-client limits add a reverse proxy upstream.

## 9. Testing

Already enumerated under §7 checklist items 28–46 — per-endpoint, concurrency under `-race`, webhook retry+deadletter+signing, persistence roundtrip, graceful shutdown with in-flight tasks, backwards compat. Coverage target: every new exported function has at least one test; every new endpoint has a happy path + one failure path.

Integration smoke (manual, documented in a test comment but not run in CI):

- Start `stoke serve --async`, POST a code task, observe the task runs to completion via `GET /api/task/{id}`.
- Subscribe to `/api/task/{id}/events` via `curl -N`, observe SSE frames in real time.
- Register a webhook pointing at `httpbin.org/post`, trigger a task, observe the POST with matching HMAC signature.
- SIGINT the server mid-task, restart, observe recovery via `agentserve.task.recovered` event + task completes on retry.

## 10. Rollout

Flag-gated via `STOKE_SERVE_ASYNC=1` environment variable. Default behavior remains sync so no existing caller breaks. Staged rollout:

1. **Week 0 (this spec lands)**: code ships with `STOKE_SERVE_ASYNC` defaulting to unset. The new endpoints (`/cancel`, `/events`, `/webhooks/*`) are live in both modes; only `POST /api/task`'s response shape toggles.
2. **Week 1–2 (staging)**: set `STOKE_SERVE_ASYNC=1` in staging. Monitor eventlog for `agentserve.webhook.deadlettered` + `agentserve.task.failed{error:"crash recovery retry cap exceeded"}` events. Success gate: zero dead-letter entries AND zero retry-cap failures across two weeks.
3. **Week 3 (production flip)**: set `STOKE_SERVE_ASYNC=1` in production. Keep the env var for one more release cycle so operators can roll back if needed.
4. **Week 6 (cleanup)**: make async the default; keep `X-Stoke-Sync: true` header as the sync opt-in. Remove the env var. Document in release notes.

Sync mode stays supported forever — some callers (dashboards that expect fast blocking responses, CI scripts that want a single exit code) will always prefer it. The code path is a strict subset of the async path post-rollout, so maintenance cost is low.

## 11. Acceptance criteria

Build + vet + test gates (same as every Stoke spec):

- `go build ./...` clean with and without `STOKE_SERVE_ASYNC` set.
- `go vet ./...` clean.
- `go test -race -count=1 ./internal/agentserve/... ./cmd/r1/...` green in both sync (default) and async (`STOKE_SERVE_ASYNC=1`) modes.

Behavioral acceptance:

- POST `/api/task` without `STOKE_SERVE_ASYNC` → blocks until executor returns, same as MVP; TaskState.Status is `completed` or `failed` in the response body.
- POST `/api/task` with `STOKE_SERVE_ASYNC=1` → returns 202 + TaskState with Status=`queued` within 50ms regardless of task duration.
- `X-Stoke-Sync: true` header overrides async mode per-request (proves the backwards-compat switch at the header layer).
- GET `/api/task/{id}/events` streams Server-Sent Events for a running async task; `Last-Event-ID` resumes from the correct sequence after a simulated disconnect.
- POST `/api/task/{id}/cancel` on a running task causes Execute's context to cancel within 1s; TaskState.Status transitions to `cancelled`.
- POST `/api/webhooks/register` rejects RFC1918 / loopback / link-local URLs unless `STOKE_SERVE_WEBHOOK_ALLOW_PRIVATE=1` is set.
- Webhook delivery: 5 retries with exponential backoff, then dead-letter to `internal/agentserve/deadletter.jsonl`. HMAC-SHA256 signature matches the registered secret.
- Graceful shutdown: SIGINT stops accepting new tasks, waits up to `--shutdown-deadline` for in-flight tasks, hard-cancels the rest. No goroutine leaks under `-race`.
- Crash recovery: kill the server mid-task, restart, observe the queued / running task resume (up to 3 retries) with an `agentserve.task.recovered` event emitted.
