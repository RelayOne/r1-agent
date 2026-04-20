# R06-python — Notification preferences API (FastAPI)

Single-service FastAPI with `GET/PUT /prefs/{user_id}` backed by an
in-memory store and Pydantic validation.

## Scope

Package `notify_api` in `src/notify_api/`.

Models via Pydantic v2:
```python
class QuietHours(BaseModel):
    start: str  # HH:MM
    end: str
class NotificationPrefs(BaseModel):
    email: bool
    sms: bool
    push: bool
    digest: Literal["off", "daily", "weekly"]
    quiet_hours: QuietHours | None = None
```

Endpoints:
- `GET /prefs/{user_id}` → 200 with prefs JSON, or 404 if unknown.
- `PUT /prefs/{user_id}` → 200 with echoed prefs. 422 on invalid.

Validation: digest is the Literal (FastAPI/Pydantic enforces).
QuietHours start/end must match `^\d{2}:\d{2}$` (custom validator).

In-memory store: `dict[str, NotificationPrefs]` protected by a
threading.Lock or asyncio.Lock.

Tests in `tests/test_prefs.py` using `fastapi.testclient.TestClient`:
- GET unknown user → 404
- PUT valid prefs → 200, echoed prefs
- PUT invalid digest → 422
- GET after PUT → returns stored prefs

## Acceptance

- `pyproject.toml` with fastapi, pydantic>=2, httpx (test), pytest (test)
- `pip install -e ".[test]"` + `pytest -q` exit 0
- At least 4 tests pass

## What NOT to do

- No database.
- No auth.
- No listing endpoint.
