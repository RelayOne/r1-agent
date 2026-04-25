# Expected Output — Task 03 (Multi-file search)

## Pass criteria (pinned assertions)

### Glob: internal/ledger/*.go (non-test)
- Count >= 5 files
- All paths under `internal/ledger/`
- No `_test.go` files in result

### Grep content: NodeTyper
- >= 1 match
- Each match includes file path + line number
- At minimum: `internal/ledger/nodes/*.go` should have NodeTyper references

### Grep files-only: import "sync"
- >= 50 files
- All paths under `internal/`
- Output is file paths, not content lines

### Grep count: ErrIncompleteManifest in internal/skillmfr/
- Count > 3
- Single integer or per-file counts summing to > 3

### Combined glob+grep: manifest.go files with Validate
- >= 1 manifest.go found
- Each found file has at least 1 Validate match
- Expected files: `internal/skillmfr/manifest.go`, `internal/skill/manifest.go`

## Allowed variance

- File counts may grow; floors are invariants.

## Failure indicators

- Glob returns 0 files
- Grep returns no matches for known symbols
- sync import count < 50 (the repo is Go-heavy, sync is ubiquitous)
