# R08-rust — parallel file indexer CLI

A CLI that walks a directory tree in parallel, indexes file metadata
into SQLite, and emits a JSON report. Exercises `rayon`, `walkdir`,
`sqlx`, and blocking-bridge patterns inside tokio.

## Scope

Binary crate `indexer`. Usage: `indexer --root <path> --db <db.sqlite>`.

Behavior:
1. Walk `--root` with rayon's `par_bridge` over `walkdir::WalkDir`.
2. For each file, compute SHA256, size, and modified-time.
3. Insert rows into a SQLite table `files(path TEXT PK, size INTEGER,
   sha256 TEXT, mtime INTEGER)`. Use `INSERT OR REPLACE`.
4. After walk completes, print a JSON object to stdout:
   `{ "total": N, "total_bytes": B, "unique_sha256": U }`.
5. Exit 0.

Migration ships in the binary via `sqlx::migrate!` from `migrations/`.

## Acceptance

- `Cargo.toml`: clap (derive), walkdir, rayon, sha2, hex, sqlx (0.7+
  with rustls-tls, sqlite, runtime-tokio), tokio full, serde_json.
- Integration test in `tests/smoke.rs` creates a tempdir with ~10
  known files, runs the binary, reads back the SQLite table, and
  asserts `total=10`, correct bytes, correct sha256 for one
  deterministic file.
- `cargo build --release` + `cargo test` exit 0.
- `cargo clippy -- -D warnings` exits 0.
- No unwraps in user-facing paths. Use `anyhow::Result<()>` from main.

## What NOT to do

- No long-running daemon mode. Single pass + exit.
- No remote database, no network I/O.
- No HTTP server.
