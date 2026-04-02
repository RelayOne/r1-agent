# Stoke

An AI coding orchestrator that drives Claude Code and Codex CLI as execution engines with deterministic PLAN->EXECUTE->VERIFY->COMMIT phases, multi-model routing, intelligent retry, parallel agent coordination, and structured quality layers.

**The thesis:** SWE-bench Pro shows the same model jumps ~15 points when you optimize the scaffold. The harness is the product.

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
| `stoke doctor` | Tool dependency check |
| `stoke version` | Version info |

### Build flags

```
--plan <path>        Plan file (default: stoke-plan.json)
--workers <n>        Max parallel agents (default: 4)
--roi <level>        ROI filter: high, medium, low, skip (default: medium)
--sqlite             Use SQLite session store instead of JSON
--interactive        Launch interactive Bubble Tea TUI
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
- Same-error-twice escalation
- Cross-task learned patterns persisted across sessions

## Architecture

```
cmd/stoke/main.go           9 commands, TUI wiring, plan validation, ROI filter, reports
internal/
  app/                       Orchestrator: config + engines + worktree + verify + OnEvent
  audit/                     17 review personas, auto-selection, prompt generation
  config/                    Policy loader, settings builder, auto-detect, validation
  context/                   Three-tier context budget, progressive compaction, 6 reminders
  engine/                    Claude + Codex runners: streaming, process groups, 3-tier timeouts
  failure/                   10 classes, TS/Go/Python/Rust parsers, policy scanner, retry decisions
  hooks/                     PreToolUse guard + PostToolUse monitor, installed in worktrees
  model/                     Task-type inference, routing table, 5-provider fallback chain
  plan/                      Plan loader, cycle detection, dependency validation, ROI filter
  report/                    Structured JSON build reports
  scan/                      Deterministic code scan + security surface mapping
  scheduler/                 GRPW priority, file-scope conflicts, resume
  session/                   JSON store + SQLite store, attempt history, learned patterns
  stream/                    NDJSON parser: 6 event types, drain-on-EOF, 3-tier timeouts
  subscriptions/             Pool allocator: acquire/release, circuit breaker, OAuth poller
  tui/                       Bubble Tea interactive (Focus/Dashboard/Detail) + headless runner
  verify/                    Build/test/lint + protected files + scope check
  workflow/                  Phase machine: retry loop, merge-on-success, cleanup-on-failure
  worktree/                  Git worktree: create, merge (merge-tree), force cleanup, helpers
```

19 packages. 6,500+ lines source. 3,400+ lines tests. 182 test functions. 60 Go files.

## Docs

- [Operator Guide](docs/operator-guide.md) -- Mode 1 vs 2, pool setup, macOS caveats, troubleshooting
- [Spec](docs/stoke-spec-final.md) -- 1,091-line frozen spec with 3 adversarial reviews

## Install

```bash
# From source
go build -o stoke ./cmd/stoke
sudo mv stoke /usr/local/bin/

# Or via install script
curl -fsSL https://stoke.dev/install | bash
```

## License

MIT
