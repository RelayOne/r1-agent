// Package hierarchy implements supervisor rules that enforce the parent-child
// relationship between mission and branch supervisors.
package hierarchy

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ericmacdougall/stoke/internal/bus"
	"github.com/ericmacdougall/stoke/internal/ledger"
	"github.com/ericmacdougall/stoke/internal/schemaval"
)

// CompletionRequiresParentAgreement evaluates branch completion proposals
// at the mission level and commits an agree or dissent node.
type CompletionRequiresParentAgreement struct{}

// NewCompletionRequiresParentAgreement returns a new rule instance.
func NewCompletionRequiresParentAgreement() *CompletionRequiresParentAgreement {
	return &CompletionRequiresParentAgreement{}
}

// Name returns the stable rule identifier used by the supervisor
// registry and audit logs.
func (r *CompletionRequiresParentAgreement) Name() string {
	return "hierarchy.completion_requires_parent_agreement"
}

// Pattern subscribes to branch-completion proposals so the mission
// supervisor can weigh in on every branch's declared completion.
func (r *CompletionRequiresParentAgreement) Pattern() bus.Pattern {
	return bus.Pattern{TypePrefix: "supervisor.branch.completion.proposed"}
}

// Priority (100) is the highest in the hierarchy category; the parent
// decision must resolve before any dependent rules fire.
func (r *CompletionRequiresParentAgreement) Priority() int { return 100 }

// Rationale is the human-readable justification surfaced in audit.
func (r *CompletionRequiresParentAgreement) Rationale() string {
	return "Branch completion must be approved at the mission level to maintain hierarchy integrity."
}

// branchCompletionPayload is the expected structure inside a branch completion proposal.
type branchCompletionPayload struct {
	BranchID    string `json:"branch_id"`
	ProposerID  string `json:"proposer_id"`
	Summary     string `json:"summary"`
	TasksTotal  int    `json:"tasks_total"`
	TasksDone   int    `json:"tasks_done"`
	VerifyPass  bool   `json:"verify_pass"`
}

// Evaluate always returns true: the mission supervisor must weigh in
// on every branch-completion proposal. The agree/dissent decision is
// made in Action based on the payload.
func (r *CompletionRequiresParentAgreement) Evaluate(_ context.Context, _ bus.Event, _ *ledger.Ledger) (bool, error) {
	// Always evaluate -- the mission supervisor must weigh in on every
	// branch completion proposal.
	return true, nil
}

// Action inspects the completion payload and publishes either an
// agree or a dissent supervisor.branch.completion.decided event. The
// decision is agree iff all declared tasks are done and verification
// passed; otherwise a dissent with a specific reason is emitted.
func (r *CompletionRequiresParentAgreement) Action(ctx context.Context, evt bus.Event, b *bus.Bus) error {
	var bp branchCompletionPayload
	if err := json.Unmarshal(evt.Payload, &bp); err != nil {
		return fmt.Errorf("unmarshal branch completion payload: %w", err)
	}

	// Determine agreement or dissent based on mission state.
	allDone := bp.TasksDone >= bp.TasksTotal && bp.TasksTotal > 0
	verified := bp.VerifyPass

	if allDone && verified {
		// Agree.
		agreePayload, _ := json.Marshal(map[string]any{
			"branch_id":  bp.BranchID,
			"decision":   "agree",
			"reason":     "all tasks completed and verification passed",
			"decided_at": time.Now().UTC().Format(time.RFC3339),
		})
		return b.Publish(bus.Event{
			Type:      "supervisor.branch.completion.decided",
			Scope:     evt.Scope,
			Payload:   agreePayload,
			CausalRef: evt.ID,
		})
	}

	// Dissent.
	reason := "branch completion rejected"
	if !allDone {
		reason = fmt.Sprintf("not all tasks done (%d/%d)", bp.TasksDone, bp.TasksTotal)
	} else if !verified {
		reason = "verification did not pass"
	}

	dissentPayload, _ := json.Marshal(map[string]any{
		"branch_id":  bp.BranchID,
		"decision":   "dissent",
		"reason":     reason,
		"decided_at": time.Now().UTC().Format(time.RFC3339),
	})
	return b.Publish(bus.Event{
		Type:      "supervisor.branch.completion.decided",
		Scope:     evt.Scope,
		Payload:   dissentPayload,
		CausalRef: evt.ID,
	})
}
// PayloadSchema returns nil — this rule emits supervisor.branch.completion.decided — unique shape,
// for which no shared schema exists in internal/supervisor/schemas.go
// yet. Equivalent to not implementing PayloadSchemaProvider.
// Tightening pass: add the specific schema + wire ValidatePayload.
func (r *CompletionRequiresParentAgreement) PayloadSchema() *schemaval.Schema {
	return nil
}
