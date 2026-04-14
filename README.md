# Stoke

An AI coding orchestrator that drives Claude Code and Codex CLI as execution engines with deterministic PLAN->EXECUTE->VERIFY->COMMIT phases, multi-model routing, intelligent retry, parallel agent coordination, and structured quality layers.

**The thesis:** SWE-bench Pro shows the same model jumps ~15 points when you optimize the scaffold. The harness is the product. (Stoke reports deltas on SWE-bench Pro, SWE-rebench, and Terminal-Bench — not contaminated Verified numbers. See [docs/benchmark-stance.md](docs/benchmark-stance.md).)

## Quick start

```bash
go build ./cmd/stoke
go test ./...

# Single task with auto-detected build/test/lint
stoke run --task "Add request ID middleware" --dry-run

# Multi-task plan with parallel agents
stoke build --plan stoke-plan.json --workers 4 --dry-run

# Generate a plan from codebase analysis
stoke plan --task "Add JWT auth" --dry-run

# Deterministic code scan
stoke scan --security

# Multi-perspective audit (17 personas)
stoke audit --dry-run

# Check progress / resume after crash
stoke status

# Check subscription utilization
stoke pool --claude-config-dir /pool/claude-1
```

## Commands

| Command | Purpose |
|---------|---------|
| `stoke run` | Single task: PLAN -> EXECUTE -> VERIFY -> COMMIT |
| `stoke build` | Multi-task plan with parallel agents, resume, ROI filter |
| `stoke plan` | Generate task plan from codebase analysis |
| `stoke scan` | Deterministic code scan (secrets, eval, injection, debug) |
| `stoke audit` | Multi-perspective review (17 personas, auto-selected) |
| `stoke status` | Session dashboard (progress, cost, learned patterns) |
| `stoke pool` | Subscription pool utilization + circuit breaker |
| `stoke pools` | List configured pool directories |
| `stoke add-claude` | Register a Claude pool directory |
| `stoke add-codex` | Register a Codex pool directory |
| `stoke remove-pool` | Remove a pool directory |
| `stoke ship` | End-to-end: plan -> build -> ship |
| `stoke mission` | Multi-phase mission execution |
| `stoke serve` | HTTP API server for programmatic access |
| `stoke mcp-serve` | MCP codebase tool server |
| `stoke yolo` | Execute without verification gates |
| `stoke scope` | Display allowed file scope for a task |
| `stoke repair` | Auto-fix common configuration issues |
| `stoke doctor` | Tool dependency check |
| `stoke version` | Version info |

### Build flags

```
--plan <path>        Plan file (default: stoke-plan.json)
--workers <n>        Max parallel agents (default: 4)
--roi <level>        ROI filter: high, medium, low, skip (default: medium)
--sqlite             Use SQLite session store instead of JSON
--interactive        Launch interactive Bubble Tea TUI
--specexec           Enable speculative parallel execution
--dry-run            Show plan without executing
```

## How it works

```
stoke build --plan stoke-plan.json
  |
  +-- Load plan, validate (cycles, deps, duplicate IDs)
  +-- ROI filter: remove low-value tasks
  +-- Auto-detect build/test/lint commands
  +-- Sort tasks by GRPW priority (critical path first)
  +-- For each dispatchable task (parallel, file-scope conflicts):
       |
       +-- Resolve provider (fallback: Claude -> Codex -> OpenRouter -> API -> lint-only)
       +-- Acquire pool (least loaded, circuit breaker)
       +-- Create git worktree + install enforcer hooks
       +-- PLAN phase    Claude, read-only tools, MCP disabled
       +-- EXECUTE phase  Claude or Codex per task type, sandbox on
       +-- VERIFY phase   Build/test/lint + scope check + protected files
       +-- REVIEW         Cross-model gate (Claude -> Codex or vice versa)
       +-- MERGE          git merge-tree validation, then merge, then cleanup
       +-- Save attempt + session state + learned patterns
       |
       +-- On failure: classify (10 classes), extract specifics,
       |   discard worktree, create fresh, inject retry brief + diff
       |   (max 3 attempts, escalate on same error)
  |
  +-- Generate report (.stoke/reports/latest.json)
  +-- Fire event-driven reminders (context >60%, error 3x, etc.)
```

## V2 Governance Architecture

Stoke v2 adds a multi-role consensus layer that wraps the execution engine:

- **Ledger** — Append-only content-addressed graph. No updates or deletes.
- **Bus** — Durable WAL-backed event system with hooks and causality tracking.
- **Supervisor** — 30 deterministic rules across 10 categories enforce governance.
- **Consensus Loops** — 7-state machine for structured agreement (PRD→SOW→ticket→PR).
- **Stances** — 10 roles (PO, CTO, QA Lead, etc.) each with dedicated concern fields.
- **Harness** — Stance lifecycle management with per-role tool authorization.
- **Bridge** — Adapters wiring v1 systems (cost tracking, verification, wisdom, audit) into the v2 event bus and ledger.

## What's enforced

**Before every commit/merge:**
- Protected file check: `.claude/`, `.stoke/`, `CLAUDE.md`, `.env*`, `stoke.policy.yaml`
- Scope check: agent can only modify files declared in `task.files`
- Build/test/lint verification pipeline
- Cross-model review gate (blocks merge on execution failure or rejection)

**Auth isolation (Mode 1):**
- `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, cloud provider vars stripped from env
- `apiKeyHelper: null` in settings (repo helpers cannot override OAuth)
- Each pool runs in its own `CLAUDE_CONFIG_DIR` / `CODEX_HOME`

**MCP isolation (plan + verify phases):**
- `--strict-mcp-config --mcp-config <empty.json>`
- `--disallowedTools mcp__*`

**Sandbox:**
- `sandbox.failIfUnavailable: true` (fail-closed)
- Filesystem writes restricted to worktree

**11-layer policy engine:** `--tools`, MCP isolation, `--disallowedTools`, `--allowedTools`, settings.json, worktree isolation, sandbox, `--max-turns`, enforcer hooks (PreToolUse + PostToolUse), verify pipeline, git ownership.

**Retry intelligence:**
- 10 failure classes with TS/Go/Python/Rust/Clippy parsers
- 9 policy violation patterns
- Clean worktree per retry (learning is in instructions, not code state)
- DiffSummary injected into retry prompt
- Same-error-twice escalation (failure fingerprint dedup)
- Cross-task learned patterns persisted via wisdom store
- AST-aware critic gate (secrets, SQL injection, empty catches) runs before build/test

## Architecture

```
cmd/stoke/main.go            20 commands, TUI wiring, plan validation, ROI filter, reports
internal/
  app/                        Orchestrator: config + engines + worktree + verify + OnEvent
  audit/                      17 review personas, auto-selection, prompt generation
  config/                     Policy loader (auto-discover stoke.yaml), settings, auto-detect, validation
  context/                    Three-tier context budget, progressive compaction, 6 reminders
  costtrack/                  Per-session cost tracking, budget-aware provider routing
  critic/                     AST-aware pre-commit critic: secrets, injection, debug prints
  engine/                     Claude + Codex runners: streaming, process groups, 3-tier timeouts
  failure/                    10 classes, TS/Go/Python/Rust parsers, fingerprint dedup, retry decisions
  hooks/                      PreToolUse guard + PostToolUse monitor, installed in worktrees
  logging/                    Structured slog logging: component/task tagging, cost/attempt events
  mission/                    Multi-phase mission execution with convergence validation
  model/                      Task-type routing, cost-aware fallback chain, cross-model review
  plan/                       Plan loader, cycle detection, file validation, ROI filter
  report/                     Structured JSON build reports
  scan/                       Deterministic code scan + security surface mapping
  scheduler/                  GRPW priority, file-scope conflicts, speculative execution, resume
  session/                    JSON store + SQLite store, attempt history, learned patterns
  specexec/                   Speculative parallel execution: fork N approaches, pick winner
  stream/                     NDJSON parser: 6 event types, drain-on-EOF, 3-tier timeouts
  subscriptions/              Pool allocator: acquire/release, circuit breaker, OAuth poller
  taskstate/                  Task state machine: phase transitions, evidence, fingerprint dedup
  tui/                        Bubble Tea interactive (Focus/Dashboard/Detail) + headless runner
  verify/                     Build/test/lint + protected files + scope check
  wisdom/                     Cross-task learning: gotchas, decisions, patterns with fingerprints
  workflow/                   Phase machine: retry loop, critic gate, merge-on-success, hooks
  worktree/                   Git worktree: create, merge (merge-tree), force cleanup, helpers
  + 77 additional packages    (convergence, repomap, symindex, goast, plugins, remote, etc.)
```

132 internal packages + 1 cmd + 9 bench = 142 packages. 55K lines source. 35K lines tests. 1,700+ test functions. 320+ Go files.

## Docs

- [Operator Guide](docs/operator-guide.md) -- Mode 1 vs 2, pool setup, macOS caveats, troubleshooting
- [Spec](docs/stoke-spec-final.md) -- 1,091-line frozen spec with 3 adversarial reviews

## Install

```bash
# From source (requires Go 1.22+)
go build -o stoke ./cmd/stoke
sudo mv stoke /usr/local/bin/
```

## License

MIT
