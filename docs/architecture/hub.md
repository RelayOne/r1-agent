# Hub Event Bus Architecture

## Overview

The hub is a central event bus that dispatches lifecycle, tool-use, model, prompt, and git events to registered subscribers. Subscribers can observe, transform, or gate (block) events.

## Core Types (`internal/hub/`)

### Bus

Thread-safe event dispatcher with priority-ordered subscriber lists, circuit breakers per subscriber, and audit logging.

```go
bus := hub.New()
bus.Register(sub)           // add subscriber
bus.Emit(ctx, event)        // sync dispatch (gates run, can block)
bus.EmitAsync(event)        // fire-and-forget (observe only)
```

### Subscriber

```go
type Subscriber struct {
    ID       string
    Events   []EventType   // or "*" for wildcard
    Mode     Mode          // ModeObserve, ModeTransform, ModeGateStrict, ModeGatePermissive
    Priority int           // lower = runs first
    Handler  HandlerFunc   // in-process
    Webhook  *WebhookConfig // HTTP webhook (external)
    Script   *ScriptConfig  // CLI script (external)
}
```

### Event Taxonomy (46 events)

| Category | Count | Examples |
|----------|-------|---------|
| Session & Mission | 16 | `session.init`, `mission.converged`, `task.completed` |
| Tool Use | 8 | `tool.pre_use`, `tool.post_use`, `tool.blocked` |
| Model API | 8 | `model.pre_call`, `model.post_call`, `model.cache_hit` |
| Prompt Construction | 6 | `prompt.building`, `prompt.skills_injected` |
| Git Operations | 8 | `git.pre_commit`, `git.merge_conflict` |

### Modes

- **Observe** — receives events, cannot affect outcome
- **Transform** — can modify event payload (e.g., inject skills into prompt)
- **Gate Strict** — returns Allow/Deny; Deny blocks the action
- **Gate Permissive** — logs but doesn't block on Deny

### Built-in Subscribers (`internal/hub/builtin/`)

| Subscriber | Mode | Priority | Purpose |
|-----------|------|----------|---------|
| HonestyGate | gate_strict | 100 | Blocks placeholder code, type suppressions, test removal |
| SecretScanner | gate_strict | 50 | Blocks AWS keys, private keys, API keys, tokens |
| CostTracker | observe | 9000 | Records per-model cost from model.post_call |
| SkillInjector | transform | 200 | Injects skills into prompts |

### External Transport

Subscribers can be HTTP webhooks or CLI scripts (configured in `.stoke/hooks.json`). The bus handles JSON serialization, timeouts, and retries.

### Circuit Breaker

Each subscriber gets an automatic circuit breaker (`hub/circuit.go`). After repeated failures, the subscriber is temporarily disabled to prevent cascading failures.

### Audit

All gate decisions are logged to an in-memory ring buffer (`hub/audit.go`). When `ChainedAudit` is enabled, decisions are appended to a hash-chained tamper-evident log (`hub/audit_chain.go`).

## Wiring

```
app.New()
  → hub.New()
  → hub.LoadConfig(.stoke/hooks.json)
  → bus.ApplyConfig()
  → bus.Register(FileProtectionGate)
  → plugins.Discover() → register plugin hooks
  → pass bus to workflow.Engine.EventBus
  → agentloop.SetEventBus(bus) — tool.pre_use/post_use
```
