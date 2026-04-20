# R05-go — chi router login endpoint

Small Go HTTP service using `chi` with `POST /login` that returns an
HMAC token on valid credentials. Exercises handlers, context,
httptest, and module layout.

## Scope

Module `github.com/example/authsvc`. Directories:
- `cmd/authsvc/main.go` — wires chi router and starts a server on
  `:8080` (or an `httptest.NewServer` in tests).
- `internal/auth/handler.go` — `POST /login` handler:
  - body `{ "email": str, "password": str }`
  - 200 `{ "token": "<hex-hmac-sha256>" }` on valid
  - 401 on invalid creds, 400 on malformed JSON
  - HMAC key from env `AUTH_SECRET`, input `email|unix-millis`
- `internal/auth/users.go` — in-memory map with
  `alice@example.com → "password123"`.

Tests in `internal/auth/handler_test.go` using `httptest`:
- happy: `alice@example.com`/`password123` → 200 with non-empty token
- unknown email → 401
- malformed JSON body → 400

## Acceptance

- `go.mod` lists `github.com/go-chi/chi/v5`.
- `go build ./...` exits 0.
- `go test ./...` exits 0; all three cases pass.
- `go vet ./...` exits 0.

## What NOT to do

- No database. In-memory only.
- No sessions / cookies. Token in response body.
- No middleware beyond chi defaults.
- No password hashing.
