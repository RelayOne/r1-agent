# Stoke Roadmap

## Product Stack

```
Stoke (open source)     AI coding orchestrator. Works everywhere.
Ember (SaaS)            Cloud dev machines. Where Stoke runs best.
Flare (infrastructure)  Firecracker microVMs on GCP. Powers both.
```

## Current State (v1)

Stoke is a Go binary that wraps Claude Code and Codex CLI as execution engines.
Deterministic PLAN -> EXECUTE -> VERIFY -> COMMIT phases with multi-model routing,
parallel agent coordination via git worktrees, and structured quality gates.

- 66 Go files, ~14K lines, 206 tests
- Zero data races (verified with -race)
- Single-account and multi-pool modes both work
- Apache 2.0 license

## Ember Integration (planned)

### Phase 1: Config + Managed AI

`~/.stoke/config.yaml`:
```yaml
ember:
  key: "ek_live_..."         # from ember.dev/settings/api-keys
  managed_models:
    enabled: true             # fallback to OpenRouter when no user sub
```

Implementation:
- `internal/managed/proxy.go` - routes through Ember /v1/ai/chat API
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

Implementation:
- `internal/compute/ember.go` - spawns Flare VMs via Ember /v1/workers API
- Workers appear on Ember dashboard grouped under parent machine
- Auto-destroy on task completion
- Scheduler decides local vs burst based on estimated task duration

### Phase 3: Remote Progress

- `internal/remote/session.go` - pushes progress to Ember /v1/sessions API
- Live progress at ember.dev/s/<session_id>
- Shareable URL for team visibility
- TUI prints link: "Web: ember.dev/s/abc123 (optional)"

### Phase 4: Chat Sidebar

The Ember desktop app / VS Code extension wraps Stoke's plan generation
in a conversational interface. The LLM only does PLANNING. Stoke's phase
machine does execution and verification.

Flow: user message -> planning model generates Stoke plan JSON -> sidebar
renders as interactive UI -> user approves -> Stoke executes -> progress
streams back -> results shown as diffs with approve/reject.

## Open Source Strategy

- License: Apache 2.0
- Stoke works fully standalone (no Ember required)
- `compute/backend.go` interface is open (anyone can write backends)
- `compute/ember.go` implementation is also open (just an HTTP client)
- The value is the Ember API behind the key, not the client code

## File Structure

```
internal/
  compute/
    backend.go         Interface (Backend, Worker, SpawnOpts)
    local.go           Goroutine backend (current behavior, always works)
    ember.go           Flare VM backend (requires Ember API key)
  managed/
    proxy.go           OpenRouter proxy via Ember API
  remote/
    session.go         Push progress to Ember dashboard
```
