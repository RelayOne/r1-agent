# Feature Map

This is the current main-branch feature inventory for R1.

## Mission Runtime

| Feature | Benefit | Status | Reference |
|---|---|---|---|
| Plan/execute/verify workflow | Keeps agent output tied to explicit execution and verification gates | Done | `app/`, `workflow/`, `verify/` |
| Adversarial review posture | Favors reviewable output over ungoverned autonomous edits | Done | `critic/`, `convergence/`, `engine/` |
| Evidence model | Persists what happened instead of trusting session memory | Done | `ledger/`, `bus/`, `session/` |
| Anti-truncation enforcement | Refuses end-turn while plan items unchecked or truncation phrases are emitted; layered machine-mechanical defense against LLM self-reduction. | Done | `internal/antitrunc/`, `internal/agentloop/antitrunc.go`, `internal/supervisor/rules/antitrunc/`, `cmd/r1/antitrunc_cmd.go`, `docs/ANTI-TRUNCATION.md` |

## Deterministic Skills

| Feature | Benefit | Status | Reference |
|---|---|---|---|
| Deterministic skill manufacture | Turns reusable workflows into governed artifacts | Done | `internal/skillmfr/` |
| Registry and selection | Lets runtime behavior map to explicit skill assets | Done | `internal/skill/`, `internal/skillselect/` |
| Flagship deterministic runtimes | Shows concrete packaged uses of the deterministic runtime path | Done | April 30 main-branch skill runtime commits |

## Skill Pack Lifecycle

| Feature | Benefit | Status | Reference |
|---|---|---|---|
| `r1 skills pack init` | Creates repo-local packs without hand-building directories | Done | `cmd/r1/skills_pack_cmd.go` |
| `info`, `install`, `list`, `publish`, `search`, `update` | Makes pack inspection, activation, discovery, and refresh operational | Done | `cmd/r1/skills_pack_cmd.go` |
| `sign` and `verify` | Adds integrity controls to pack distribution | Done | `cmd/r1/skills_pack_cmd.go` |
| `serve` HTTP registry | Exposes published packs through a stable read-only registry | Done | `cmd/r1/skills_pack_server.go` |
| Runtime signed-pack verification | Prevents runtime registration from ignoring pack integrity | Done | April 30 main-branch signed-pack verification commit |

## Runtime Helper Surfaces

| Feature | Benefit | Status | Reference |
|---|---|---|---|
| Ledger audit runtime | Lets deterministic flows query ledger-backed audit evidence | Done | April 30 main-branch ledger audit runtime commit |
| Skill execution audit runtime | Makes runtime execution behavior inspectable | Done | April 30 main-branch execution audit runtime commit |
| Metrics collection runtime | Exposes runtime metrics snapshots to deterministic flows | Done | `cmd/r1-mcp/metrics_runtime.go` |
| Timeout and cancellation hooks | Keeps deterministic runtime calls bounded and cancellation-aware | Done | `cmd/r1-mcp/backends.go` |
| Oneshot runtime cost metadata | Makes runtime cost visible to callers and operators | Done | April 30 main-branch oneshot cost metadata commit |

## Agentic Test Harness

| Feature | Benefit | Status | Reference |
|---|---|---|---|
| `r1.*` MCP catalog (38 tools across 10 categories) | One namespace; every UI action reachable through MCP | Done (catalog only; back-end pending specs 1-7) | `internal/mcp/r1_server_catalog.go` |
| Slack-style envelope + stokerr/ taxonomy | Predictable wire shape; no raw Go errors leak | Done | `internal/mcp/envelope.go`, `internal/mcp/stokerr_map.go` |
| `r1 mcp serve --print-tools [--markdown]` | Lint + docs generator have a stable input | Done | `cmd/r1/mcp.go` |
| `internal/tui/teatest_shim.go` | Bubble Tea drivable through MCP without a terminal emulator | Done (in-process driver; teatest swap pending dep) | `internal/tui/teatest_shim.go` |
| `A11yEmitter` + JSONPath evaluator | Synthetic a11y trees + structural assertions | Done | `internal/tui/a11y.go`, `internal/tui/jsonpath.go` |
| `*.agent.feature.md` parser + dispatcher | Gherkin-shaped tests dispatched to MCP catalog | Done | `tools/agent-feature-runner/` |
| 8 seed feature fixtures across all 10 categories | Coverage gate per spec 8 §10 | Done | `tests/agent/{tui,web,cli,mission,worktree}/` |
| `lint-view-without-api` + allowlist | UI without API is a build break | Done (Go scanner active; React + Tauri scanners blocked on specs 6/7 merge) | `tools/lint-view-without-api/` |
| `make agent-features[-update,-drift-check]`, `make lint-views`, `make docs-agentic`, `make storybook-mcp-validate` | One-line CI/local recipes | Done | `Makefile` |
| `docs/AGENTIC-API.md` + D-A1..D-A5 acceptance | External-agent contract + decisions log | Done | `docs/AGENTIC-API.md`, `docs/decisions/index.md` |

## Status

### Done

- governed mission runtime
- deterministic skill substrate
- full pack lifecycle including signing, verification, and HTTP serving
- runtime metrics/audit/timeout/cancel/cost helper surfaces
- agentic test harness wire surface (38 r1.* tools, parser/dispatcher,
  TUI shim, lint scanner, 8 seed fixtures, AGENTIC-API.md, D-A1..D-A5)

### In Progress

- broader runtime-wide adoption of deterministic skills
- agentic test harness back-end wiring (depends on specs 1-7 merging
  the cortex/lanes/TUI/r1d/web/desktop sources)

### Scoped

- stronger publishing and release-check loops for pack distribution

### Scoping

- more outward-facing superiority reporting and proof surfaces

### Potential-On Horizon

- cross-product distribution and exchange of governed deterministic
  skills
