# Task 03 — Multi-file search (grep + glob)

**Ability under test:** Grep + Glob tools (rows #5, #6 in parity matrix)  
**Reference products:** Claude Code (`tools.Grep`, `tools.Glob`)  
**R1 equivalent:** `internal/tools/tools.go` `grep`, `glob`

## Task description

In the Stoke repo at `/home/eric/repos/stoke/`, perform:

1. **Glob** — find all `*.go` files under `internal/ledger/` (not test files).
   Expected: 5+ files.

2. **Grep content** — search for the string `NodeTyper` across all `.go`
   files in `internal/ledger/`. Report files + line numbers.
   Expected: at least 1 match.

3. **Grep files-only mode** — find all `.go` files in `internal/` that
   import `"sync"`.
   Expected: 50+ files.

4. **Grep count mode** — count occurrences of `ErrIncompleteManifest`
   across all `.go` files in `internal/skillmfr/`.
   Expected: count > 3.

5. **Combined** — use Glob to find all `manifest.go` files under
   `internal/`, then Grep each for the string `Validate`.
   Report: list of paths + first matching line per file.

## Acceptance criteria

- [ ] Glob returns >= 5 files from `internal/ledger/`
- [ ] Grep content finds `NodeTyper` with file+line
- [ ] Grep files-only returns >= 50 files importing "sync"
- [ ] Grep count for `ErrIncompleteManifest` > 3
- [ ] Combined glob+grep works end-to-end

## Evaluation scoring

- PASS: all 5 ACs met
- PARTIAL: grep or glob works but not both
- FAIL: neither tool produces output
