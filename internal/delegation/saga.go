// Package delegation — saga.go
//
// STOKE-016: cascade revocation with saga settlement. When a
// TrustPlane delegation is revoked (via Manager.Revoke or a
// TrustPlane-initiated revocation event arriving on the NATS
// topic `trustplane.delegation.revoked`), every in-flight
// WorkUnit executing under that delegation has to be settled
// according to the WorkUnit's SettlementPolicy.
//
// TrustPlane handles the revocation itself (cascade walk,
// child invalidation, audit anchoring). The Saga orchestrator
// is Stoke-side work: query local state for WorkUnits using
// the revoked delegation, apply the matching settlement policy
// per WorkUnit.
//
// Three settlement policies (shipped):
//
//   - rollback-immediately  (v1) — default: abort in-flight
//                                  work, run compensating
//                                  transactions, mark unit
//                                  Revoked. What callers get
//                                  when they don't specify
//                                  SettlementPolicy.
//   - complete-then-revoke  (v2) — finish the current atomic
//                                  step, then honor revocation.
//                                  Guarantees a clean terminal
//                                  state but can delay the
//                                  revocation honor indefinitely
//                                  if the step is long-running.
//   - checkpoint-and-revoke (v2) — save a resumable checkpoint,
//                                  then honor revocation. Best
//                                  of both: revoked promptly
//                                  but resumable by the
//                                  delegator from known-good
//                                  state.
//
// Idempotency: the saga safely handles duplicate revocation
// events from the NATS topic. Revoking an already-Revoked
// WorkUnit is a no-op.
package delegation

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/RelayOne/r1/internal/workunit"
)

// DefaultCompleteThenRevokeTimeout is how long settle() waits for
// a SettleCompleteThenRevoke unit's StepBoundary hook to fire
// before falling through to rollback semantics. 30s reflects the
// expectation that "current atomic step" is bounded and any unit
// holding the saga longer than that has already lost the
// race-against-revocation; safer to revoke + run comp txns than
// to wait indefinitely while a delegation is gone.
const DefaultCompleteThenRevokeTimeout = 30 * time.Second

// SettlementKind names the three settlement policies.
type SettlementKind string

const (
	SettleRollbackImmediately SettlementKind = "rollback-immediately"
	SettleCompleteThenRevoke  SettlementKind = "complete-then-revoke"
	SettleCheckpointAndRevoke SettlementKind = "checkpoint-and-revoke"
)

// CompensatingTxn is a rollback action associated with a
// WorkUnit. For each mutating action an agent takes while
// executing a WorkUnit, it can register a compensating txn so
// rollback-immediately can undo it cleanly. Callers whose
// actions are natively idempotent or read-only skip this.
type CompensatingTxn func(ctx context.Context) error

// Checkpoint is the snapshot state captured by
// SettleCheckpointAndRevoke. Opaque to this package;
// callers serialize whatever shape they need (JSON, protobuf,
// etc.).
type Checkpoint []byte

// Saga orchestrates settlement across the in-flight WorkUnits
// for a given Manager. A single Saga instance watches one
// TrustPlane subscription and dispatches settlement per event.
type Saga struct {
	mgr *Manager

	mu           sync.Mutex
	workUnits    map[string]*sagaEntry // keyed by WorkUnit ID
	byDelegation map[string]map[string]struct{} // delegationID -> set of workUnitIDs

	// perDelegationMu serializes OnRevocation calls for the
	// same delegation ID so duplicate NATS deliveries don't
	// run comp txns + snapshot hooks twice. Entries live
	// under mu for creation; the per-ID mutex is held for
	// the duration of the settlement.
	perDelegationMu map[string]*sync.Mutex

	// completeThenRevokeTimeout caps how long settle() waits on
	// a SettleCompleteThenRevoke unit's StepBoundary hook before
	// falling through to rollback. Mutable via
	// SetCompleteThenRevokeTimeout for tests + operators that
	// want a tighter bound. Read under mu.
	completeThenRevokeTimeout time.Duration
}

// sagaEntry is the per-WorkUnit bookkeeping the saga keeps.
type sagaEntry struct {
	unit     *workunit.WorkUnit
	anchor   workunit.AuditAnchor
	policy   SettlementKind
	comps    []CompensatingTxn
	snapshot func(ctx context.Context) (Checkpoint, error)

	// stepBoundary is the caller-supplied hook that returns
	// once the unit reaches a safe step boundary (current
	// atomic step finished — comp txns no longer needed). Used
	// only by SettleCompleteThenRevoke. nil means the policy
	// can't honor "complete then revoke" semantics; settle()
	// falls through to rollback for safety.
	stepBoundary func(ctx context.Context) error
}

// NewSaga constructs an orchestrator wired to a delegation
// Manager.
func NewSaga(mgr *Manager) *Saga {
	return &Saga{
		mgr:                       mgr,
		workUnits:                 map[string]*sagaEntry{},
		byDelegation:              map[string]map[string]struct{}{},
		perDelegationMu:           map[string]*sync.Mutex{},
		completeThenRevokeTimeout: DefaultCompleteThenRevokeTimeout,
	}
}

// SetCompleteThenRevokeTimeout overrides the default timeout
// settle() applies when waiting for a SettleCompleteThenRevoke
// unit's StepBoundary hook to fire. Pass <= 0 to disable the
// bound (the saga will block until ctx is canceled or the hook
// returns) — operators who use this should be confident their
// step boundary actually fires.
func (s *Saga) SetCompleteThenRevokeTimeout(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completeThenRevokeTimeout = d
}

// SetStepBoundary registers a hook that signals when the named
// WorkUnit has finished its current atomic step (i.e. it's safe
// to revoke without compensating transactions). The hook is
// consulted only by SettleCompleteThenRevoke; other policies
// ignore it. No-op for unknown unitIDs so callers can wire the
// hook before or after Register without sequencing concerns.
//
// Hook contract: the function blocks until the step boundary is
// reached (returning nil) OR ctx is canceled (returning the ctx
// error). settle() calls it with a derived ctx whose deadline is
// completeThenRevokeTimeout; the hook MUST honor cancellation.
func (s *Saga) SetStepBoundary(unitID string, hook func(ctx context.Context) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.workUnits[unitID]; ok {
		e.stepBoundary = hook
	}
}

// Register associates a WorkUnit with a settlement policy +
// optional compensating transactions + optional snapshot hook.
// The policy defaults to SettleRollbackImmediately when the
// unit's SettlementPolicy is blank.
//
// Optional params:
//   - compensatingTxns: applied in REVERSE order for rollback
//     (last-added-first-reverted, matching how nested
//     operations unwind).
//   - snapshot: invoked for SettleCheckpointAndRevoke to
//     capture pre-revocation state.
func (s *Saga) Register(unit *workunit.WorkUnit, anchor workunit.AuditAnchor, compensatingTxns []CompensatingTxn, snapshot func(ctx context.Context) (Checkpoint, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	policy := SettlementKind(unit.SettlementPolicy)
	if policy == "" {
		policy = SettleRollbackImmediately
	}
	entry := &sagaEntry{
		unit:     unit,
		anchor:   anchor,
		policy:   policy,
		comps:    append([]CompensatingTxn(nil), compensatingTxns...),
		snapshot: snapshot,
	}
	s.workUnits[unit.ID] = entry

	set, ok := s.byDelegation[unit.DelegationID]
	if !ok {
		set = map[string]struct{}{}
		s.byDelegation[unit.DelegationID] = set
	}
	set[unit.ID] = struct{}{}
}

// Deregister removes a WorkUnit from the saga's book. Called
// by callers once a unit completes or fails (reaches terminal
// state through non-revocation paths). Also drops the
// per-delegation mutex once the delegation has no remaining
// tracked units so long-lived subscribers don't leak
// `*sync.Mutex` per historical delegation ID.
func (s *Saga) Deregister(unitID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.workUnits[unitID]
	if !ok {
		return
	}
	delete(s.workUnits, unitID)
	if set, ok := s.byDelegation[entry.unit.DelegationID]; ok {
		delete(set, unitID)
		if len(set) == 0 {
			delete(s.byDelegation, entry.unit.DelegationID)
			delete(s.perDelegationMu, entry.unit.DelegationID)
		}
	}
}

// OnRevocation is the handler the NATS subscriber calls when a
// `trustplane.delegation.revoked` event arrives. Walks every
// WorkUnit registered under delegationID and applies its
// settlement policy. Safe to call with an unknown delegationID
// (no-op). Idempotent against duplicate events.
//
// Concurrency: a per-delegation mutex serializes duplicate
// events for the same delegation so comp txns + snapshot hooks
// run at most once per unit. Events for DIFFERENT delegations
// still run in parallel.
//
// Mid-settlement registrations: if Register() adds a NEW
// WorkUnit under the same delegation while settlement is in
// flight, the post-settlement cleanup is scoped to only the
// IDs we snapshotted at the start. Late arrivals stay in the
// book so a subsequent revocation event (or a manual
// OnRevocation retry) settles them correctly — they're never
// orphaned.
func (s *Saga) OnRevocation(ctx context.Context, delegationID string) SettlementReport {
	// Acquire (or create) the per-delegation mutex under the
	// top-level lock, then release the top-level lock before
	// acquiring the per-delegation one so other delegations
	// aren't blocked.
	s.mu.Lock()
	perMu, ok := s.perDelegationMu[delegationID]
	if !ok {
		perMu = &sync.Mutex{}
		s.perDelegationMu[delegationID] = perMu
	}
	s.mu.Unlock()

	perMu.Lock()
	defer perMu.Unlock()

	s.mu.Lock()
	set, ok := s.byDelegation[delegationID]
	if !ok {
		// Could mean "unknown delegation" OR "earlier
		// duplicate event already settled this one". Either
		// way: no work to do.
		s.mu.Unlock()
		return SettlementReport{DelegationID: delegationID}
	}
	// Snapshot the unit IDs so we can release the lock before
	// running (potentially long) settlement actions, AND so
	// mid-settlement Register() arrivals under this delegation
	// don't end up in our settle loop (they'll be picked up by
	// the next revocation event or a manual retry).
	unitIDs := make([]string, 0, len(set))
	for id := range set {
		unitIDs = append(unitIDs, id)
	}
	entries := make([]*sagaEntry, 0, len(unitIDs))
	for _, id := range unitIDs {
		entries = append(entries, s.workUnits[id])
	}
	s.mu.Unlock()

	report := SettlementReport{DelegationID: delegationID}
	for _, e := range entries {
		outcome := s.settle(ctx, e)
		report.Outcomes = append(report.Outcomes, outcome)
	}
	// Tidy up: only the units we actually settled leave the
	// book. Late arrivals registered during settlement stay
	// in byDelegation + workUnits so they survive to the
	// next revocation event instead of being orphaned.
	s.mu.Lock()
	defer s.mu.Unlock()
	settledIDs := make(map[string]struct{}, len(unitIDs))
	for _, id := range unitIDs {
		settledIDs[id] = struct{}{}
		delete(s.workUnits, id)
	}
	remainingDelegation := false
	if remaining, ok := s.byDelegation[delegationID]; ok {
		for id := range settledIDs {
			delete(remaining, id)
		}
		if len(remaining) == 0 {
			delete(s.byDelegation, delegationID)
		} else {
			remainingDelegation = true
		}
	}
	// Drop the per-delegation mutex when the delegation
	// has no remaining tracked units — otherwise the map
	// grows monotonically over time and leaks one lock per
	// lifetime delegation ID. Keeping the mutex only while
	// units are live means a subsequent OnRevocation for
	// the same ID just re-creates a fresh mutex (safe
	// because there's no in-flight caller by definition).
	if !remainingDelegation {
		delete(s.perDelegationMu, delegationID)
	}
	return report
}

// SettlementReport is the bundle of outcomes OnRevocation
// produces. Callers use it to drive observability + operator
// UI.
type SettlementReport struct {
	DelegationID string
	Outcomes     []SettlementOutcome
}

// SettlementOutcome is one WorkUnit's settlement result.
// All error fields are strings (not Go `error` values) so
// JSON marshaling round-trips correctly — `error` marshals
// to `{}` and operators watching the report channel would
// otherwise see empty objects instead of failure text.
type SettlementOutcome struct {
	WorkUnitID string
	Policy     SettlementKind
	// FinalStatus is the WorkUnitStatus the unit ended on
	// after settlement — typically WorkUnitRevoked.
	FinalStatus workunit.WorkUnitStatus
	// CompensatingTxnErrors holds any messages from individual
	// compensating transactions. Non-empty entries are
	// logged but don't abort settlement — the unit is still
	// marked Revoked so operators can see the mixed state.
	CompensatingTxnErrors []string
	// Checkpoint is the captured state (only for
	// SettleCheckpointAndRevoke).
	Checkpoint Checkpoint
	// SnapshotError is the error message from the caller-
	// supplied snapshot function, if any.
	SnapshotError string
	// AuditAnchorError is the message from writing the
	// `revoked` audit event, if any. Non-empty indicates the
	// WorkUnit reached Revoked state but the audit anchor
	// failed to record it.
	AuditAnchorError string
	// StepBoundaryError is the message from
	// SettleCompleteThenRevoke's wait-for-step hook. Set when
	// the hook returned a non-nil error (timeout/cancel) or
	// when no hook was registered (fell through to rollback).
	// Non-empty indicates the unit was revoked under rollback
	// semantics rather than waiting for a clean step boundary.
	StepBoundaryError string
}

// settle applies the per-entry settlement policy. Called by
// OnRevocation; not exported because callers should go through
// OnRevocation to get proper bookkeeping.
func (s *Saga) settle(ctx context.Context, e *sagaEntry) SettlementOutcome {
	out := SettlementOutcome{WorkUnitID: e.unit.ID, Policy: e.policy}

	if e.unit.IsTerminal() {
		// Already terminal (completed / failed / revoked);
		// nothing to do. Keep the WorkUnitRevoked final
		// status if it was already Revoked, else preserve
		// whatever terminal status it hit.
		out.FinalStatus = e.unit.Status
		return out
	}

	switch e.policy {
	case SettleRollbackImmediately:
		// Reverse-order compensating txns — last action
		// reverts first so nested operations unwind cleanly.
		for i := len(e.comps) - 1; i >= 0; i-- {
			if err := e.comps[i](ctx); err != nil {
				out.CompensatingTxnErrors = append(out.CompensatingTxnErrors, fmt.Sprintf("comp[%d]: %v", i, err))
			}
		}
		if err := e.unit.Revoke(ctx, e.anchor); err != nil {
			out.AuditAnchorError = err.Error()
		}

	case SettleCheckpointAndRevoke:
		if e.snapshot != nil {
			ck, err := e.snapshot(ctx)
			if err != nil {
				out.SnapshotError = err.Error()
			} else {
				out.Checkpoint = ck
			}
		}
		if err := e.unit.Revoke(ctx, e.anchor); err != nil {
			out.AuditAnchorError = err.Error()
		}

	case SettleCompleteThenRevoke:
		// Wait for the unit's StepBoundary hook to signal that
		// the current atomic step is finished, then Revoke
		// without running comp txns (the hook returning nil is
		// the unit's promise that its work landed cleanly).
		//
		// Three failure modes — all fall through to rollback
		// semantics for safety:
		//   1. No hook registered: caller declared the policy
		//      but didn't wire a boundary. Treat as if the unit
		//      never reaches a safe step.
		//   2. Hook returns error (timeout / ctx canceled /
		//      explicit failure): step boundary couldn't be
		//      observed; comp txns must run to undo partial
		//      work.
		//   3. Wait timer expires: the saga's
		//      completeThenRevokeTimeout bounds the wait so a
		//      stuck unit can't pin a delegation forever.
		// All three populate StepBoundaryError so operators see
		// why the unit took the rollback path.
		ok, stepErr := e.waitForStep(ctx, s.completeThenRevokeTimeoutLocked())
		if !ok {
			out.StepBoundaryError = stepErr
			for i := len(e.comps) - 1; i >= 0; i-- {
				if err := e.comps[i](ctx); err != nil {
					out.CompensatingTxnErrors = append(out.CompensatingTxnErrors, fmt.Sprintf("comp[%d]: %v", i, err))
				}
			}
		}
		if err := e.unit.Revoke(ctx, e.anchor); err != nil {
			out.AuditAnchorError = err.Error()
		}

	default:
		// Unknown policy: safest fallback is rollback-
		// immediately.
		for i := len(e.comps) - 1; i >= 0; i-- {
			if err := e.comps[i](ctx); err != nil {
				out.CompensatingTxnErrors = append(out.CompensatingTxnErrors, fmt.Sprintf("comp[%d]: %v", i, err))
			}
		}
		if err := e.unit.Revoke(ctx, e.anchor); err != nil {
			out.AuditAnchorError = err.Error()
		}
	}

	out.FinalStatus = e.unit.Status
	return out
}

// completeThenRevokeTimeoutLocked returns the configured
// timeout for SettleCompleteThenRevoke wait. Uses its own
// short-lock so settle() (which is called outside s.mu) can
// read the value safely without callers having to think about
// locking.
func (s *Saga) completeThenRevokeTimeoutLocked() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.completeThenRevokeTimeout
}

// waitForStep blocks on the entry's StepBoundary hook with a
// derived context bounded by timeout. Returns (true, "") when
// the hook returns nil — caller can revoke cleanly without
// running comp txns. Returns (false, msg) for the three rollback
// fallthrough cases (no hook, hook error, timeout); caller
// should run comp txns and revoke.
//
// timeout <= 0 disables the bound: the wait blocks until ctx is
// canceled or the hook returns, matching
// SetCompleteThenRevokeTimeout's documented semantics.
func (e *sagaEntry) waitForStep(ctx context.Context, timeout time.Duration) (bool, string) {
	if e.stepBoundary == nil {
		return false, "no StepBoundary hook registered for unit; falling back to rollback semantics"
	}
	waitCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	if err := e.stepBoundary(waitCtx); err != nil {
		// Disambiguate timeout from caller-side cancel from
		// hook-internal failure so operators reading the
		// outcome can tell why the unit fell through.
		if waitCtx.Err() == context.DeadlineExceeded {
			return false, fmt.Sprintf("StepBoundary timed out after %s: %v", timeout, err)
		}
		return false, fmt.Sprintf("StepBoundary error: %v", err)
	}
	return true, ""
}
