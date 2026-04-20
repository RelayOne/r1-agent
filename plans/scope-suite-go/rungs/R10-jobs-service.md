# R10-go — Multi-worker jobs service with SQLite

Heaviest Go rung. Production-shaped async jobs service: submit work,
poll status, workers drain queue, persist everything in SQLite, full
e2e with graceful shutdown.

## Scope

Module `github.com/example/jobs-service` with layered packages:

- `internal/jobs/` — `JobKind` enum (`Echo | Sleep{MS int}`),
  `JobStatus` (`queued|running|done|failed`), `Job` struct,
  `Repo` interface.
- `internal/store/` — SQLite impl of `Repo` using `database/sql`.
  Migration creates `jobs(id INTEGER PK, kind TEXT, payload TEXT,
  status TEXT CHECK(status IN ('queued','running','done','failed')),
  result TEXT, error TEXT, created_at INTEGER NOT NULL,
  started_at INTEGER, finished_at INTEGER)`.
- `cmd/jobs-service/` — chi server:
  - `POST /jobs` body `{kind, payload}` → 202 `{id}`
  - `GET /jobs/:id` → 200 full job JSON or 404
  - `GET /healthz` → 200
  - Spawns N workers (env `WORKERS`, default 4). Each worker polls
    for `queued` jobs via atomic UPDATE…RETURNING, executes by kind
    (Echo = echo payload; Sleep{MS} = sleep MS ms then echo), marks
    `done` or `failed`.
  - SIGINT/SIGTERM → stop accepting new jobs, workers drain for up
    to 5s, exit.

## Acceptance

- `go build ./...` + `go test ./...` + `go vet ./...` all exit 0.
- `tests/e2e_test.go` covers:
  1. Submit 20 Echo jobs rapidly, poll until all `done`, assert
     results match payload.
  2. Submit 5 `Sleep{MS:200}` with WORKERS=4; assert wall-clock
     < 500ms (proves concurrent execution).
  3. Submit invalid JSON → 400.
  4. Submit 5 `Sleep{MS:100}`, send SIGTERM after 50ms, assert
     exit <5s and every started job in terminal state.
- Shutdown is clean (no goroutine leak, no SQLite lock errors).

## What NOT to do

- No external queue (Redis/SQS).
- No auth, no rate-limiting.
- No retries on failed jobs.
- No long-polling/SSE; clients poll `GET /jobs/:id`.
