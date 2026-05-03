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

(filled in when the pass-2 commit lands)

## Pass 3 — `goast/`, `repomap/`, `symindex/`, `chunker/`, `tfidf/`, `vecindex/`

(filled in when the pass-3 commit lands)

## Pass 4 — `memory/`, `wisdom/`, `research/`, `replay/`, `lsp/`, `mcp/`

(filled in when the pass-4 commit lands)

## Pass 5 — remaining packages

(filled in when the pass-5 commit lands)

## Running totals

| pass | hits at start | annotated | refactored | hits at end |
| ---- | ------------- | --------- | ---------- | ----------- |
| 1    | 0             | 0         | 0          | 0           |
| 2    | tbd           | tbd       | tbd        | tbd         |
| 3    | tbd           | tbd       | tbd        | tbd         |
| 4    | tbd           | tbd       | tbd        | tbd         |
| 5    | tbd           | tbd       | tbd        | tbd         |
