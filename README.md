# R1

R1 is a governed agent runtime for software work. It runs strong coding
agents through a plan, execute, verify, and review loop, records what
happened into a durable evidence model, and is increasingly opinionated
about turning repeatable workflows into deterministic, inspectable skill
artifacts.

`r1` is the CLI name and the primary entrypoint under `cmd/r1`.

## What Ships On `main`

- core mission loop with planning, execution, verification, and review
- content-addressed ledger and WAL-backed runtime evidence
- benchmark and parity evidence under `evaluation/`
- deterministic skill manufacturing, registry, and selection surfaces
- skill-pack lifecycle commands including `init`, `info`, `install`,
  `list`, `publish`, `search`, `sign`, `verify`, `update`, and `serve`
- seeded repo/user skill-pack registries and signed-pack runtime
  verification
- new runtime helpers for ledger audit, execution audit, metrics
  collection, timeout/cancel behavior, oneshot runtime cost metadata,
  and flagship deterministic runtimes
- **anti-truncation enforcement** — a layered, machine-mechanical
  defense against LLM self-truncation. Refuses end-turn while plan
  items are unchecked or truncation phrases are emitted. See
  [`docs/ANTI-TRUNCATION.md`](docs/ANTI-TRUNCATION.md).

## What Changed In The Last 30 Days

The most important main-branch change is that deterministic skills
stopped being only an authoring/compiler story and became a real
distribution and runtime story:

- packs can be created, searched, published, signed, verified, updated,
  and served over HTTP
- installed signed packs can be verified at runtime
- `stoke-mcp` gained runtime functions for metrics, audit, timeout, and
  cancellation-aware behavior
- the repo and user pack libraries are now first-class runtime inputs

## Where To Start

- Architecture: [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md)
- Workflow narrative: [`docs/HOW-IT-WORKS.md`](docs/HOW-IT-WORKS.md)
- Feature inventory: [`docs/FEATURE-MAP.md`](docs/FEATURE-MAP.md)
- Deployment posture: [`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md)
- Commercial framing: [`docs/BUSINESS-VALUE.md`](docs/BUSINESS-VALUE.md)
- Main evaluation artifact:
  [`evaluation/r1-vs-reference-runtimes-matrix.md`](evaluation/r1-vs-reference-runtimes-matrix.md)

## Status

### Done

- governed plan/execute/verify runtime
- parity evidence and deterministic skill substrate
- signed skill-pack lifecycle and HTTP registry surface
- runtime audit, metrics, timeout/cancel, and cost metadata helpers

### In Progress

- parity-to-superiority execution and broader deterministic-skill
  adoption across more product surfaces

### Scoped

- more productized pack distribution and publishing loops
- stronger release checks around skill packaging and dependency
  validation

### Scoping

- broader outward-facing superiority reporting

### Potential-On Horizon

- cross-product deterministic skill exchange and marketplace dynamics
