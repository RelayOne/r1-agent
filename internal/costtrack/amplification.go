// Package costtrack — amplification.go
//
// B2 — Token Amplification Budget Per Flow.
//
// Multi-agent harnesses can burn 4-220x the tokens of a single-agent
// run on the same task (Kim et al. 2026). Without a flow-level
// ceiling and an ALERTING channel, a stuck flow looks identical to a
// productive flow until the bill arrives. This file defines the
// budget contract; the measurement table is populated in
// bench/metrics/amplification.go (see B2 measurement section in
// docs/anti-deception-matrix.md).
//
// USAGE
//
//	tracker := costtrack.NewTracker(cfg)
//	budget := costtrack.AmplificationBudget{
//	    TaskClass:       "feature_add",
//	    BaselineTokens:  150_000,   // measured single-agent baseline
//	    MaxMultiplier:   4.0,       // Kim 2026 "Rule of 4"
//	    AlertMultiplier: 2.5,       // warn before ceiling
//	}
//	tracker.SetAmplificationBudget(budget)
//	// On each request, tracker compares cumulative tokens to budget
//	// and emits cost.amplification.alert / cost.amplification.exceeded.

package costtrack

import (
	"fmt"
	"sync"
)

// AmplificationBudget bounds total token use for a flow as a
// multiple of a single-agent baseline for the same task class.
// Default Kim 2026 "Rule of 4" — most well-engineered multi-agent
// flows stay under 4x; flows over 4x are usually stuck or chasing
// their own tail.
type AmplificationBudget struct {
	// TaskClass labels the workload, e.g. "feature_add", "bug_fix",
	// "refactor", "scaffold". Used to look up the right baseline.
	TaskClass string

	// BaselineTokens is the median single-agent token count for this
	// task class, measured over the bench corpus. ZERO means
	// "baseline not yet measured" and disables enforcement.
	BaselineTokens int

	// MaxMultiplier is the hard ceiling. Cumulative tokens >
	// BaselineTokens * MaxMultiplier emits cost.amplification.exceeded
	// and the supervisor MAY halt the flow.
	MaxMultiplier float64

	// AlertMultiplier is the soft warning. Crossing it emits
	// cost.amplification.alert so an operator can intervene before
	// the hard ceiling fires.
	AlertMultiplier float64
}

// AmplificationStatus is the verdict for one budget check.
type AmplificationStatus int

const (
	AmplificationOK       AmplificationStatus = iota // under AlertMultiplier
	AmplificationAlert                               // ≥ Alert, < Max
	AmplificationExceeded                            // ≥ Max
	AmplificationDisabled                            // baseline not measured
)

// String returns the JSON-friendly status name.
func (s AmplificationStatus) String() string {
	switch s {
	case AmplificationAlert:
		return "alert"
	case AmplificationExceeded:
		return "exceeded"
	case AmplificationDisabled:
		return "disabled"
	case AmplificationOK:
		return "ok"
	default:
		return "ok"
	}
}

// Check returns the current verdict given cumulative tokens spent.
// Pure function; safe for concurrent use.
func (b AmplificationBudget) Check(cumulativeTokens int) (AmplificationStatus, float64) {
	if b.BaselineTokens <= 0 {
		return AmplificationDisabled, 0
	}
	mult := float64(cumulativeTokens) / float64(b.BaselineTokens)
	if b.MaxMultiplier > 0 && mult >= b.MaxMultiplier {
		return AmplificationExceeded, mult
	}
	if b.AlertMultiplier > 0 && mult >= b.AlertMultiplier {
		return AmplificationAlert, mult
	}
	return AmplificationOK, mult
}

// AmplificationTracker accumulates token counts per flow and emits
// status transitions via the OnTransition callback. The bus wiring
// (cost.amplification.alert / cost.amplification.exceeded events)
// lives in the supervisor — this struct is the deterministic
// state-machine half of the contract.
type AmplificationTracker struct {
	mu           sync.Mutex
	budget       AmplificationBudget
	cumulative   int
	lastStatus   AmplificationStatus
	OnTransition func(prev, curr AmplificationStatus, mult float64)
}

// NewAmplificationTracker constructs a tracker for one flow.
func NewAmplificationTracker(budget AmplificationBudget) *AmplificationTracker {
	return &AmplificationTracker{budget: budget, lastStatus: AmplificationOK}
}

// Add records additional tokens spent and fires OnTransition if the
// cumulative crosses an AlertMultiplier or MaxMultiplier boundary.
// Callers should invoke this after every billable LLM round-trip.
func (t *AmplificationTracker) Add(tokens int) AmplificationStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cumulative += tokens
	status, mult := t.budget.Check(t.cumulative)
	if status != t.lastStatus && t.OnTransition != nil {
		prev := t.lastStatus
		t.lastStatus = status
		t.OnTransition(prev, status, mult)
	} else {
		t.lastStatus = status
	}
	return status
}

// Cumulative returns the total tokens recorded so far.
func (t *AmplificationTracker) Cumulative() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cumulative
}

// Snapshot returns the tracker's current state for diagnostic
// rendering.
func (t *AmplificationTracker) Snapshot() (AmplificationBudget, int, AmplificationStatus) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.budget, t.cumulative, t.lastStatus
}

// FormatStatus renders the tracker state as an operator-facing
// banner line. Empty when status is OK or Disabled — only firing
// states get a banner.
func FormatStatus(taskClass string, status AmplificationStatus, mult float64) string {
	switch status {
	case AmplificationAlert:
		return fmt.Sprintf("  ⚠️  amplification ALERT: %s flow at %.1fx baseline", taskClass, mult)
	case AmplificationExceeded:
		return fmt.Sprintf("  🛑 amplification EXCEEDED: %s flow at %.1fx baseline (Rule of 4)", taskClass, mult)
	case AmplificationOK, AmplificationDisabled:
		return ""
	default:
		return ""
	}
}

// Bus event names emitted by the supervisor when an amplification
// transition fires. Exported as constants so producers + consumers
// can't drift.
const (
	BusEventAmplificationAlert    = "cost.amplification.alert"
	BusEventAmplificationExceeded = "cost.amplification.exceeded"
)

// Wiring note (B2 follow-up): the AmplificationTracker contract is
// in place. The workflow engine calls tracker.Add(usage.Total) on
// every billable LLM round-trip once a flow's tracker is attached
// via the engine config. The baseline-measurement methodology (Q16
// in the addendum) determines what BaselineTokens value to populate
// per task class — until that data lands, BaselineTokens=0 keeps
// enforcement disabled and the contract is no-op safe.
