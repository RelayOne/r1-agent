// Package workunit implements STOKE-020: the binding struct that
// ties together an A2A Task, a DelegationToken, and an audit
// context so a receiving agent has a single handle that carries
// everything it needs to (a) verify the requester's authority and
// (b) anchor its own work back to the ledger.
//
// Without this binding, an A2A peer sending a task, a delegation
// token, and an audit trail would send three separate objects and
// the receiver would have to stitch them — three chances to drop
// the coupling. WorkUnit makes the binding a single type with one
// lifecycle event per stage (Bind / Accept / Complete), each of
// which writes a node to the ledger.
//
// Scope of this initial implementation: the WorkUnit struct, its
// lifecycle helpers, and validation. The delegation validity check
// (trustplane.Client — stub or RealClient HTTP) and the
// ledger-writer (bridge adapter) are plumbed through interface
// slots so this package has no outbound dependency on either —
// both can be injected by the caller.
package workunit

import (
	"context"
	"fmt"
	"time"
)

// WorkUnit binds an A2A task, a delegation, and an audit anchor
// into a single handle. The receiver of a WorkUnit verifies the
// delegation before accepting the task, re-verifies periodically
// during long runs, and anchors every lifecycle event back to
// both the originating delegation and the audit context.
type WorkUnit struct {
	// ID is the unique identifier for this binding. Typically a
	// content hash over (A2ATaskID, DelegationID, AuditContext,
	// ParentWorkUnitID) so two binds with identical inputs
	// produce the same ID.
	ID string `json:"id"`

	// A2ATaskID identifies the A2A task the unit wraps. Opaque
	// to this package — consumers look it up in internal/a2a/
	// when they need the full task shape.
	A2ATaskID string `json:"a2a_task_id"`

	// DelegationID is the TrustPlane delegation token this unit
	// is executing under. The receiving agent MUST verify this
	// delegation is valid (signature, not expired, not revoked)
	// before Accept.
	DelegationID string `json:"delegation_id"`

	// AuditContext is the audit-trail context ref: typically a
	// ledger node ID the receiver anchors its own events to.
	// Empty is tolerated for unbound work (operator-issued tasks
	// that don't cross an agent boundary), but production A2A
	// flows require it.
	AuditContext string `json:"audit_context,omitempty"`

	// ParentWorkUnitID is the ID of the WorkUnit that spawned
	// this one, if any. Lets auditors walk a chain of delegated
	// work across multiple agents without losing the causal
	// thread.
	ParentWorkUnitID string `json:"parent_work_unit_id,omitempty"`

	// SettlementPolicy is one of:
	//   "rollback-immediately"   — on revocation, abort in-flight
	//                               work; compensating transactions
	//                               run; downstream state reverts.
	//   "complete-then-revoke"   — finish the current atomic step,
	//                               then honor the revocation.
	//   "checkpoint-and-revoke"  — save a resumable checkpoint,
	//                               then honor the revocation so
	//                               the delegator can pick back up
	//                               from a known-good state.
	//
	// Defaults to rollback-immediately (safest) when empty.
	SettlementPolicy string `json:"settlement_policy,omitempty"`

	// Status tracks the unit's lifecycle position.
	Status WorkUnitStatus `json:"status"`

	// CreatedAt + AcceptedAt + CompletedAt are the wall-clock
	// timestamps of each lifecycle transition. Nil CompletedAt
	// means still in-flight; zero AcceptedAt means not yet
	// accepted (Status would be WorkUnitPending).
	CreatedAt   time.Time  `json:"created_at"`
	AcceptedAt  *time.Time `json:"accepted_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// WorkUnitStatus is the lifecycle position of a WorkUnit.
type WorkUnitStatus string

const (
	// WorkUnitPending: Bind() has created the unit but no
	// receiver has accepted it yet. No delegation check has
	// been performed.
	WorkUnitPending WorkUnitStatus = "pending"

	// WorkUnitAccepted: a receiver called Accept(), the
	// delegation check passed, and the receiver is now
	// executing the wrapped A2A task.
	WorkUnitAccepted WorkUnitStatus = "accepted"

	// WorkUnitCompleted: the receiver called Complete(). The
	// wrapped A2A task is done and the result is available.
	WorkUnitCompleted WorkUnitStatus = "completed"

	// WorkUnitFailed: the receiver called Fail() because the
	// task itself failed (non-revocation reason). Distinguished
	// from Revoked to help operators diagnose root cause.
	WorkUnitFailed WorkUnitStatus = "failed"

	// WorkUnitRevoked: the parent delegation was revoked and
	// the settlement policy was applied. The unit is terminal.
	WorkUnitRevoked WorkUnitStatus = "revoked"
)

// DelegationVerifier is implemented by whatever subsystem can
// answer "is this delegation currently valid for this
// delegatee?". In production this is the internal/delegation/
// wrapper over trustplane.RealClient (hand-written HTTP against
// the vendored OpenAPI spec); in tests it's a mock.
type DelegationVerifier interface {
	VerifyDelegation(ctx context.Context, delegationID, delegateeID string) error
}

// AuditAnchor is implemented by whatever subsystem writes
// lifecycle events to the ledger. Separated via interface so
// this package doesn't import internal/ledger/ directly.
type AuditAnchor interface {
	AnchorWorkUnit(ctx context.Context, unit *WorkUnit, event string) error
}

// Bind creates a new pending WorkUnit. Caller provides the A2A
// task ID, delegation ID, audit context, and optional parent
// unit ID. Returns the unit with Status=WorkUnitPending and
// CreatedAt set; no verification has happened yet.
//
// The returned unit is a VALUE (not a pointer) so the caller can
// freely copy it across goroutines; subsequent lifecycle calls
// return the updated unit.
func Bind(a2aTaskID, delegationID, auditContext, parentUnitID string) (*WorkUnit, error) {
	if a2aTaskID == "" {
		return nil, fmt.Errorf("workunit: a2a_task_id is required")
	}
	if delegationID == "" {
		return nil, fmt.Errorf("workunit: delegation_id is required")
	}
	u := &WorkUnit{
		A2ATaskID:        a2aTaskID,
		DelegationID:     delegationID,
		AuditContext:     auditContext,
		ParentWorkUnitID: parentUnitID,
		SettlementPolicy: "rollback-immediately",
		Status:           WorkUnitPending,
		CreatedAt:        time.Now().UTC(),
	}
	u.ID = u.computeID()
	return u, nil
}

// computeID is a light-weight, deterministic ID derived from the
// identifying fields. Uses a simple string concatenation rather
// than SHA256 so callers can derive the ID client-side without
// importing crypto/sha256; the ledger adapter is free to re-hash
// with SHA256 when it persists the unit.
func (u *WorkUnit) computeID() string {
	return fmt.Sprintf("wu-%s-%s-%s", u.A2ATaskID, u.DelegationID, u.CreatedAt.Format("20060102T150405.999999Z"))
}

// Accept marks the unit as accepted after verifying the
// delegation. Receivers call this before starting execution so
// the ledger has a record that the receiver explicitly took
// responsibility for the work.
//
// If verifier is nil, the delegation check is skipped — caller
// MUST have verified out-of-band (useful for test scaffolds and
// for operator-issued tasks where the delegation is implicit).
// Production A2A paths MUST pass a real verifier.
func (u *WorkUnit) Accept(ctx context.Context, delegateeID string, verifier DelegationVerifier, anchor AuditAnchor) error {
	if u.Status != WorkUnitPending {
		return fmt.Errorf("workunit: cannot accept from status %q", u.Status)
	}
	if verifier != nil {
		if err := verifier.VerifyDelegation(ctx, u.DelegationID, delegateeID); err != nil {
			return fmt.Errorf("workunit: delegation verify failed: %w", err)
		}
	}
	now := time.Now().UTC()
	u.AcceptedAt = &now
	u.Status = WorkUnitAccepted
	if anchor != nil {
		if err := anchor.AnchorWorkUnit(ctx, u, "accepted"); err != nil {
			return fmt.Errorf("workunit: audit anchor failed: %w", err)
		}
	}
	return nil
}

// Reverify re-runs the delegation check mid-execution. For long-
// running WorkUnits this should be called periodically (once per
// minute is a reasonable default) so a revoked delegation is
// noticed before the receiver commits more work against it.
//
// Returns nil on continued validity, a wrapped verifier error
// on failure. The caller is responsible for invoking the
// settlement policy on error — this function doesn't mutate
// u.Status so the caller can choose whether to mark the unit
// Revoked or apply a more nuanced flow.
func (u *WorkUnit) Reverify(ctx context.Context, delegateeID string, verifier DelegationVerifier) error {
	if u.Status != WorkUnitAccepted {
		return fmt.Errorf("workunit: cannot reverify from status %q", u.Status)
	}
	if verifier == nil {
		return nil
	}
	if err := verifier.VerifyDelegation(ctx, u.DelegationID, delegateeID); err != nil {
		return fmt.Errorf("workunit: reverify failed: %w", err)
	}
	return nil
}

// Complete marks the unit as successfully finished. Writes a
// terminal audit event so auditors can close the chain.
func (u *WorkUnit) Complete(ctx context.Context, anchor AuditAnchor) error {
	if u.Status != WorkUnitAccepted {
		return fmt.Errorf("workunit: cannot complete from status %q", u.Status)
	}
	now := time.Now().UTC()
	u.CompletedAt = &now
	u.Status = WorkUnitCompleted
	if anchor != nil {
		if err := anchor.AnchorWorkUnit(ctx, u, "completed"); err != nil {
			return fmt.Errorf("workunit: audit anchor failed: %w", err)
		}
	}
	return nil
}

// Fail marks the unit as failed for a non-revocation reason
// (the wrapped task errored, the receiver crashed, etc.). Writes
// an audit event with the failure reason.
func (u *WorkUnit) Fail(ctx context.Context, reason string, anchor AuditAnchor) error {
	if u.Status != WorkUnitAccepted && u.Status != WorkUnitPending {
		return fmt.Errorf("workunit: cannot fail from status %q", u.Status)
	}
	now := time.Now().UTC()
	u.CompletedAt = &now
	u.Status = WorkUnitFailed
	if anchor != nil {
		if err := anchor.AnchorWorkUnit(ctx, u, "failed: "+reason); err != nil {
			return fmt.Errorf("workunit: audit anchor failed: %w", err)
		}
	}
	return nil
}

// Revoke marks the unit as terminated due to delegation
// revocation. The caller is responsible for applying the
// SettlementPolicy (rollback / complete-then-revoke /
// checkpoint-and-revoke) — Revoke() just records the terminal
// state.
func (u *WorkUnit) Revoke(ctx context.Context, anchor AuditAnchor) error {
	if u.Status == WorkUnitCompleted || u.Status == WorkUnitFailed || u.Status == WorkUnitRevoked {
		return nil // idempotent: revoking a terminal unit is a no-op
	}
	now := time.Now().UTC()
	u.CompletedAt = &now
	u.Status = WorkUnitRevoked
	if anchor != nil {
		if err := anchor.AnchorWorkUnit(ctx, u, "revoked"); err != nil {
			return fmt.Errorf("workunit: audit anchor failed: %w", err)
		}
	}
	return nil
}

// IsTerminal reports whether the unit is in a terminal status
// that won't change further.
func (u *WorkUnit) IsTerminal() bool {
	switch u.Status {
	case WorkUnitCompleted, WorkUnitFailed, WorkUnitRevoked:
		return true
	default:
		return false
	}
}

// Validate runs invariant checks on the unit's current shape.
// Called by serializers before persisting so a corrupted unit
// surfaces at the write boundary rather than during subsequent
// read.
func (u *WorkUnit) Validate() error {
	if u.ID == "" {
		return fmt.Errorf("workunit: id is required")
	}
	if u.A2ATaskID == "" {
		return fmt.Errorf("workunit: a2a_task_id is required")
	}
	if u.DelegationID == "" {
		return fmt.Errorf("workunit: delegation_id is required")
	}
	if u.CreatedAt.IsZero() {
		return fmt.Errorf("workunit: created_at is required")
	}
	switch u.Status {
	case WorkUnitPending, WorkUnitAccepted, WorkUnitCompleted, WorkUnitFailed, WorkUnitRevoked:
		// ok
	default:
		return fmt.Errorf("workunit: invalid status %q", u.Status)
	}
	// If accepted, AcceptedAt must be set; similarly for
	// completed/failed/revoked + CompletedAt.
	if u.Status != WorkUnitPending && u.AcceptedAt == nil && u.Status != WorkUnitFailed {
		// A unit can Fail directly from Pending (e.g. delegation
		// verify failed), so Failed + nil AcceptedAt is OK. Other
		// post-pending states require AcceptedAt.
		return fmt.Errorf("workunit: accepted_at is required for status %q", u.Status)
	}
	if u.IsTerminal() && u.CompletedAt == nil {
		return fmt.Errorf("workunit: completed_at is required for terminal status %q", u.Status)
	}
	return nil
}
