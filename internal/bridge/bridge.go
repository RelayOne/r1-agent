// Package bridge wires v1 runtime components into the v2 bus and ledger.
package bridge

import "github.com/RelayOne/r1/internal/bus"

// Bridge event types for v1 component integration.
//
// Only events that are actually published by a bridge adapter are
// declared here. Declaring an unwired event creates a "live adapter"
// appearance (callers grep, find the constant, assume it fires)
// without an actual emitter — surfaced as
// audit/scan-governance-gaps.md item #7. When a workflow / hook /
// skill / profile-detection bridge lands, add the constant alongside
// the publisher in the same change so the contract stays honest.
const (
	EvtCostRecorded     bus.EventType = "cost.recorded"
	EvtBudgetAlert      bus.EventType = "cost.budget.alert"
	EvtVerifyStarted    bus.EventType = "verify.started"
	EvtVerifyCompleted  bus.EventType = "verify.completed"
	EvtLearningRecorded bus.EventType = "wisdom.learning.recorded"
	EvtAuditCompleted   bus.EventType = "audit.completed"
)
