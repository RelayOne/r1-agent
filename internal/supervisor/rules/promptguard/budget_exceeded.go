// Package promptguard implements the supervisor's promptguard-budget
// rule. Spec specs/promptguard-hardening.md §T5 item 21.
//
// The rule consumes promptguard.budget.exceeded events and dispatches
// a daemon.session.kill command. Registered in both
// internal/supervisor/manifests/branch.go and mission.go so it fires
// regardless of supervisor tier.

package promptguard

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/ledger"
)

// budgetExceededPayload is the wire shape published by
// internal/promptguard.Emit when the budget trips.
type budgetExceededPayload struct {
	SessionID  string `json:"session_id"`
	Threshold  int    `json:"threshold"`
	Detections int    `json:"detections"`
}

// BudgetExceeded is the supervisor.Rule implementation. It evaluates
// every promptguard.budget.exceeded event and emits a
// daemon.session.kill action carrying the session ID and a
// human-readable reason.
type BudgetExceeded struct{}

// NewBudgetExceeded returns the rule for manifest registration.
func NewBudgetExceeded() *BudgetExceeded { return &BudgetExceeded{} }

// Name returns the canonical rule identifier surfaced in operator
// audit views.
func (r *BudgetExceeded) Name() string { return "promptguard.budget_exceeded" }

// Pattern is the bus filter the supervisor core walks against
// incoming events. Matches the exact event type Emit publishes.
func (r *BudgetExceeded) Pattern() bus.Pattern {
	return bus.Pattern{TypePrefix: "promptguard.budget.exceeded"}
}

// Priority places the rule above ordinary drift rules (which sit at
// 80) so a session-kill dispatch is not delayed by lower-priority
// observers. The spec mandates kill-latency ≤100ms.
func (r *BudgetExceeded) Priority() int { return 95 }

// Rationale is rendered into ledger audit logs alongside every rule
// firing.
func (r *BudgetExceeded) Rationale() string {
	return "Sustained injection pressure indicates an adversarial environment; killing the session bounds blast radius."
}

// Evaluate returns true when the payload is a well-formed
// promptguard.budget.exceeded event with a non-empty SessionID. A
// malformed payload returns (false, error) so the supervisor's
// caller-side logging can surface the protocol bug.
func (r *BudgetExceeded) Evaluate(_ context.Context, evt bus.Event, _ *ledger.Ledger) (bool, error) {
	var p budgetExceededPayload
	if err := json.Unmarshal(evt.Payload, &p); err != nil {
		return false, fmt.Errorf("unmarshal budget payload: %w", err)
	}
	if p.SessionID == "" {
		return false, nil
	}
	return true, nil
}

// Action publishes the daemon.session.kill event. Sessionhub
// subscribers (see internal/server/sessionhub/promptguard_kill.go)
// consume the event and call SessionHub.Delete.
func (r *BudgetExceeded) Action(_ context.Context, evt bus.Event, b *bus.Bus) error {
	var p budgetExceededPayload
	if err := json.Unmarshal(evt.Payload, &p); err != nil {
		return fmt.Errorf("unmarshal budget payload: %w", err)
	}
	if p.SessionID == "" {
		return nil
	}
	killPayload, _ := json.Marshal(map[string]any{
		"session_id": p.SessionID,
		"reason": fmt.Sprintf("promptguard budget exceeded (%d>=%d)",
			p.Detections, p.Threshold),
		"source": "promptguard.budget_exceeded",
	})
	return b.Publish(bus.Event{
		Type:      "daemon.session.kill",
		Scope:     evt.Scope,
		Payload:   killPayload,
		CausalRef: evt.ID,
	})
}
