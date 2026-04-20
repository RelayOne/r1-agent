# R09-rust — admin auth service slice (multi-crate + database)

Cargo workspace building the user-admin slice of an auth service.
Exercises workspace + DB + HTTP + typed errors + integration tests
in one coherent scope. Single-purpose "slice" of a larger system.

## Scope

Workspace with three crates:

1. `auth-domain` — pure types: `User { id, email, role }`,
   `Role` enum, validation helpers. No runtime deps beyond serde.
2. `auth-db` — SQLite persistence: `UserRepo` trait with async
   `create_user`, `get_user_by_email`, `list_users`, `delete_user`
   by id. Implementation via sqlx. Migrations in
   `auth-db/migrations/0001_users.sql` create the `users` table.
3. `auth-admin` (binary) — Axum server exposing:
   - `POST /admin/users { email, role }` → 201 with created user.
     401 if `X-Admin-Token` header missing/wrong.
   - `GET /admin/users` → 200 with JSON array of all users.
   - `DELETE /admin/users/:id` → 204 on success.
   - 409 on duplicate email.
   - 404 on unknown id for DELETE.

Admin token comes from env `ADMIN_TOKEN`; tests set it at spawn time.

## Acceptance

- `cargo build --workspace` exits 0.
- `cargo test --workspace` exits 0 — at least one e2e test per
  endpoint using a temp SQLite file and ephemeral port.
- `cargo clippy --workspace -- -D warnings` exits 0.
- `auth-db` compiles independently without depending on `auth-admin`.
- `auth-domain` has zero non-std runtime deps (serde is fine).

## What NOT to do

- No JWT / session tokens. Static admin header only.
- No rate limiting, no CORS.
- No migrations beyond 0001_users.sql.
