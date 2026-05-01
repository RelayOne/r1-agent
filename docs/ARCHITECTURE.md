# R1 Architecture

This document describes the current product architecture as represented in `/home/eric/repos/r1-agent` on 2026-05-01.

## Architectural Thesis

R1 runs software work through a plan, execute, verify, and review loop, records durable evidence for the run, and turns repeatable workflows into deterministic skill packs that can be inspected, signed, and distributed.

## Core Notes

- Go runtime focused on governed software execution rather than general chat UX.
- Evidence and deterministic skills are distinct architectural planes beside the mission loop.
- Distribution of governed skill packs is now a live subsystem, not just an aspiration.

## Repo Map

| Path | Role |
|---|---|
| `cmd` | CLI and runtime entry points. |
| `internal` | Daemon, execution, cloud, and governance internals. |
| `docs` | Canonical narrative set. |
| `bench` | Benchmarks and supporting evaluation inputs. |
| `desktop` | Desktop-facing docs and surfaces. |

## Runtime and Product Layers

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

## Operator and Delivery Considerations

- Go test remains the primary validation spine.
- Current checkout has active daemon and rules work plus untracked local artifacts outside this docs commit.
- Deployment narrative should stay focused on governed runtime packaging rather than generic SaaS operations.

## Current State

### Stable

- Mission runtime with planning, execution, verification, and review.
- Ledger and evidence model.
- Deterministic skill-pack lifecycle and registry surfaces.

### Moving

- Daemon and runtime policy work are active in the current checkout.
- Broader portfolio adoption of deterministic skills is still unfolding.

### Likely Next

- Wider skill distribution, stronger release checks, and portfolio exchange of governed workflows.
- Sharper superiority reporting against peer coding-agent runtimes.

---

Last updated: 2026-05-01
