# R05-rust — Axum login endpoint with signed token

Build a small Axum HTTP service with a `POST /login` endpoint that
validates credentials against an in-memory user store and returns a
signed HMAC token. Exercises async, serde, state, and integration
testing.

## Scope

Binary crate `auth-service` using Axum 0.7+ and tokio. Server listens
on a TCP port chosen at runtime (tests pass the port in).

Endpoint `POST /login`:
- Request JSON: `{ "email": "…", "password": "…" }`.
- On valid credentials: 200 with `{ "token": "<hmac-sha256>" }` where
  the HMAC signs `"<email>:<unix-millis>"` with a key passed via env
  var `AUTH_SECRET`.
- On malformed JSON / missing fields: 400 with
  `{ "error": "invalid request" }`.
- On wrong password or unknown email: 401 with
  `{ "error": "invalid credentials" }`.

In-memory users: a fixed map `HashMap<String, String>` loaded at
startup containing at minimum `alice@example.com → "password123"`.

Integration tests in `tests/login.rs` spin up the server on an
ephemeral port via `tokio::net::TcpListener::bind("127.0.0.1:0")` and
issue requests using `reqwest::Client`. At least three cases:
- valid creds → 200 + token present
- unknown email → 401
- missing password field → 400

## Acceptance

- `Cargo.toml` declares `axum`, `tokio` (features `full`),
  `serde`, `serde_json`, `sha2`, `hmac`, `hex`, and in
  `[dev-dependencies]`: `reqwest` (features `json`), `tokio`.
- `src/main.rs` / `src/lib.rs` builds and implements the behavior.
- `tests/login.rs` passes all three cases.
- `cargo build` + `cargo test` each exit 0.
- `cargo clippy -- -D warnings` exits 0 (keep it clean).

## What NOT to do

- No real database. In-memory only.
- No password hashing beyond the stored plaintext check — this is a
  scope test, not security.
- No CORS / middleware beyond the default Axum router.
- No frontend. Server-only.
