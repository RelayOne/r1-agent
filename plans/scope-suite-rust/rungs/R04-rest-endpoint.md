# R04-rust — single REST endpoint

A single-endpoint Axum service returning JSON. Exercises async,
Axum routing, serde, and HTTP integration testing without state.

## Scope

Binary crate `hello-api`. Server on an ephemeral port
(tests inject, main uses 3000). One endpoint:

- `GET /hello/:name` returns 200 with JSON `{ "greeting": "Hello, <name>!" }`.

Integration test uses `reqwest` (or `axum::body::Body` +
`tower::ServiceExt::oneshot`) and asserts both the status code and
the deserialized JSON body.

## Acceptance

- `Cargo.toml` has `axum`, `tokio` (feature `full`), `serde`,
  `serde_json`; dev: `reqwest` (feature `json`) or `tower`.
- `src/main.rs` (and/or `src/lib.rs`) implement the route.
- `cargo build` + `cargo test` exit 0.
- Test asserts `response.greeting == "Hello, World!"` when calling
  `/hello/World`.

## What NOT to do

- No database, no state, no middleware.
- No more routes.
- No CORS or auth.
