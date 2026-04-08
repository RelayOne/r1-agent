// Package bridge wires v1 runtime components into the v2 bus and ledger.
//
// Each bridge adapter wraps a v1 component, publishes bus events for
// observability, and writes ledger nodes for persistence. The supervisor's
// rules subscribe to these events for governance enforcement.
//
// # CostBridge
//
// Wraps costtrack.Tracker. Emits events and writes ledger nodes on cost changes.
//
//   - Event: "cost.recorded" — payload: costtrack.Usage (model, task_id,
//     input_tokens, output_tokens, cache_read, cache_write, cost, timestamp)
//   - Event: "cost.budget.alert" — payload: costtrack.Alert (emitted by the
//     tracker's alert callback when budget thresholds are crossed)
//   - Ledger node type: "cost_record" — content: same as cost.recorded payload
//
// # VerifyBridge
//
// Wraps verify.Pipeline. Emits start/complete events around verification runs.
//
//   - Event: "verify.started" — payload: {dir, task_id}
//   - Event: "verify.completed" — payload: {outcomes: []verify.Outcome, success: bool}
//   - Ledger node type: "verification" — content: same as verify.completed payload
//
// # WisdomBridge
//
// Wraps wisdom.Store. Emits events when learnings are recorded.
//
//   - Event: "wisdom.learning.recorded" — payload: {task_id, category,
//     description, file?, failure_pattern?}
//   - Ledger node type: "wisdom_learning" — content: same as event payload
//
// # AuditBridge
//
// Records audit reports as bus events and ledger nodes.
//
//   - Event: "audit.completed" — payload: audit.AuditReport
//   - Ledger node type: "audit_report" — content: same as event payload
//   - Edge: "references" from audit_report node to task node (if task exists)
//
// # Additional event types
//
// The bridge package also defines event types used by other bridge adapters
// not yet fully implemented:
//
//   - "workflow.phase.started", "workflow.phase.completed", "workflow.task.completed"
//   - "hook.decision", "skill.injected", "profile.detected"
package bridge
