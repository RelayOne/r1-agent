# Feature Map

This is the current main-branch feature inventory for R1.

## Mission Runtime

| Feature | Benefit | Status | Reference |
|---|---|---|---|
| Plan/execute/verify workflow | Keeps agent output tied to explicit execution and verification gates | Done | `app/`, `workflow/`, `verify/` |
| Adversarial review posture | Favors reviewable output over ungoverned autonomous edits | Done | `critic/`, `convergence/`, `engine/` |
| Evidence model | Persists what happened instead of trusting session memory | Done | `ledger/`, `bus/`, `session/` |

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
| Metrics collection runtime | Exposes runtime metrics snapshots to deterministic flows | Done | `cmd/stoke-mcp/metrics_runtime.go` |
| Timeout and cancellation hooks | Keeps deterministic runtime calls bounded and cancellation-aware | Done | `cmd/stoke-mcp/backends.go` |
| Oneshot runtime cost metadata | Makes runtime cost visible to callers and operators | Done | April 30 main-branch oneshot cost metadata commit |

## Status

### Done

- governed mission runtime
- deterministic skill substrate
- full pack lifecycle including signing, verification, and HTTP serving
- runtime metrics/audit/timeout/cancel/cost helper surfaces

### In Progress

- broader runtime-wide adoption of deterministic skills

### Scoped

- stronger publishing and release-check loops for pack distribution

### Scoping

- more outward-facing superiority reporting and proof surfaces

### Potential-On Horizon

- cross-product distribution and exchange of governed deterministic
  skills
