# R07-python — Async worker pool with asyncio queue

Service that spawns N asyncio worker tasks communicating via
asyncio.Queue. Exercises concurrency, graceful shutdown.

## Scope

Package `workerpool_demo` in `src/workerpool_demo/`.

N workers (env `WORKERS`, default 4) consume `Job` dataclasses from
an `asyncio.Queue`, compute `job.value ** 2`, push results to a
result queue. Shutdown signal flushes queue and awaits workers.

HTTP via FastAPI:
- `POST /jobs` body `{"value": int}` → 202
- `GET /results` → JSON array of `{value, squared}`
- `POST /shutdown` → blocks until workers drain + exit, returns 200

## Acceptance

- `pyproject.toml` includes fastapi, httpx (test), pytest, pytest-asyncio
- At least 3 tests pass (submit 50, verify squared counts; shutdown
  blocks; invalid body → 422)
- `pytest -q` exits 0

## What NOT to do

- No persistence.
- No retries.
- No threading.
