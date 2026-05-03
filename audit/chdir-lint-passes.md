# `chdir-lint` audit log — r1d-server Phase A

This file records the disposition of every unannotated `os.Chdir` / `os.Getwd` /
`filepath.Abs("")` / `os.Open("./...")` hit reported by `tools/lint-no-chdir.sh`
during the Phase A audit gate (specs/r1d-server.md §10, items 5–9).

Format per pass:

- **Packages covered:** the directory glob this pass owns.
- **Hits found:** total reported by the lint over those packages before this pass landed.
- **Disposition:** per-line summary — annotated or refactored.
- **Hits remaining:** must be 0 by the time the pass commit lands.

The repo-wide running total appears at the bottom and ticks down to 0 by TASK-9 / TASK-10.

## Pass 1 — `engine/`, `agentloop/`, `tools/`, `bash/` (n/a), `patchapply/`

- **Hits found:** 0
- **Disposition:** none required.
- **Hits remaining:** 0
- Note: `internal/bash/` does not exist in this tree — bash execution lives inside `internal/tools/` (the cascading-replace tool surface). The five packages owned by this pass are otherwise all clean.

## Pass 2 — `worktree/`, `verify/`, `baseline/`, `gitblame/`, `git*/`

- **Hits found:** 1
- **Disposition:**
  - `internal/verify/gates_yaml_test.go:252` `os.Getwd` — test helper that walks up from the test cwd to find `go.mod`. Legitimate test-only use: runs inside `go test`, never inside a session goroutine. Annotated `// LINT-ALLOW chdir-test: ...`.
- **Refactors:** none
- **Hits remaining:** 0
- Note: `internal/worktree/`, `internal/baseline/`, `internal/gitblame/`, and the other `git*` packages are all clean.

## Pass 3 — `goast/`, `repomap/`, `symindex/`, `chunker/`, `tfidf/`, `vecindex/`

- **Hits found:** 0
- **Disposition:** none required.
- **Hits remaining:** 0
- Note: these analysis packages already accept `repoRoot string` (or equivalent file paths) at every public entry point — no implicit cwd reads. Verified by grep over `os.Chdir` / `os.Getwd` / `filepath.Abs("")` / `os.Open("./...")`.

## Pass 4 — `memory/`, `wisdom/`, `research/`, `replay/`, `lsp/`, `mcp/`

- **Hits found:** 3
- **Disposition:**
  - `internal/lsp/lsp.go:87` `os.Getwd` — **refactored**. The previous code fell back to `os.Getwd()` when the LSP server was constructed with `s.root == ""`. That fallback is exactly the cwd-leak surface the audit exists to eliminate (two concurrent sessions racing on the process-wide cwd inside the multi-session daemon). The `cmd/r1/lsp_cmd.go` caller already resolves a root before constructing the server, so the in-server fallback is dead defensive code; replaced with a log-and-bail.
  - `internal/memory/scope.go:207` `os.Getwd` — **annotated** `// LINT-ALLOW chdir-fallback`. Documented step-3 fallback in the `RepoHashAt(ctx, dir)` ladder; multi-session callers can avoid it by passing a non-empty `dir`. The function-level doc comment already states this.
  - `internal/memory/reconciler.go:90` `os.Getwd` — **refactored + annotated**. Added a `RepoRoot string` field on `Reconciler`; `Reconcile()` prefers it when set and falls back to the existing `reconcilerRepoRoot` hook only when empty. The hook itself keeps `os.Getwd()` as the legacy single-process default and is annotated `// LINT-ALLOW chdir-fallback`. Multi-session callers MUST set `RepoRoot` (documented on the struct).
- **Refactors:** 2 (`lsp/lsp.go`, `memory/reconciler.go`)
- **Annotations:** 2 (`memory/scope.go`, `memory/reconciler.go` fallback hook)
- **Hits remaining:** 0
- Note: `internal/wisdom/`, `internal/research/`, `internal/replay/`, `internal/mcp/` are all clean.

## Pass 5 — remaining packages

(filled in when the pass-5 commit lands)

## Running totals

| pass | hits at start | annotated | refactored | hits at end |
| ---- | ------------- | --------- | ---------- | ----------- |
| 1    | 0             | 0         | 0          | 0           |
| 2    | 1             | 1         | 0          | 0           |
| 3    | 0             | 0         | 0          | 0           |
| 4    | 3             | 2         | 2          | 0           |
| 5    | tbd           | tbd       | tbd        | tbd         |
