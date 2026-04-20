# R09-go — Admin slice (multi-package + SQLite + chi)

Coherent "slice" of an admin auth service with proper package
boundaries + DB persistence + HTTP + e2e tests. Exercises workspace-
style layout inside a single module.

## Scope

Module `github.com/example/auth-admin`. Packages:

- `internal/domain/` — pure types: `User { ID, Email, Role }`,
  `Role` (enum/string consts), validation helpers. No runtime deps
  beyond stdlib.
- `internal/db/` — SQLite `UserRepo` with methods
  `CreateUser`, `GetUserByEmail`, `ListUsers`, `DeleteUser`. Uses
  `database/sql` + `mattn/go-sqlite3`. Migration applied on
  `NewRepo` creates the `users` table.
- `cmd/authsvc/` — chi router with:
  - `POST /admin/users` body `{email, role}` → 201 user JSON;
    401 without `X-Admin-Token` matching env.
  - `GET /admin/users` → 200 list.
  - `DELETE /admin/users/:id` → 204 or 404.
  - 409 on duplicate email.

## Acceptance

- `go build ./...` + `go test ./...` + `go vet ./...` all exit 0.
- At least one e2e test per endpoint using `httptest.NewServer` +
  temp SQLite file.
- `internal/domain` has zero non-stdlib runtime deps.

## What NOT to do

- No JWT / sessions. Static admin token only.
- No rate-limiting, no CORS.
- No migrations beyond the initial `users` table.
