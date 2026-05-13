// Package promptguard — budget.go
//
// Per-session injection budget. Spec
// specs/promptguard-hardening.md §T5 items 19-22.
//
// When a session accumulates N detections (weighted by severity), it
// is operating in an adversarial environment and continuing to run is
// increasingly unsafe regardless of per-detection disposition. The
// budget tracker increments on every Emit, persists state via the
// bus WAL, and publishes a `promptguard.budget.exceeded` event the
// moment the threshold trips. The supervisor's
// promptguard.budget_exceeded rule consumes that event and dispatches
// a `daemon.session.kill` action.
//
// Severity weights (spec §T5 item 19):
//
//	"low"      0  ignored — does not count
//	"medium"   1  default
//	"high"     2
//	"critical" 3  any single critical detection immediately trips
//
// Persistence: state is written to the bus WAL on every increment so
// the budget survives daemon restart. Key path:
// promptguard.budget.<session_id>. Cleared on session.end.

package promptguard

import (
	"sync"
	"time"
)

// Budget is the per-session detection counter + threshold envelope.
// The zero value is NOT ready for use; obtain Budgets from the
// package-level budget tracker via Increment.
type Budget struct {
	SessionID     string
	Threshold     int           // default 5
	Window        time.Duration // 0 == no window (all detections count)
	Detections    int
	FirstDetected time.Time
	LastDetected  time.Time
}

// DefaultBudgetThreshold matches spec §T5 rollout default (5
// detections). Operators override via cfg.PromptGuard.Budget.MaxDetections.
const DefaultBudgetThreshold = 5

// SeverityWeight maps a ThreatEvent.Severity to the budget weight.
// See spec §T5 item 19. Returns 0 for the "low" path so callers don't
// bother bumping the counter for low-severity hits.
func SeverityWeight(severity string) int {
	switch severity {
	case "low":
		return 0
	case "medium":
		return 1
	case "high":
		return 2
	case "critical":
		return 3
	default:
		return 1
	}
}

// budgetTracker is the package-level shared state. The mutex guards
// the map AND the threshold field so a runtime threshold change is
// race-free with concurrent Increment calls.
var (
	budgetMu        sync.Mutex
	budgetMap       = map[string]*Budget{}
	budgetThreshold = DefaultBudgetThreshold
	// budgetPersist is a test-overrideable seam for the WAL-persist
	// path. The daemon installs a real persister via
	// SetBudgetPersister; tests leave it unset (in-memory only).
	budgetPersist BudgetPersister
)

// BudgetPersister is the narrow contract the bus-WAL persistence path
// implements. Implementations MUST be safe for concurrent callers.
// The daemon installs an implementation via SetBudgetPersister at
// startup so a daemon restart can reconstruct in-flight budgets.
type BudgetPersister interface {
	WriteBudget(b Budget) error
	DeleteBudget(sessionID string) error
}

// SetBudgetThreshold installs `n` as the package-level threshold.
// Returns the previous value so callers can restore it in tests.
// Passing 0 or negative leaves the threshold unchanged and returns
// the current value (operators disabling the budget via config use a
// different code path: cfg.PromptGuard.Budget.MaxDetections=0 is
// honoured by skipping the trip path entirely; see TestBudget*).
func SetBudgetThreshold(n int) int {
	budgetMu.Lock()
	defer budgetMu.Unlock()
	prev := budgetThreshold
	if n > 0 {
		budgetThreshold = n
	}
	return prev
}

// SetBudgetPersister installs the WAL-backed persister. Pass nil to
// detach (tests).
func SetBudgetPersister(p BudgetPersister) {
	budgetMu.Lock()
	defer budgetMu.Unlock()
	budgetPersist = p
}

// ResetBudgetState wipes all in-memory budget entries. Test-only.
func ResetBudgetState() {
	budgetMu.Lock()
	defer budgetMu.Unlock()
	budgetMap = map[string]*Budget{}
	budgetThreshold = DefaultBudgetThreshold
}

// IncrementBudget bumps the counter for sessionID by SeverityWeight
// (severity). Returns (exceeded, snapshot). When `exceeded` is true
// the caller MUST publish a promptguard.budget.exceeded event so the
// supervisor's session-kill rule can dispatch.
//
// A single "critical" detection trips the budget immediately,
// independent of the current counter — matches the spec acceptance
// test TestBudgetIncrement_CriticalTripsImmediately.
//
// Empty sessionID is a no-op: the budget is meaningless without a
// session correlation. Returns (false, Budget{}) so callers do not
// fire a phantom kill action.
func IncrementBudget(sessionID, severity string) (bool, Budget) {
	if sessionID == "" {
		return false, Budget{}
	}
	weight := SeverityWeight(severity)
	if weight == 0 {
		return false, Budget{}
	}

	budgetMu.Lock()
	defer budgetMu.Unlock()

	b, ok := budgetMap[sessionID]
	if !ok {
		b = &Budget{
			SessionID:     sessionID,
			Threshold:     budgetThreshold,
			FirstDetected: time.Now().UTC(),
		}
		budgetMap[sessionID] = b
	}
	b.Detections += weight
	b.LastDetected = time.Now().UTC()
	if b.Threshold == 0 {
		b.Threshold = budgetThreshold
	}

	// Persist (best-effort). A failed write does not block the
	// in-memory increment; we log at the caller-side in Emit so a
	// missing-WAL operator can still see the trip in stderr.
	if budgetPersist != nil {
		_ = budgetPersist.WriteBudget(*b)
	}

	exceeded := false
	if severity == "critical" {
		// Critical detections trip immediately regardless of
		// accumulated count (spec §T5 item 19).
		exceeded = true
	} else if b.Detections >= b.Threshold {
		exceeded = true
	}

	return exceeded, *b
}

// ResetBudgetForSession clears the budget for sessionID. Called on
// session.end events so a long-lived daemon does not retain
// indefinitely-growing budget records.
func ResetBudgetForSession(sessionID string) {
	if sessionID == "" {
		return
	}
	budgetMu.Lock()
	delete(budgetMap, sessionID)
	persist := budgetPersist
	budgetMu.Unlock()
	if persist != nil {
		_ = persist.DeleteBudget(sessionID)
	}
}

// BudgetSnapshot returns a copy of the current Budget for sessionID,
// or a zero-value Budget if no detections have been recorded. Used
// by operator-facing audit views.
func BudgetSnapshot(sessionID string) Budget {
	budgetMu.Lock()
	defer budgetMu.Unlock()
	if b, ok := budgetMap[sessionID]; ok {
		return *b
	}
	return Budget{}
}

// BudgetExceededPayload is the structured payload of the
// promptguard.budget.exceeded event published by Emit when the
// budget trips. Consumers (the supervisor rule) unmarshal this
// payload to populate the daemon.session.kill action.
type BudgetExceededPayload struct {
	SessionID     string    `json:"session_id"`
	Threshold     int       `json:"threshold"`
	Detections    int       `json:"detections"`
	FirstDetected time.Time `json:"first_detected"`
	LastDetected  time.Time `json:"last_detected"`
}

// SnapshotToPayload converts a Budget into the wire payload shape.
func SnapshotToPayload(b Budget) BudgetExceededPayload {
	return BudgetExceededPayload{
		SessionID:     b.SessionID,
		Threshold:     b.Threshold,
		Detections:    b.Detections,
		FirstDetected: b.FirstDetected,
		LastDetected:  b.LastDetected,
	}
}
