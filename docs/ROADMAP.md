# Stoke Roadmap

## Product Stack

```
Stoke (open source)     AI coding orchestrator. Works everywhere.
Ember (SaaS)            Cloud dev machines. Where Stoke runs best.
Flare (infrastructure)  Firecracker microVMs on GCP. Powers both.
```

## Current State (v2)

Stoke is a Go binary with two layers:

**V1 Execution Engine** — Wraps Claude Code and Codex CLI as execution engines.
Deterministic PLAN -> EXECUTE -> VERIFY -> COMMIT phases with multi-model routing,
parallel agent coordination via git worktrees, and structured quality gates.

**V2 Governance Layer** — Multi-role consensus architecture:
- Append-only content-addressed ledger (immutable reasoning graph)
- Durable WAL-backed event bus with hooks and causality tracking
- 30 deterministic supervisor rules across 10 categories
- 7-state consensus loops for structured agreement
- 10 stance roles (PO, CTO, QA Lead, etc.) with per-role tool authorization
- Bridge adapters wiring v1 systems into v2 event bus + ledger

- MIT license

## Ember Integration (planned, not yet implemented)

### Phase 1: Config + Managed AI

`~/.stoke/config.yaml`:
```yaml
ember:
  key: "ek_live_..."         # from ember.dev/settings/api-keys
  managed_models:
    enabled: true             # fallback to OpenRouter when no user sub
```

- Routes through Ember /v1/ai/chat API
- OpenRouter base rate + 20% markup, billed to Ember account
- Model routing: user subs first, then user API keys, then managed pool

### Phase 2: Burst Compute

```yaml
ember:
  burst:
    enabled: true
    threshold_minutes: 5      # tasks > 5min estimated get Flare VM
    worker_size: "4x"
    max_workers: 8
    auto_destroy: true
```

- Spawns Flare VMs via Ember /v1/workers API
- Workers appear on Ember dashboard grouped under parent machine
- Auto-destroy on task completion
- Scheduler decides local vs burst based on estimated task duration

### Phase 3: Remote Progress

- Pushes progress to Ember /v1/sessions API via `internal/remote/`
- Live progress at ember.dev/s/<session_id>
- Shareable URL for team visibility

### Phase 4: Chat Sidebar

The Ember desktop app / VS Code extension wraps Stoke's plan generation
in a conversational interface. The LLM only does PLANNING. Stoke's phase
machine does execution and verification.

## Open Source Strategy

- License: MIT
- Stoke works fully standalone (no Ember required)
- Compute backend interface is planned as extensible (anyone can write backends)
- The value is the Ember API behind the key, not the client code
