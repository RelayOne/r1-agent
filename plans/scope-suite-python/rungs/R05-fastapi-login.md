# R05-python — FastAPI login endpoint with JWT

Build a small FastAPI service with `POST /login` that validates
credentials against an in-memory user store and returns a JWT. Tests
exercise FastAPI's TestClient.

## Scope

Package `auth_api` in `src/auth_api/`. Use FastAPI + Pydantic v2.

Endpoint `POST /login`:
- Body `{ "email": str, "password": str }`.
- 200 `{ "token": "<JWT>" }` on valid creds.
- 400 on validation errors.
- 401 `{ "detail": "invalid credentials" }` on wrong creds.

JWT: use `PyJWT` (`jwt.encode`) with HS256; secret from env
`AUTH_SECRET`; payload `{ "email": <email>, "iat": <int> }`.

In-memory users: `USERS = {"alice@example.com": "password123"}`.

Tests in `tests/test_login.py` using `fastapi.testclient.TestClient`:
- valid creds → 200, token parses with same secret
- unknown email → 401
- missing password → 400

## Acceptance

- `pyproject.toml` with `fastapi`, `pydantic>=2`, `pyjwt` in deps;
  `pytest`, `httpx` in dev/test deps.
- `pip install -e ".[test]"` succeeds in a fresh venv.
- `pytest -q` exits 0; three tests passing.

## What NOT to do

- No database. In-memory only.
- No password hashing.
- No middleware beyond FastAPI defaults.
- No `uvicorn` in tests — TestClient is sufficient.
