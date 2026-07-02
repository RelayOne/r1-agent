# Proposed CLAUDE.md edits — audit/complete-systems-2026-07-01 (PR #328)

CLAUDE.md is protected by `.claude/hooks/protect-hooks.sh` ("human-controlled;
propose changes via AskUserQuestion"), and the fix-wave agents correctly
refused to bypass it. These are the verified corrections awaiting approval
(audit IDs A113, A080, plus deletion follow-ups from A063/A064/A102/A103/A104/A107/A109).

All figures measured at branch HEAD after the dead-package deletions:
`go list ./internal/... | wc -l` = 259, `ls internal | wc -l` = 180,
`ls cmd | wc -l` = 11, `go list ./bench/... | wc -l` = 11,
dispatch-switch `case` arms in cmd/r1/main.go = 71,
node types: `grep -r "func.*NodeType()" internal/ledger/nodes/` = 52 (current row is correct).

## 1. Header (A113)
- OLD: `## Package map (183 internal + 1 cmd + 10 bench)`
- NEW: `## Package map (180 internal dirs / 259 Go packages + 11 cmd binaries + 11 bench packages)`

## 2. cmd line (A113)
- OLD: `cmd/r1/main.go                    20 commands. --roi, --sqlite, --interactive, --specexec flags.`
- NEW: `cmd/r1/main.go                    71 subcommands (see r1 --help). --roi, --sqlite, --interactive, --specexec flags. 10 further cmd/ binaries: r1-bench, r1-server, r1-mcp, r1-gateway, r1-a2a, r1-acp, r1-skill-compile, chat-probe, critique-compare, heroa-e2e.`

## 3. Delete rows for packages removed on this branch
- `contentid/` (deleted — A102: zero importers; its taxonomy rejected every real ledger ID)
- `dispatch/` (deleted — A103: never constructed; Process() never called)
- `harness/models/` (deleted — A104: zero importers)
- `harness/stances/` (deleted — A063/A064: dormant duplicate of harness/prompts)

## 4. Key design decision 15 (A080)
- OLD: `model.Resolve() walks Primary -> FallbackChain (Claude -> Codex -> OpenRouter -> API -> lint-only)`
- NEW: `model.Resolve() walks Primary -> FallbackChain. Wired execution runners today: Claude -> Codex (Native); OpenRouter/DirectAPI/Ember/lint-only are router-defined but isAvailable() returns false for them (not yet wired as workflow runners).`

## 5. Optional: the A113 finder also verified the map body lists only ~106 of 180
top-level internal dirs; adding the missing rows is worthwhile but larger —
approve separately if wanted.

Apply with a single `docs(claude-md)` commit after approval (`/approve protect-hooks`
or apply by hand).
