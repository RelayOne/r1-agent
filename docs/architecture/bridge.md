# V1-to-V2 Bridge Adapters

Package: `internal/bridge/`

## Purpose

Bridge adapters translate v1 components (cost tracking, verification, wisdom,
audit) into v2 bus events and ledger nodes. This allows the v1 execution engine
to participate in v2 governance without rewriting.

## Components

### CostBridge

Wraps `internal/costtrack/Tracker` to emit bus events and persist to ledger.

```go
bridge.CostBridge.Record(model, tokens, cost)
  → bus.Publish("cost.recorded", {model, tokens, cost})
  → ledger.AddNode({type: "cost_record", ...})
  → if over budget: bus.Publish("cost.budget.alert", ...)
```

### VerifyBridge

Wraps `internal/verify/` pipeline results.

```go
bridge.VerifyBridge.RecordResult(result)
  → bus.Publish("verify.started", ...)
  → bus.Publish("verify.completed", {passed, findings})
```

### WisdomBridge

Wraps `internal/wisdom/` cross-task learnings.

```go
bridge.WisdomBridge.RecordLearning(pattern, context)
  → bus.Publish("wisdom.learning.recorded", ...)
  → ledger.AddNode({type: "wisdom_record", ...})
```

### AuditBridge

Wraps `internal/audit/` multi-perspective review.

```go
bridge.AuditBridge.RecordAudit(personas, findings)
  → bus.Publish("audit.started", ...)
  → bus.Publish("audit.completed", {findings})
```

## Event Types

| Event | Source | Purpose |
|-------|--------|---------|
| `cost.recorded` | CostBridge | Usage tracking |
| `cost.budget.alert` | CostBridge | Budget threshold crossed |
| `verify.started` | VerifyBridge | Verification lifecycle |
| `verify.completed` | VerifyBridge | Verification results |
| `wisdom.learning.recorded` | WisdomBridge | Cross-task learning |
| `workflow.phase.started` | WorkflowBridge | Phase lifecycle |
| `workflow.phase.completed` | WorkflowBridge | Phase results |
| `workflow.task.completed` | WorkflowBridge | Task completion |
| `audit.started` | AuditBridge | Audit lifecycle |
| `audit.completed` | AuditBridge | Audit findings |
| `hook.decision` | HookBridge | Hook decisions |
| `skill.injected` | SkillBridge | Skill integration |
| `profile.detected` | ProfileBridge | Profile detection |

## Corresponding Ledger Node Types

Each bridge event optionally writes a ledger node for persistent storage:

- `cost.recorded` → `cost_record` node
- `wisdom.learning.recorded` → `wisdom_record` node
- `audit.completed` → `audit_record` node
- `verify.completed` → `verify_record` node
