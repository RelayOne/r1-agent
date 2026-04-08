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

## Ember Integration

### Phase 1: Config + Managed AI — Implemented

`internal/env/ember/ember.go` (458 LOC) + `internal/env/ember/ai.go` (197 LOC)

- Routes through Ember /v1/ai/chat API via `internal/provider/ember.go` (161 LOC)
- OpenRouter base rate + 20% markup, billed to Ember account
- Model routing: user subs first, then user API keys, then managed pool

Configuration in `~/.stoke/config.yaml`:
```yaml
ember:
  key: "ek_live_..."
  managed_models:
    enabled: true
```

### Phase 2: Burst Compute — Implemented

`internal/env/ember/ember.go` includes burst compute integration.

- Spawns Flare VMs via Ember /v1/workers API
- Workers appear on Ember dashboard grouped under parent machine
- Auto-destroy on task completion
- Scheduler decides local vs burst based on estimated task duration

Configuration:
```yaml
ember:
  burst:
    enabled: true
    threshold_minutes: 5
    worker_size: "4x"
    max_workers: 8
    auto_destroy: true
```

### Phase 3: Remote Progress — Implemented

`internal/remote/session.go` (170 LOC)

- Pushes progress to Ember /v1/sessions API
- Live progress at ember.dev/s/<session_id>
- Shareable URL for team visibility

### Phase 4: Chat Sidebar — Not in scope for Stoke

The Ember desktop app / VS Code extension wraps Stoke's plan generation
in a conversational interface. This is an Ember-side workstream. The Stoke
side already exports the surfaces it needs via the MCP server and HTTP API.

## Flare Integration

Stoke talks to the Fly-compatible REST API via `internal/env/fly/` which
works against Flare unchanged. No additional Stoke work is needed for
Flare compute backends.

## Open Source Strategy

- License: MIT
- Stoke works fully standalone (no Ember required)
- Compute backend interface is extensible (inproc, docker, ssh, fly, ember)
- The value is the Ember API behind the key, not the client code
