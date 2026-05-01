# CLAUDE.md -- Stoke

## Build / Test / Vet

```bash
go build ./cmd/r1
go test ./...
go vet ./...
```

These three commands are the CI gate.

## Package map (132 internal + 1 cmd + 9 bench)

```
cmd/r1/main.go                    20 commands. --roi, --sqlite, --interactive, --specexec flags.

--- V2 GOVERNANCE ---
contentid/                         Content-addressed ID generation (SHA256, 16 prefixes)
stokerr/                           Structured error taxonomy (10 error codes)
ledger/                            Append-only content-addressed graph (nodes, edges, filesystem + SQLite)
ledger/nodes/                      22 node type structs with NodeTyper interface
ledger/loops/                      7-state consensus loop tracker
bus/                               Durable WAL-backed event bus (hooks, delayed events, causality)
supervisor/                        Deterministic rules engine (30 rules, 10 categories, 3 manifests)
supervisor/manifests/              Rule set manifests per supervisor tier (mission, branch)
supervisor/rules/consensus/        Consensus rules (review, dissent, convergence, timeout)
supervisor/rules/cross_team/       Cross-team coordination rules
supervisor/rules/drift/            Drift detection rules
supervisor/rules/hierarchy/        Hierarchy enforcement rules
supervisor/rules/research/         Research lifecycle rules
supervisor/rules/sdm/              SDM advisory rules
supervisor/rules/skill/            Skill lifecycle rules
supervisor/rules/snapshot/         Snapshot protection rules
supervisor/rules/trust/            Trust verification rules (second-opinion gates)
concern/                           Per-stance context projection (10 sections, 9 role templates)
concern/sections/                  Ledger-backed section renderers for concern field projection
concern/templates/                 Role-specific concern field templates (CTO, Dev, Reviewer)
harness/                           Stance lifecycle: spawn/pause/resume/terminate (11 templates)
harness/models/                    Model provider interface and mock for stance workers
harness/prompts/                   System prompt templates per stance role
harness/stances/                   Stance definitions (CTO, Dev, Reviewer, PO) with system prompts
harness/tools/                     Tool authorization model for stance workers
snapshot/                          Protected baseline manifest (file paths + content hashes)
wizard/                            First-time config with presets (minimal/balanced/strict)
skillmfr/                          Skill manufacturing pipeline (4 workflows, confidence ladder)
bench/                             Golden mission benchmarking with regression detection
bridge/                            V1→V2 bridge adapters (cost, verify, wisdom, audit → bus+ledger)

--- CORE WORKFLOW ---
agentloop/                         Native agentic tool-use loop via Anthropic Messages API (caching, parallel tools)
app/                               Orchestrator: config + engines + worktree + verify + OnEvent + auto-detect
hub/                               Typed event hub with subscriber hooks (lifecycle, tool, cost events)
hub/builtin/                       Built-in hub subscribers (honesty gate, cost tracker)
mission/                           Mission lifecycle runner with convergence loop and phase handlers
workflow/                          Phase machine: plan -> execute+verify retry loop, scope, review, merge
engine/                            Claude/Codex CLI runners: process groups, streaming, 3-tier timeouts
orchestrate/                       Mission execution pipeline integrator
scheduler/                         GRPW priority ordering, file-scope conflict, resume, WithSpecExec wrapper
plan/                              Load/Save/Validate plans (cycle DFS, deps), ROI filter
taskstate/                         Anti-deception task state: phase transitions, evidence gates

--- PLANNING & DECOMPOSITION ---
interview/                         Socratic clarification phase before task execution
intent/                            Intent classification and verbalization gate
conversation/                      Multi-turn conversation state management
skillselect/                       Tech stack auto-detection and skill mapping from repo structure

--- CODE ANALYSIS ---
goast/                             Go AST-based code analysis and extraction
repomap/                           Repository map with graph-ranked importance (PageRank)
symindex/                          Symbol indexing for fast function/class lookup
depgraph/                          Import/dependency graph extraction
chunker/                           Semantic code chunking by meaningful boundaries
tfidf/                             TF-IDF semantic search over codebase files
vecindex/                          Vector/embedding-based semantic code search
semdiff/                           Semantic diff analysis with structural changes
diffcomp/                          Diff compression for compact change representation
gitblame/                          Git blame integration for attribution-aware editing

--- FILE & WORKSPACE ---
atomicfs/                          Multi-file atomic edits with transactional semantics
fileutil/                          Shared file system operations and path safety
filewatcher/                       File system monitoring with cache invalidation
worktree/                          Git worktree create/merge/cleanup, BaseCommit, mergeMu
branch/                            Conversation branching for multiple solution paths
hashline/                          Hash-anchored line verification for concurrent edits

--- TESTING & VERIFICATION ---
baseline/                          Captures and compares build/test/lint state
verify/                            Build/test/lint pipeline + CheckProtectedFiles + CheckScope
convergence/                       Adversarial self-audit for mission completion
testgen/                           Test scaffold generation from function signatures
testselect/                        Dependency-aware test selection via import graph
critic/                            Adversarial pre-commit critic for quality gates

--- ERROR HANDLING & RECOVERY ---
failure/                           10 failure classes, fingerprint dedup, ShouldRetry escalation
errtaxonomy/                       Structured error taxonomy for retry strategies
checkpoint/                        Synchronous checkpointing before dangerous operations

--- CODE GENERATION ---
patchapply/                        Unified diff parsing/application with fuzzy match
extract/                           Structured content parsing from LLM output
autofix/                           Auto-lint-and-fix iterative improvement loop
conflictres/                       Smart merge conflict resolution with semantics
tools/                             Cascading str_replace algorithm (exact, whitespace, ellipsis, fuzzy)

--- AGENT BEHAVIOR ---
boulder/                           Idle detection and continuation enforcement
specexec/                          Speculative parallel execution (4 strategies, pick winner)
handoff/                           Agent-to-agent context transfer management

--- KNOWLEDGE & LEARNING ---
memory/                            Persistent cross-session knowledge storage
wisdom/                            Cross-task learnings: gotchas, decisions, FindByPattern
research/                          Persistent indexed research storage with FTS5
flowtrack/                         Flow-aware intent tracking from action sequences
replay/                            Session recording for post-mortem debugging

--- LLM INTEGRATION ---
apiclient/                         Multi-provider SSE streaming API client
provider/                          Direct AI model API clients for providers
mcp/                               Model Context Protocol codebase tool server
model/                             9 task types, 5-provider fallback, CostAwareResolve
prompt/                            Prompt engineering utilities and fingerprinting
prompts/                           BuildPlanPrompt, BuildExecutePrompt, BuildReviewPrompt
promptcache/                       Cache-aligned prompt construction for max hits
microcompact/                      Cache-aligned context compaction
ctxpack/                           Adaptive context bin-packing for window limits
tokenest/                          Token count estimation without external APIs
costtrack/                         Real-time cost tracking with budget alerts

--- PERMISSIONS & SECURITY ---
consent/                           Human-in-the-loop approval workflow
rbac/                              Role-Based Access Control enforcement
hooks/                             Anti-deception: PreToolUse/PostToolUse guards, Install()
scan/                              18 deterministic rules (secrets, eval, injection, exec)

--- CONFIG & SESSION ---
config/                            YAML policy parser, auto-detect, claude_settings, validate
session/                           SessionStore interface: JSON + SQLite (WAL), attempts, state
subscriptions/                     Pool Acquire/Release, circuit breaker, usage poller
pools/                             Worker pool management and scaling
context/                           Three-tier context budget, progressive compaction, reminders

--- INFRASTRUCTURE ---
agentmsg/                          Inter-agent communication protocol
dispatch/                          Three-tier message dispatch queue
logging/                           Structured leveled logging (Task, Attempt, Cost helpers)
metrics/                           Thread-safe counters and performance metrics
telemetry/                         Structured metrics collection
notify/                            Event notification system
stream/                            NDJSON parser: 6 event types, drain-on-EOF, 3-tier timeouts
jsonutil/                          JSON parsing from mixed-format LLM outputs
schemaval/                         Structured output validation for responses
validation/                        Input validation at API boundaries

--- UI & INTERFACES ---
tui/                               Headless runner + Bubble Tea TUI (Dashboard/Focus/Detail)
viewport/                          Constrained file viewport for focused viewing
repl/                              Interactive REPL interface
server/                            Mission API HTTP endpoints
remote/                            Build session progress reporting to dashboard
report/                            BuildReport with per-task TaskReport, FailureReport
progress/                          Plan-aware progress estimation and ETA
audit/                             17 review personas (5 core + 12 specialized)

--- LIFECYCLE ---
skill/                             Reusable workflow pattern system
plugins/                           Plugin manifest and loading system
preflight/                         Pre-flight workspace assertions
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
20. Budget enforcement: `CostTracker.OverBudget()` checked before each execute attempt
21. Failure fingerprint dedup: `failure.Compute()` + `MatchHistory()` escalates repeated failures
22. `verificationExplicit` bool distinguishes "all false" from "omitted" in YAML policy parsing
23. Dependency-aware test selection via `testselect.BuildGraph()` narrows `go test` to affected pkgs
24. Ranked repomap injected into execute prompts (token-budgeted via `RenderRelevant`)
25. Pre-merge snapshots (`snapshot.Take`) with restore-on-failure for safe rollback
26. Speculative execution (`--specexec`): 4 strategies in parallel, pick the winner
27. Codex/Claude parity: both runners populate CostUSD, DurationMs, NumTurns, Tokens
28. V2 bridge adapters: v1 cost/verify/wisdom/audit emit bus events + write ledger nodes via bridge package
