// Package bridge wires v1 runtime components into the v2 bus and ledger.
package bridge

import "github.com/RelayOne/r1/internal/bus"

// Bridge event types for v1 component integration.
const (
	EvtCostRecorded     bus.EventType = "cost.recorded"
	EvtBudgetAlert      bus.EventType = "cost.budget.alert"
	EvtVerifyStarted    bus.EventType = "verify.started"
	EvtVerifyCompleted  bus.EventType = "verify.completed"
	EvtLearningRecorded bus.EventType = "wisdom.learning.recorded"
	EvtPhaseStarted     bus.EventType = "workflow.phase.started"
	EvtPhaseCompleted   bus.EventType = "workflow.phase.completed"
	EvtTaskCompleted    bus.EventType = "workflow.task.completed"
	EvtAuditStarted     bus.EventType = "audit.started"
	EvtAuditCompleted   bus.EventType = "audit.completed"
	EvtHookDecision     bus.EventType = "hook.decision"
	EvtSkillInjected    bus.EventType = "skill.injected"
	EvtProfileDetected  bus.EventType = "profile.detected"
)
