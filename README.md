# R1

**A governed coding-agent runtime built to prove what happened, not just produce an answer.**

R1 runs software work through a plan, execute, verify, and review loop, records durable evidence for the run, and turns repeatable workflows into deterministic skill packs that can be inspected, signed, and distributed.

## Why R1

Coding agents are useful, but most of them still leave too little evidence behind and make it too hard to repeat the same workflow safely across teams. Developers, platform teams, and organizations that want coding agents with stronger governance and repeatability.

## Key Benefits

- Govern the whole mission: planning, execution, verification, and review are explicit runtime phases.
- Keep receipts: ledger, WAL, and runtime evidence are core design elements.
- Productize repetition: deterministic skill packs let teams standardize proven workflows.
- Distribute safely: packs can be signed, verified, served, and installed instead of copied ad hoc.

## Quick Start

```bash
go test ./...
make test || true
```

## How It Works

1. Accept a mission and force it through planning before execution starts.
2. Run the work while emitting evidence, receipts, and governance artifacts.
3. Verify and review the result with explicit runtime helpers and audit surfaces.
4. Package repeatable workflows into deterministic skills and distribute them through registries.

## Features

### Governed Runtime
- Plan, execute, verify, and review mission loop.
- Evidence-first execution with ledger and WAL support.

### Deterministic Skills
- Skill manufacturing, registry, selection, and runtime verification.
- Pack lifecycle commands for install, publish, sign, verify, update, and serve.

### Runtime Extensions
- Metrics, audit, cancellation, and timeout-aware helpers.
- MCP-backed runtime surfaces for governed automation.

### Commercial Story
- Provable software work and repeatable governed execution.
- Positioning against weaker black-box coding agents.

## Project Status

### Shipped

- Mission runtime with planning, execution, verification, and review.
- Ledger and evidence model.
- Deterministic skill-pack lifecycle and registry surfaces.

### In Progress

- Daemon and runtime policy work are active in the current checkout.
- Broader portfolio adoption of deterministic skills is still unfolding.

### Coming Next

- Wider skill distribution, stronger release checks, and portfolio exchange of governed workflows.
- Sharper superiority reporting against peer coding-agent runtimes.

## Documentation

| Document | Purpose |
|---|---|
| [`README.md`](docs/README.md) | Launch-page narrative for the product. |
| [`ARCHITECTURE.md`](docs/ARCHITECTURE.md) | Codebase shape, runtime layers, and key subsystems. |
| [`HOW-IT-WORKS.md`](docs/HOW-IT-WORKS.md) | User and system flow from input to value. |
| [`FEATURE-MAP.md`](docs/FEATURE-MAP.md) | Shipped capabilities grouped by outcome. |
| [`DEPLOYMENT.md`](docs/DEPLOYMENT.md) | Local, staging, and production deployment posture. |
| [`BUSINESS-VALUE.md`](docs/BUSINESS-VALUE.md) | Buyer narrative, differentiation, and commercial framing. |


## Development

```bash
go test ./...
make test || true
```

---

Last updated: 2026-05-01
