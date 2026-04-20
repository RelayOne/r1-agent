# R10-rust — Multi-worker jobs service with SQLite persistence

Biggest rung. Build a production-shaped async jobs service: submit
work, poll status, receive results, persist everything in SQLite, run
N worker tasks concurrently. Exercises workspaces, multi-module code
organization, SQL migrations, async worker pools, cancellation, and
full end-to-end integration testing.

## Scope

Cargo workspace with three crates:

1. `jobs-core` (library) — public types: `JobKind`, `JobStatus`,
   `Job`, and a `JobRepo` trait abstracting storage.
2. `jobs-storage` (library) — implements `JobRepo` over SQLite
   using `sqlx` (0.7+) with a compiled-in `migrations/` directory.
   Migration `0001_init.sql` creates a `jobs` table with columns
   `id INTEGER PK`, `kind TEXT NOT NULL`, `payload TEXT NOT NULL`,
   `status TEXT NOT NULL CHECK(status IN ('queued','running','done','failed'))`,
   `result TEXT`, `error TEXT`, `created_at INTEGER NOT NULL`,
   `started_at INTEGER`, `finished_at INTEGER`.
3. `jobs-service` (binary) — Axum HTTP server:
   - `POST /jobs` submits a job with `{ kind, payload }`, returns
     `{ id }` with 202.
   - `GET /jobs/:id` returns the current row as JSON.
   - On startup, spawns N worker tasks (N from env `WORKERS`,
     default 4). Workers poll the queue, pick up `status='queued'`
     jobs atomically (UPDATE … WHERE status='queued' LIMIT 1 …
     RETURNING * semantics), set `status='running'`, do the work,
     set `status='done'` with result or `'failed'` with error.
   - Supported `JobKind::Echo` (trivial echo of payload) and
     `JobKind::Sleep { ms }` (sleep then echo) — enough to test
     concurrency and timing.
   - Graceful shutdown on SIGINT: stop accepting new jobs, let
     in-flight workers drain for up to 5s, then exit.

Integration tests in `jobs-service/tests/` or a top-level
`tests/e2e.rs`:
- Submit 20 Echo jobs rapidly, poll until all `done`, assert
  correct results.
- Submit 5 Sleep{ms=200} jobs with WORKERS=4, assert they complete
  in < 500ms wall-clock (proves concurrent execution).
- Submit an invalid payload (unparseable JSON) → 400.
- Shutdown test: spawn server, submit 5 Sleep{ms=100} jobs, send
  SIGTERM after 50ms, verify server exits within 5s and any
  started workers marked their jobs as completed (no lost work).

## Acceptance

- Workspace `Cargo.toml` at repo root lists the three members.
- Each member compiles independently.
- `cargo build --workspace` exits 0.
- `cargo test --workspace` exits 0; every case passes.
- `cargo clippy --workspace -- -D warnings` exits 0.
- `sqlx::migrate!()` runs the 0001 migration on startup.
- Shutdown is clean (no zombie threads, no SQLite lock errors).

## What NOT to do

- No external job queue (Redis, SQS). SQLite is the queue.
- No auth, no rate limiting.
- No metric endpoints beyond `/healthz` returning 200.
- No retries — failed jobs stay `failed`.
- No long-polling or server-sent events; clients poll `GET /jobs/:id`.
