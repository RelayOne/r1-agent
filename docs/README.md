# Stoke Documentation

## Getting Started

- [Operator Guide](operator-guide.md) — Mode 1 vs 2, pool setup, macOS caveats, troubleshooting
- [Spec](stoke-spec-final.md) — 1,091-line frozen spec with 3 adversarial reviews

## Architecture

### V2 Governance Layer

- [V2 Overview](architecture/v2-overview.md) — Substrate layer, supervisor, consensus loops, harness/stances
- [Ledger](architecture/ledger.md) — Content-addressed graph, 13+ node types, query API
- [Bus](architecture/bus.md) — Durable WAL event bus, hooks, delayed events, causality
- [Supervisor Rules](architecture/supervisor-rules.md) — 30 rules across 10 categories
- [Harness & Stances](architecture/harness-stances.md) — 11 stance templates, tool authorization

### Execution Engine

- [Providers](architecture/providers.md) — 5-provider fallback chain, task-type routing
- [Environment Backends](architecture/env-backends.md) — InProc, Docker, SSH, Fly, Ember
- [Policy Engine](architecture/policy-engine.md) — 11-layer enforcement, negative tests
- [Failure Recovery](architecture/failure-recovery.md) — 10 failure classes, retry strategy

### Integration

- [Bridge Adapters](architecture/bridge.md) — V1-to-V2 cost, verify, wisdom, audit
- [OAuth Usage Endpoint](architecture/oauth-usage-endpoint.md) — Undocumented API, contract test

### Existing

- [Agent Loop](architecture/agentloop.md)
- [Hub](architecture/hub.md)
- [Integrations](architecture/integrations.md)
- [Skill Pipeline](architecture/skill-pipeline.md)
- [Wizard](architecture/wizard.md)

## Benchmarking

- [Bench Corpus Format](bench-corpus-format.md) — Task schema, directory structure, judge pipeline
- [SWE-bench Evaluation](bench-swebench.md) — Evaluation path, expected results

## Historical Context

- [History](history/README.md) — Design documents and working notes from development

## Roadmap

- [Roadmap](ROADMAP.md) — Product stack, Ember integration status, open source strategy

## Harness Architecture

- [Harness Architecture](harness-architecture.md) — Detailed harness design
