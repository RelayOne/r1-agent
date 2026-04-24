# Architecture

This document covers Stoke's technical architecture: the stack, the
repository layout, the major subsystems and how they talk to each
other, the data model, the execution flow, the testing architecture,
and the infrastructure a deployed Stoke instance requires.

For the user-facing view (what the operator sees, in order), see
[HOW-IT-WORKS.md](HOW-IT-WORKS.md). For the marketing pitch with no
jargon, see [BUSINESS-VALUE.md](BUSINESS-VALUE.md). For the full
feature table with spec traceability, see [FEATURE-MAP.md](FEATURE-MAP.md).

## Tech stack

| Layer | Technology |
|-------|------------|
| Language | Go 1.25.5 (CGO enabled for SQLite) |
| Module | `github.com/ericmacdougall/stoke`, one module, no vendored deps beyond `go.mod` |
| Concurrency | Native goroutines + sync primitives; process-group isolation for child LLM CLIs |
| Persistence | SQLite (modernc.org/sqlite, pure Go SQLite driver) + append-only NDJSON WAL + JSON session files |
| Search | FTS5 (built into SQLite) for wisdom, memory, research. Vector search via sqlite-vec (scoped) |
| Event bus | Custom WAL-backed, ULID-indexed, parent-hash chained NDJSON at `.stoke/bus/events.log` |
| Ledger | Content-addressed SHA256, 16 node-type prefixes, Merkle chain with two-level commitment for redaction |
| LLM transport | Anthropic Messages API (native tool use), Claude Code CLI, Codex CLI, Direct OpenRouter, Direct APIs via SSE |
| TUI | Bubble Tea (Elm-arch Go TUI framework) + lipgloss styling |
| HTTP | stdlib `net/http` everywhere — zero framework dependency in the runtime path |
| Telemetry | Structured slog + custom perflog package (microsecond spans, `STOKE_PERFLOG=1`) |
| Sandboxing | Per-pool `CLAUDE_CONFIG_DIR` / `CODEX_HOME`, per-worktree `settings.json`, `Setpgid:true`, SIGTERM→SIGKILL group kill |
| CI | GitHub Actions: build/vet/test + race + lint (advisory) + govulncheck + gosec |
| Release | goreleaser (cross-platform), cosign keyless OIDC signing, Homebrew via `ericmacdougall/homebrew-stoke`, Docker via ghcr.io |
| Container | Multi-stage Dockerfile (distroless runtime), separate `Dockerfile.pool` for worker images |

## Repository map

```
cmd/                              Nine executables
├── stoke/                        Primary orchestrator (~7K LOC in main.go, 30+ subcommands)
├── stoke-acp/                    Agent Client Protocol adapter (S-U-002)
├── stoke-a2a/                    Agent-to-Agent peering (signed cards, HMAC, x402, saga compensators)
├── stoke-mcp/                    MCP codebase tool server
├── stoke-server/                 Mission API HTTP server (dashboards, programmatic access)
├── stoke-gateway/                Managed-cloud gateway
├── r1-server/                    Per-machine dashboard (port 3948)
├── chat-probe/                   Chat-descent + sessionctl socket diagnostic
└── critique-compare/             Bench runner for reviewer prompt tuning

internal/                         180 packages — see PACKAGE-AUDIT.md for the full table
├── ledger/, bus/, supervisor/    v2 governance: append-only graph, WAL events, rules
├── concern/, harness/, stance*/  Per-stance context projection + lifecycle + role-aware signing
├── app/, workflow/, mission/     Core orchestration: phase machine + multi-phase runner
├── engine/, agentloop/           Claude/Codex CLI runners + native Anthropic Messages loop
├── scheduler/, specexec/         GRPW priority + speculative parallel execution
├── plan/, taskstate/             Plan load/save/validate + anti-deception state machine
├── verify/, critic/, convergence/ Build/test/lint + AST critic + adversarial self-audit
├── executor/, browser/, deploy/  Multi-task executors (code, research, browse, deploy, delegate)
├── failure/, errtaxonomy/        10 failure classes, fingerprint dedup, retry escalation
├── memory/, wisdom/, research/   Cross-session knowledge with FTS5
├── repomap/, symindex/, goast/   PageRank repomap + symbol index + Go AST analysis
├── consent/, hooks/, hitl/       Anti-deception: PreToolUse/PostToolUse, HITL approval
├── policy/, promptguard/, redact/ Policy engine, prompt-injection defense, secret redaction
├── mcp/, model/, provider/       MCP client/server, task-type routing, 5-provider fallback
├── costtrack/, subscriptions/    Real-time cost tracking, pool acquire/release, circuit breaker
├── session/, eventlog/, runtrack/ Persistence: JSON + SQLite session stores, WAL bus, run track
├── trustplane/, a2a/, delegation/ TrustPlane gateway, A2A peering, delegation executor
├── tui/, server/, repl/          User interfaces
└── (+ ~100 focused packages)     Each doing one thing well — see PACKAGE-AUDIT.md

bench/                            11 subpackages — golden mission bench
corpus/                           Independent bench modules with their own go.mod
specs/                            Scoped specs (STATUS: done/ready/in-progress)
docs/                             This tree
plans/                            Portfolio execution index + scope-suite ladder state
configs/                          Sample `stoke.policy.yaml`, MCP manifests, provider pools
.claude-settings/                 Pre-authored worktree settings.json for sandbox + tools + MCP
```

## System components

### Ledger (`internal/ledger/`)

Append-only content-addressed graph. Every node is a
`(type, payload) → SHA256 prefix + body` pair. Edges encode causality
and consensus.

- 22 node type structs (`internal/ledger/nodes/`): PRD, SOW, Ticket,
  Review, Artifact, Cost, Verify, AuditFinding, Stance, Skill,
  Concern, Merge, etc. — each implements `NodeTyper`.
- 7 edge types: `produces`, `reviews`, `blocks`, `depends-on`,
  `consumes`, `consents`, `escalates`.
- 7-state consensus loop tracker (`internal/ledger/loops/`) drives
  the `PRD → SOW → ticket → PR → landed` lifecycle.
- Dual backend: filesystem (append-only file per node type) and
  SQLite (normalized tables + edge table + WAL). The interface makes
  them interchangeable; filesystem is the default for local runs,
  SQLite for long-lived dashboards.
- Redaction uses a two-level Merkle commitment. IDs depend on the
  header + commitment hash, not raw content, so tier-2 content wipes
  preserve chain integrity forever (scoped: `specs/ledger-redaction.md`).

### Bus (`internal/bus/`)

Durable WAL-backed event system. 30+ event types. Hooks.
Delayed events (cron-style). Parent-hash causality chains.

- ULID-indexed so sort order = time order without a second column.
- Every event carries a STOKE protocol envelope (`stoke_version`,
  `instance_id`, `trace_parent` W3C TraceContext, optional
  `ledger_node_id`). See `docs/stoke-protocol.md`.
- WAL on disk at `.stoke/bus/events.log`; SQLite upgrade scoped at
  `specs/event-log-proper.md` (ULID-indexed events table with
  parent-hash chain, `stoke sow --resume-from=<seq>` restart).

### Supervisor (`internal/supervisor/`)

Deterministic rules engine. 30 rules across 10 categories.

- Categories: consensus, drift, hierarchy, research, skill, snapshot,
  SDM (software delivery management), cross-team, trust, lifecycle.
- Three per-tier manifests (mission, branch, session) control which
  rules fire where.
- Rules are pure functions of event + ledger state → advisory or
  blocking verdict. No LLM involvement; deterministic is the point.
- Example rules: `ConsensusReview`, `TrustSecondOpinion`,
  `SnapshotProtection`, `DriftDetect`, `HierarchyEnforce`,
  `ResearchLifecycle`, `SkillLifecycle`.

### Concern + Harness (`internal/concern/`, `internal/harness/`)

Per-stance context projection and stance lifecycle.

- 10 concern sections (current-task, recent-activity, relevant-code,
  dependencies, wisdom, memory, risks, verdicts, costs, notes) + 9
  role templates (CTO, Dev, Reviewer, PO, QA Lead, Researcher, SDM,
  Deployer, Harness) render a role-specific system prompt from
  ledger state.
- Stance lifecycle: spawn / pause / resume / terminate with 11 stance
  templates. Per-stance tool authorization — a Reviewer never gets
  `Write`; a PO never gets `Bash`.

### Engine + Agentloop (`internal/engine/`, `internal/agentloop/`)

The actual "call an LLM" layer.

- `engine/` wraps Claude Code CLI and Codex CLI as subprocesses.
  Process-group isolation (`Setpgid: true`), streaming NDJSON parser
  (`internal/stream/`), 3-tier timeouts (init, step, turn),
  SIGTERM→SIGKILL group kill on timeout.
- `agentloop/` is the native agentic tool-use loop against the
  Anthropic Messages API — full parallel tools, prompt caching,
  honeypot gate, tool-output sanitization, and context-budget
  reminders. Used when the operator has a direct API key and prefers
  not to shell out to Claude Code CLI.

### Scheduler (`internal/scheduler/`)

Task dispatch, parallel execution, conflict resolution.

- **GRPW priority** (Generalized Resource-constrained Project
  scheduling Problem, Worktree-aware): tasks with the most downstream
  work dispatch first, so the critical path is never waiting on a
  leaf.
- File-scope conflict detection: if task A writes `a.go` and task B
  reads `a.go`, A runs first. If they both write, serialized.
- Resume support reads `.stoke/session.json` (or the SQL store),
  picks up at the first incomplete task.
- `WithSpecExec(N, strategy)` wraps dispatch with speculative parallel
  execution: fork N approaches, pick the winner by verification
  result.

### Workflow (`internal/workflow/`)

The phase machine. `PLAN → EXECUTE → VERIFY → (maybe) RETRY → REVIEW → MERGE`.

- Plan: Claude, read-only tools, MCP disabled, repomap injected.
- Execute: Claude or Codex per task type, sandbox on, verification
  descent gate + honeypot gate + tool-output sanitization + critic
  gate.
- Verify: build + test + lint + scope check + protected-file check
  + AST-aware critic (secrets, SQL injection, debug prints,
  empty-catch patterns).
- Review: cross-model gate. If Claude implemented, Codex reviews.
  If Codex implemented, Claude reviews. Dissent blocks merge.
- Merge: `git merge-tree --write-tree` for zero-side-effect conflict
  validation; serialized merge via `mergeMu sync.Mutex`; force
  cleanup via `git worktree remove --force` + `os.RemoveAll` fallback
  + `git worktree prune`.

### Verification Descent (H-91 series)

The anti-deception layer. Separate engine activated via
`STOKE_DESCENT=1` or `--descent`.

- Anti-deception contract injected into every worker prompt at dispatch.
- Forced self-check before turn end: the model signals completion
  evidence; the parser cross-checks against git state + AC state +
  tool-call log.
- Ghost-write detector: post-tool hook flags "tool reported success
  but file is empty" fakes.
- Per-file repair cap of 3 attempts (Cursor 2.0 parity).
- Bootstrap per descent cycle: manifest-touching repairs re-install
  deps before the next AC runs.
- Env-issue worker tool: workers self-report environment blockers;
  descent skips multi-analyst convergence (~$0.10/AC saved).
- VerifyFunc on acceptance criteria lets non-code executors (research,
  browser, deploy, delegation) plug into the same 8-tier ladder.
- Soft-pass AC after 2× `ac_bug` verdicts so reviewers blaming the
  AC can't spin forever.

### Executors (`internal/executor/`, `internal/browser/`, `internal/deploy/`, `internal/delegation/`)

Multi-task agent: one interface, many backends.

- `Executor` interface: `Execute`, `BuildCriteria`, `BuildRepairFunc`,
  `BuildEnvFixFunc`. Uniform across task types.
- `stoke task "<free-text>"` routes via a classifier that maps text
  to the right executor.
- Shipping executors: CodeExecutor (SOW-backed),
  ResearchExecutor MVP, BrowserExecutor Part 1 (http + HTML strip +
  verify-contains/regex), DeployExecutor (Fly.io), DelegationExecutor
  MVP (hired-agent verify-settle via TrustPlane).

### Failure recovery (`internal/failure/`, `internal/errtaxonomy/`)

- 10 failure classes: BUILD, TEST, LINT, SCOPE, PROTECTED_FILE,
  TIMEOUT, AUTH, TOOL, SANDBOX, UNCLASSIFIED.
- TS / Go / Python / Rust / Clippy error parsers extract the first
  ~3 meaningful errors.
- 9 policy violation patterns detect hook bypass, path escape,
  protected-file write, scope-exceed, etc.
- Fingerprint dedup (`failure.Compute`): same error twice → escalate
  to a different model / strategy.
- Clean worktree per retry so "learning" is carried in instructions,
  not state.

### MCP client + server (`internal/mcp/`)

- Server (`stoke mcp-serve`): exposes ledger, wisdom, research, skill
  stores as MCP tools for external consumers.
- Client: consumes external MCP servers (GitHub, Linear, Slack,
  Postgres, custom). Per-server circuit breaker, trust label,
  concurrency caps, auth env vars. HTTPS enforcement on non-localhost
  URLs. Per-CallTool audit marker.

### TrustPlane + A2A (`internal/trustplane/`, `internal/a2a/`)

- `trustplane.RealClient`: stdlib-only HTTP implementation of the
  8-method `Client` interface. Ed25519 DPoP signing via `trustplane/dpop`
  (RFC 9449, no go-jose dependency).
- A2A (Agent-to-Agent): HMAC tokens + trust clamp + x402 micropayments
  + signed agent cards + JWKS + saga compensators. Agent Card spec
  v1.0.0; canonical path `/.well-known/agent-card.json`; legacy
  `/.well-known/agent.json` 308-redirects until 2026-05-22 sunset.

### r1-server (`cmd/r1-server/`)

Per-machine dashboard. Port 3948. Discovers running Stoke instances
via `<repo>/.stoke/r1.session.json` signature files, exposes the
event stream + ledger DAG + checkpoints over HTTP + SSE. Stoke
continues to work when r1-server isn't installed (silent fallback).

- Polling-only filesystem scanner (60s), walks `$HOME/{,code,projects,dev,repos,src,work}`, skips `.git`/`node_modules`/`vendor`/`target`.
- Per-session event tailer (500ms), PID-liveness probe via signal 0.
- Embedded vanilla-JS SPA: instance list + live-tailing stream view.
  3D force-directed ledger visualizer (Three.js + time scrubber,
  InstancedMesh, Web Worker simulation) scoped in `specs/r1-server-ui-v2.md`.

## Data models

### Session

```
Session {
  ID:            string (ULID)
  PlanPath:      string
  StartedAt:     time.Time
  Tasks:         []TaskState
  Attempts:      []Attempt
  Cost:          CostSummary
  LearnedPatterns: []Pattern
  BaseCommit:    string (captured at worktree creation)
}
```

Persisted via `session.SessionStore` interface. Two backends: JSON
file (`Store`) and SQLite WAL (`SQLStore`). Both satisfy the same
interface.

### Plan + Task + AcceptanceCriterion

```
Plan {
  Version: string
  Tasks:   []Task
}

Task {
  ID:          string
  Description: string
  Files:       []string   # scope
  Deps:        []string
  AcceptanceCriteria: []AcceptanceCriterion
  ROI:         string     # high|medium|low|skip
  TaskType:    string     # code|research|browse|deploy|delegate
}

AcceptanceCriterion {
  Name:         string
  Command:      string               # optional
  FileExists:   string               # optional
  ContentMatch: *regexp.Regexp       # optional
  VerifyFunc:   func(ctx) (bool,string)  # `json/yaml:"-"` — non-code executors
}
```

Plan validation catches cycles (DFS), duplicate IDs, missing deps,
unknown files. ROI filter removes `low`/`skip` tasks before
execution.

### Ledger node + edge

```
Node {
  ID:        string  # content-addressed SHA256 prefix
  Type:      string  # 16 prefixes: prd, sow, tkt, rev, art, cost, vfy, ...
  Payload:   any     # serialized per-type struct
  ParentHash: string # Merkle chain to previous node of same type
  CreatedAt: time.Time
}

Edge {
  From:      string  # Node.ID
  To:        string  # Node.ID
  Kind:      string  # produces|reviews|blocks|depends-on|consumes|consents|escalates
  CreatedAt: time.Time
}
```

### Bus event

```
Event {
  ID:           string  # ULID
  Type:         string  # e.g. stoke.task.phase.plan.start
  Data:         map[string]any
  StokeVersion: string
  InstanceID:   string
  TraceParent:  string  # W3C TraceContext
  LedgerNodeID: string  # optional
  ParentHash:   string  # causality chain
  OccurredAt:   time.Time
}
```

## API surface

### HTTP (Mission API, `stoke serve` + `stoke-server`)

| Method | Path | Purpose |
|--------|------|---------|
| POST   | `/api/mission` | Start a mission |
| GET    | `/api/mission/{id}` | Mission status |
| GET    | `/api/mission/{id}/events?after=&limit=` | Tail the event stream |
| GET    | `/api/session/{id}/ledger` | Ledger DAG |
| GET    | `/api/session/{id}/checkpoints` | Checkpoint history |
| POST   | `/api/task` | Dispatch a free-text task (agent-serve) |
| GET    | `/api/task/{id}` | Task result (agent-serve) |

Auth: `X-Stoke-Bearer` token.

### A2A (Agent-to-Agent)

| Method | Path | Purpose |
|--------|------|---------|
| GET    | `/.well-known/agent-card.json` | Signed agent card (v1.0.0 canonical) |
| GET    | `/.well-known/agent.json` | 308 redirect to canonical (sunset 2026-05-22) |
| POST   | `/a2a/hire` | Hire the agent for a task (HMAC-authenticated) |
| POST   | `/a2a/settle` | x402 micropayment settlement |

### r1-server (port 3948)

| Method | Path | Purpose |
|--------|------|---------|
| GET    | `/api/health` | Health probe |
| POST   | `/api/register` | Stoke startup registers its `r1.session.json` |
| GET    | `/api/sessions?status=` | Instance list |
| GET    | `/api/session/{id}` | Session metadata |
| GET    | `/api/session/{id}/events?after=&limit=` | Cursor-paginated events |
| GET    | `/api/session/{id}/ledger` | Ledger DAG |
| GET    | `/api/session/{id}/checkpoints` | Checkpoint history |

### MCP (`stoke mcp-serve`, `stoke-mcp`)

12 MCP tools exposing ledger, wisdom, research, and skill stores to
external consumers. Tool names: `stoke_ledger_query`,
`stoke_wisdom_search`, `stoke_wisdom_add`, `stoke_research_search`,
`stoke_research_add`, `stoke_skill_list`, `stoke_skill_invoke`, etc.

### Unix socket (`stoke ctl`, `internal/sessionctl/`)

8 verbs for live session control: `pause`, `resume`, `status`,
`cancel`, `attach`, `replay`, `eject`, `inspect`. Used by `stoke
attach`, the TUI's live-follow mode, and chat-descent control.

## Execution flow

### `stoke build --plan stoke-plan.json`

1. Config load (`internal/config/`). Auto-discover `stoke.policy.yaml`
   starting from CWD, walking up. Validate. Merge
   `verificationExplicit` flag so "all false" vs "omitted" is
   distinguishable.
2. Plan load + validate (`internal/plan/`). Cycle DFS, duplicate IDs,
   dep resolution, file existence check.
3. Auto-detect build/test/lint commands from repo structure
   (`internal/skillselect/` + `internal/config/`).
4. ROI filter: remove `low`/`skip` tasks.
5. Sort by GRPW priority (`internal/scheduler/`).
6. Initialize subscription pools (`internal/subscriptions/`): OAuth
   poller, circuit breaker per pool, least-loaded acquisition.
7. Initialize ledger + bus + supervisor. Register built-in hub
   subscribers (honesty gate, cost tracker).
8. For each dispatchable task:
   a. `model.Resolve(taskType)` walks Primary → FallbackChain
      (Claude → Codex → OpenRouter → Direct API → lint-only).
   b. `subscriptions.Acquire()` picks a pool worker.
   c. `worktree.Create()` makes a fresh worktree at the base commit;
      `hooks.Install()` drops the PreToolUse + PostToolUse guards.
   d. Write `r1.session.json` signature; start heartbeat goroutine.
   e. PLAN phase: `workflow.RunPlan()`. Claude, read-only tools, MCP
      disabled. Repomap injected (`internal/repomap/` ranked by
      PageRank, token-budgeted via `RenderRelevant`).
   f. EXECUTE phase: `workflow.RunExecute()`. Sandbox on. Agentloop
      runs with honeypot gate + tool-output sanitization + verification
      descent + context reminders.
   g. VERIFY phase: `verify.Run()` → build + test + lint + scope check
      + protected-file check + AST critic.
   h. REVIEW phase: `model.CrossModelReviewer()`. Claude implements →
      Codex reviews (or vice versa). Dissent blocks merge.
   i. MERGE phase: `worktree.Merge()` with `git merge-tree --write-tree`
      pre-validation; `mergeMu sync.Mutex` serializes.
   j. `session.Save()` persists attempt + learned patterns + ledger
      node.
9. On task failure: `failure.Classify()` → fingerprint dedup via
   `failure.MatchHistory()`. If same-error-twice, escalate. Otherwise
   discard worktree, create fresh, inject retry brief + diff summary.
   Max 3 attempts.
10. Final report: `report.Build()` writes `.stoke/reports/latest.json`.
11. Event-driven reminders fire throughout (context >60%, error 3×,
    test-write, turn-drift, idle detection).

## Infrastructure

Stoke is un-managed-first. The single binary you build from this repo
does everything the project does.

### Required infrastructure

**Zero.** A plain host with:

- Go 1.25+ (build time only if installing from source)
- Git
- `claude` or `codex` on PATH (the CLIs Stoke drives)
- Any subset of: `ANTHROPIC_API_KEY`, OpenAI OAuth via Codex CLI,
  OpenRouter API key, direct Anthropic API key — Stoke walks the
  fallback chain until one works.

All state is local. SQLite files live under `.stoke/`.

### Optional infrastructure

- **r1-server** (port 3948): per-machine dashboard. Silent fallback
  when not installed. Spawned automatically by Stoke startup via
  `ensureR1ServerRunning()` → `exec.LookPath` + `Setsid:true`.
  Disabled via `STOKE_NO_R1_SERVER=1`.
- **Subscription pools** (`CLAUDE_CONFIG_DIR` / `CODEX_HOME`):
  pre-authenticated directories Stoke round-robins through. Required
  for high-throughput builds where a single subscription rate-limits.
  Registered via `stoke add-claude` / `stoke add-codex`.
- **TrustPlane gateway**: identity anchoring for A2A peering. Opt-in
  via `STOKE_TRUSTPLANE_MODE=real` + `STOKE_TRUSTPLANE_PRIVKEY*`.
  Default is a stub client that talks locally.
- **Managed cloud gateway** (`cmd/stoke-gateway/`): hosted session
  state, centralized pool management, cross-agent audit consolidation.
  Opt-in; local-only path stays feature-complete forever per
  `STEWARDSHIP.md`.
- **MCP servers**: GitHub, Linear, Slack, Postgres, any custom server.
  Configured in `stoke.policy.yaml`.

### Container runtime

- `Dockerfile`: multi-stage, distroless runtime image. Published to
  `ghcr.io/ericmacdougall/stoke:latest` on each tag.
- `Dockerfile.pool`: worker image for the macOS Keychain isolation
  workaround. Docker volume-based isolation lets operators run
  multiple pools on a single macOS host without Keychain collisions.

## Type system and validation

- Go's static type system is the primary validation layer.
- Config validation happens at load time in `internal/config/`. YAML
  fields are explicitly typed; `verificationExplicit bool`
  distinguishes "all false" from "omitted" so policies can be
  semantically different.
- `internal/validation/` runs at every API boundary (HTTP handlers,
  MCP tool entry points, CLI flag parsing).
- `internal/schemaval/` validates structured output from LLMs against
  a JSON schema before it's consumed.
- `internal/stokerr/`: structured error taxonomy with 10 error codes.
  Errors are `stokerr.Error` values carrying a code + context map;
  `errors.As` unwraps them uniformly.
- `internal/jsonutil/` handles the "LLM emitted JSON-adjacent text"
  case — code-fence stripping, comment stripping, trailing-comma
  tolerance — before falling back to stdlib `encoding/json`.

## Testing architecture

- **Unit tests**: co-located with packages. ~100K LOC across ~1,010
  Go files. Table-driven style preferred.
- **Integration tests**: `integration_test.go` at repo root exercises
  full Stoke workflows against local fixtures.
- **Race detector**: full repo is race-clean as of the streamjson
  TwoLane stop-channel fix. CI runs `go test ./... -race`; any new
  race fails the job, not an advisory warning.
- **Red-team corpus** (`internal/redteam/corpus/`): 58-sample
  adversarial regression suite across OWASP LLM01, CL4R1T4S,
  Rehberger SpAIware, Willison's prompt-injection tag. Minimum 60%
  detection rate asserted per category.
- **Golden mission bench** (`bench/`): 20-task corpus across 5
  categories (security, correctness, refactoring, features, testing).
  Nightly workflow produces HTML report artifacts. Regression
  detection vs baseline.
- **Dogfooding scan**: `stoke scan` runs 18+ deterministic rules over
  all 1,010 Go source files; zero blocking findings in CI.
- **Negative hook tests**: 12 attack payloads exercise enforcer hooks;
  all must be blocked.
- **OAuth endpoint contract test**: validates forward-compatibility of
  the OAuth usage endpoint Anthropic exposes (the polling source for
  circuit-breaker state).
- **Agent Card schema tests**: JSON tag pins for A2A v1.0.0
  compatibility.

---

*Last updated: 2026-04-23 (holistic refresh after 30-PR lint + race + OSS-hub campaign).*
