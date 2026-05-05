# Architecture

This is the trunk architecture view for R1.

## Audience

- engineers maintaining the runtime
- reviewers checking whether docs match the current deterministic-skill
  and registry work
- stakeholders who need the system shape without reading every package

## Core System Planes

R1 currently has five architectural planes that matter together:

1. Mission execution: planning, execution, verification, review
2. Governance and evidence: ledger, WAL, receipts, honesty, cost
3. Deterministic skills: compile, manufacture, register, select, run
4. Distribution and runtime extension: packs, registries, MCP-backed
   runtime functions
5. Anti-truncation enforcement: regex catalog, scope-completion gate,
   supervisor rules, agentloop wiring, post-commit git hook, and
   `r1 antitrunc verify` CLI / MCP tool — a layered, machine-
   mechanical defense against LLM self-reduction. Each layer is
   independently effective so the model cannot side-step one and
   pass.

## Execution Core

The execution core still centers on the orchestrator packages:

- `app/`, `workflow/`, `mission/`
- `engine/`, `agentloop/`
- `verify/`, `critic/`, `convergence/`
- `scheduler/`, `plan/`, `taskstate/`

That is the original runtime thesis: one strong implementer, explicit
verification, and adversarial review instead of loose multi-agent
consensus.

## Evidence Core

The evidence plane gives R1 its governance posture:

- content-addressed ledger
- WAL-backed event surfaces
- receipts and honesty artifacts
- cost and replay evidence

This is why new runtime features keep adding audit and metrics hooks
instead of only new prompts.

## Deterministic Skill Plane

The deterministic skill lane now spans more than compilation:

- manufacturing and manifest enforcement
- registry and selection
- seeded repo/user pack libraries
- signed pack authoring and verification
- runtime registration and verification hooks

The important architectural shift on April 30 is that pack distribution
is now a real subsystem, not just a future direction.

## Anti-Truncation Plane

The anti-truncation plane addresses a documented LLM behaviour: under
long-running multi-task work the model self-reduces scope to fit
imagined token / time / Anthropic load-balance budgets. The plane is
seven layers, each independently effective:

- regex catalog — `internal/antitrunc/phrases.go`
- scope-completion gate — `internal/antitrunc/gate.go`
- cortex Lobe (Detector) — `internal/cortex/lobes/antitrunc/`
- supervisor rules — `internal/supervisor/rules/antitrunc/`
- agentloop wiring — `internal/agentloop/antitrunc.go`
- post-commit git hook — `scripts/git-hooks/post-commit-antitrunc.sh`
- CLI + MCP tool — `cmd/r1/antitrunc_cmd.go`,
  `internal/mcp/r1_server.go`

The gate composes BEFORE any other end-turn hook, so a model that
says "skip the gate this once" is ignored at the host process layer.
Operator override (`--no-antitrunc-enforce`) is real but has no
LLM-visible toggle. Full details: [`ANTI-TRUNCATION.md`](ANTI-TRUNCATION.md).

## Runtime Extension Plane

`cmd/r1-mcp/backends.go` is now a practical bridge between the core
runtime and deterministic helpers:

- metrics collection runtime
- skill execution audit runtime
- ledger audit query runtime
- timeout and cancellation-aware runtime wrappers
- oneshot runtime cost metadata
- flagship runtimes and pack-registry-backed behavior

These let deterministic workflows observe and prove more about their own
execution.

## Status

### Done

- mission runtime and verification core
- evidence and governance plane
- deterministic skill and pack-registry foundations
- runtime audit/metrics/cancel/timeout extension surfaces

### In Progress

- wider product adoption of the deterministic skill lane

### Scoped

- stronger distribution, publishing, and release checks for pack flows

### Scoping

- broader superiority reporting against peer runtimes

### Potential-On Horizon

- portfolio-wide exchange of deterministic skills and governed runtime
  assets
