# R08-go — Parallel file indexer with SQLite

CLI that walks a directory tree concurrently, indexes metadata to
SQLite, emits a JSON summary. Exercises goroutines, `filepath.Walk`,
`crypto/sha256`, and `database/sql` with `mattn/go-sqlite3`.

## Scope

Module `github.com/example/indexer`. Binary `cmd/indexer/main.go`.

Flags:
- `--root <path>` (required)
- `--db <file.sqlite>` (required)
- `--workers N` (default runtime.NumCPU())

Behavior:
1. Walk `--root` with `filepath.Walk` emitting paths to a channel.
2. N worker goroutines pull paths, compute SHA256 + size + mtime,
   INSERT OR REPLACE into `files(path TEXT PRIMARY KEY, size INTEGER,
   sha256 TEXT, mtime INTEGER)`.
3. After workers drain, print JSON `{"total": N, "total_bytes": B,
   "unique_sha256": U}` and exit 0.

Schema created at startup if missing.

## Acceptance

- `go.mod` lists `github.com/mattn/go-sqlite3`.
- `tests/smoke_test.go` creates a tempdir with known files, runs the
  binary via `exec.Command`, reads the SQLite table via `sql.Open`,
  and asserts total + unique_sha256 + a deterministic file's hash.
- `go build` + `go test` + `go vet` all exit 0.

## What NOT to do

- No daemon mode. Single-pass exit.
- No HTTP server.
- No flag library beyond `flag`.
