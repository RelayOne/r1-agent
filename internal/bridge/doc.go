// Package bridge wires v1 runtime components into the v2 bus and ledger.
//
// Each bridge adapter wraps a v1 component, publishes bus events for
// observability, and writes ledger nodes for persistence. Events land on
// the durable governance bus (inspectable via the cmd/r1 eventlog / ops
// commands, and available for supervisor rules to pattern-match); ledger
// nodes populate the governance graph.
//
// Wiring status (audit A037/A055): VerifyBridge and WisdomBridge are
// constructed by the app orchestrator on governed runs
// (internal/app.Orchestrator.Run); CostBridge usage flows through the
// governance Governor's cost path (internal/governance.onCost).
// AuditBridge has no default production construction site yet — callers
// that produce audit.AuditReports construct it explicitly.
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
// # Future bridges (intentionally not declared yet)
//
// Workflow phase / task, hook decision, skill injection, and profile
// detection bridges are NOT shipped here. The constants for those
// events live alongside the publisher in whichever package eventually
// adds them — declaring them here without an emitter creates a "live
// adapter" appearance with no actual events on the bus. Per
// audit/scan-governance-gaps.md item #7.
package bridge
