# R08-python — File indexer CLI with SQLite

CLI that walks a directory, indexes file metadata to SQLite, emits
a JSON summary. Exercises concurrent.futures + sqlite3.

## Scope

Package `indexer` in `src/indexer/`.

Entry: `indexer.cli:main` mapped via `[project.scripts] indexer = "indexer.cli:main"`.

Flags (argparse):
- `--root <path>` (required)
- `--db <file.sqlite>` (required)
- `--workers N` (default os.cpu_count())

Behavior:
1. Walk root via `os.walk` (or `pathlib.Path.rglob("*")`).
2. Use `concurrent.futures.ThreadPoolExecutor(max_workers=workers)`
   to compute SHA256 + size + mtime per file.
3. INSERT OR REPLACE into `files(path TEXT PRIMARY KEY, size INTEGER,
   sha256 TEXT, mtime INTEGER)`.
4. Print JSON `{"total": N, "total_bytes": B, "unique_sha256": U}`.

## Acceptance

- `pyproject.toml` declares script entrypoint + pytest.
- `tests/test_smoke.py` creates temp dir with ~10 known files, runs
  the script via subprocess, reads SQLite, asserts total + one
  deterministic sha256.
- `pip install -e ".[test]"` + `pytest -q` exit 0.

## What NOT to do

- No third-party SQL library (sqlite3 stdlib only).
- No HTTP server.
- No daemon mode.
