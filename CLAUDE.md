# CLAUDE.md -- Stoke

## Build / Test / Vet

```bash
go build ./cmd/stoke
go test ./...
go vet ./...
```

These three commands are the CI gate.

## Package map (19 internal + 1 cmd)

```
cmd/stoke/main.go                 9 commands: run, build, plan, scan, audit, status, pool, doctor, version
                                   --roi, --sqlite, --interactive flags. checkResume, buildRunConfig helpers.

internal/
  app/app.go                       Orchestrator: config + engines + worktree + verify + OnEvent + auto-detect
  audit/audit.go                   17 review personas (5 core + 12 specialized), auto-selection by context,
                                   BuildPrompt with scan findings + security surface injection
  config/
    policy.go                      Full YAML parser + defaults + normalization
    claude_settings.go             Per-worktree settings.json (sandbox, permissions, apiKeyHelper:null)
    detect.go                      Auto-detect build/test/lint for Node.js, Go, Rust, Python
    validate.go                    Policy validation (missing phases, write tools in plan, missing deny rules)
  context/context.go               Three-tier budget (active/session/project), progressive compaction
                                   (gentle/moderate/aggressive), 6 event-driven reminders
  engine/
    types.go                       PhaseSpec, RunSpec, RunResult, OnEventFunc, CommandRunner interface
    env.go                         safeEnvForClaudeMode1(), safeEnvForCodexMode1(), safeEnvMode2()
    claude.go                      StdoutPipe streaming, process group isolation, 3-tier timeouts, MCP triple
    codex.go                       Streaming, CODEX_HOME isolation, stderr rate limit detection
  failure/analyzer.go              10 classes, TS/Go/Python/Rust/Clippy parsers, 9 policy patterns,
                                   ShouldRetry() with escalation logic
  hooks/hooks.go                   PreToolUse guard (protected files, git mutations, destructive commands)
                                   PostToolUse monitor (type bypasses, secret leaks)
                                   Install() writes scripts into worktree, HooksConfig() for settings.json
  model/router.go                  9 task types, benchmark-backed routes (YUV.AI, Terminal-Bench, Milvus),
                                   Resolve() with 5-provider fallback chain, CrossModelReviewer()
  plan/
    plan.go                        Load, Save, Validate (cycle detection DFS, duplicate IDs, missing deps)
    roi.go                         ROI classification (High/Medium/Low/Skip), FilterByROI()
  report/report.go                 BuildReport with per-task TaskReport, FailureReport, ReviewReport
  scan/
    scan.go                        Deterministic code scan: 18 rules (secrets, eval, innerHTML, exec, etc.)
    security.go                    Security surface mapping: auth, crypto, injection, network, file categories
  scheduler/scheduler.go           GRPW priority ordering, file-scope conflict detection, resume support
  session/
    store.go                       SessionStore interface + JSON file store: state, attempts, learning
    sqlstore.go                    SQLite-backed store: WAL mode, same interface, Stats() method
  stream/parser.go                 NDJSON: 6 event types, drain-on-EOF, 3-tier timeouts (idle/post-result/global)
  subscriptions/
    manager.go                     Acquire/Release with mutex, circuit breaker (3 fails -> 5min), utilization
    usage.go                       OAuth usage endpoint poller (api.anthropic.com/api/oauth/usage)
  tui/
    runner.go                      Headless text runner for CI/CD: TaskStart, Event, TaskComplete, Summary
    interactive.go                 Bubble Tea TUI: Dashboard/Focus/Detail modes, keyboard nav, pool bars
  verify/pipeline.go               Build/test/lint + CheckProtectedFiles + CheckScope + AnalyzeOutcomes
  workflow/workflow.go             Phase machine: plan -> execute+verify retry loop (clean worktree per retry),
                                   scope enforcement, cross-model review gate, merge-on-success, hooks install
  worktree/
    manager.go                     Create (with BaseCommit), merge (mergeMu + merge-tree), force cleanup
    helpers.go                     ModifiedFiles, DiffSummary, ScopeCheck, CommitAll, ValidateMerge
```

## Key design decisions

1. `cmd.Dir` for worktree cwd (Claude Code has no `--cd` flag)
2. `--tools` for hard built-in restriction; `--allowedTools` only auto-approves
3. MCP triple isolation: `--strict-mcp-config` + empty config + `--disallowedTools mcp__*`
4. Sandbox via settings.json per worktree (no `--sandbox` CLI flag)
5. `apiKeyHelper: null` (JSON null via `*string`) suppresses repo helpers in Mode 1
6. `sandbox.failIfUnavailable: true` -- fail-closed
7. Process group isolation: `Setpgid: true` + `killProcessGroup()` (SIGTERM then SIGKILL)
8. `git merge-tree --write-tree` for zero-side-effect conflict validation
9. `mergeMu sync.Mutex` serializes all merges to main
10. GRPW priority: tasks with most downstream work dispatch first
11. Cross-model review via `model.CrossModelReviewer()`: Claude implements -> Codex reviews
12. Retry: compare BEFORE overwriting `lastFailure`. Copy phase, don't mutate. Clean worktree per retry.
13. `BaseCommit` captured at worktree creation for `diff BaseCommit..HEAD`
14. Worktree cleanup: `--force` + `os.RemoveAll` fallback + `worktree prune`
15. `model.Resolve()` walks Primary -> FallbackChain (Claude -> Codex -> OpenRouter -> API -> lint-only)
16. Enforcer hooks installed in every worktree via `hooks.Install()`
17. Event-driven reminders fire during tool use (context >60%, error 3x, test write, etc.)
18. ROI filter removes low-value tasks before execution
19. `session.SessionStore` interface: both JSON (`Store`) and SQLite (`SQLStore`) satisfy it
