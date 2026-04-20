# R10-python — Multi-worker jobs service with SQLite

Heaviest python rung: production-shaped async jobs service with
SQLite persistence, N workers, graceful shutdown.

## Scope

Package `jobs_service` in `src/jobs_service/`:

- `jobs_service/jobs.py` — `JobKind = Literal["echo","sleep"]`,
  `JobStatus = Literal["queued","running","done","failed"]`,
  `Job` dataclass, `Repo` protocol.
- `jobs_service/store.py` — SQLite impl. Schema:
  `jobs(id INTEGER PRIMARY KEY AUTOINCREMENT, kind TEXT, payload TEXT,
  status TEXT CHECK(status IN ('queued','running','done','failed')),
  result TEXT, error TEXT, created_at INTEGER NOT NULL,
  started_at INTEGER, finished_at INTEGER)`.
- `jobs_service/api.py` — FastAPI:
  - `POST /jobs {kind, payload}` → 202 `{id}`
  - `GET /jobs/{id}` → 200 job JSON | 404
  - `GET /healthz` → 200
  - Spawns N asyncio worker tasks (env `WORKERS`, default 4).
    Workers atomically claim `queued` jobs via UPDATE…RETURNING,
    execute (echo = echo payload, sleep = `asyncio.sleep(ms/1000)`
    then echo), mark done/failed.
  - SIGTERM handler: stop accepting new jobs, drain 5s, exit.

## Acceptance

- `pyproject.toml` with fastapi, httpx, pytest, pytest-asyncio.
- `pip install -e ".[test]"` + `pytest -q` exit 0.
- Tests (at least 4):
  1. Submit 20 Echo jobs, poll → all `done`, results match payloads.
  2. Submit 5 `sleep` with payload `ms=200` + WORKERS=4, assert
     wall-clock < 500ms.
  3. Invalid JSON → 422.
  4. Shutdown test: submit 5 sleep-100s, SIGTERM after 50ms, assert
     exit <5s and all started jobs reach terminal status.

## What NOT to do

- No external queue.
- No auth.
- No retries.
- No long-polling.
