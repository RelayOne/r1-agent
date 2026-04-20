# R09-python — Auth admin slice (multi-module + SQLite + FastAPI)

Multi-module FastAPI slice: domain types, SQLite repo, admin
endpoints with an admin-token header gate.

## Scope

Package `auth_admin` in `src/auth_admin/` with submodules:

- `auth_admin/domain.py` — pure dataclasses/Pydantic: `User { id,
  email, role: "admin"|"member" }`, validation helpers.
- `auth_admin/db.py` — SQLite via stdlib sqlite3. `UserRepo` class
  with `create_user`, `get_user_by_email`, `list_users`,
  `delete_user`. Schema initialized on `UserRepo.__init__`.
- `auth_admin/api.py` — FastAPI app:
  - `POST /admin/users {email, role}` → 201 user, 401 without
    `X-Admin-Token: <env ADMIN_TOKEN>`
  - `GET /admin/users` → 200 list
  - `DELETE /admin/users/{id}` → 204 | 404
  - 409 on duplicate email

## Acceptance

- `pyproject.toml` with fastapi, pydantic>=2, httpx (test), pytest (test).
- `pip install -e ".[test]"` + `pytest -q` exit 0.
- At least 4 tests covering: unauthorized POST, happy POST, list,
  duplicate-409, delete.

## What NOT to do

- No JWT / sessions.
- No migrations beyond inline schema init.
- No rate-limiting / CORS.
