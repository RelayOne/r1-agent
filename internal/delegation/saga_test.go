package delegation

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/RelayOne/r1/internal/trustplane"
	"github.com/RelayOne/r1/internal/workunit"
)

func newAcceptedUnit(t *testing.T, taskID, delegationID, policy string) *workunit.WorkUnit {
	t.Helper()
	u, err := workunit.Bind(taskID, delegationID, "", "")
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	u.SettlementPolicy = policy
	if err := u.Accept(context.Background(), "delegatee", nil, nil); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	return u
}

func TestSaga_OnRevocation_RollbackImmediately(t *testing.T) {
	m := NewManager(trustplane.NewStubClient())
	s := NewSaga(m)
	u := newAcceptedUnit(t, "task-a", "del-1", string(SettleRollbackImmediately))

	var compCalls int
	comp := func(context.Context) error {
		compCalls++
		return nil
	}

	s.Register(u, nil, []CompensatingTxn{comp, comp, comp}, nil)
	report := s.OnRevocation(context.Background(), "del-1")
	if len(report.Outcomes) != 1 {
		t.Fatalf("outcomes=%d want 1", len(report.Outcomes))
	}
	if report.Outcomes[0].FinalStatus != workunit.WorkUnitRevoked {
		t.Errorf("final status=%q want revoked", report.Outcomes[0].FinalStatus)
	}
	if compCalls != 3 {
		t.Errorf("comp calls=%d want 3", compCalls)
	}
}

func TestSaga_OnRevocation_CheckpointAndRevoke(t *testing.T) {
	m := NewManager(trustplane.NewStubClient())
	s := NewSaga(m)
	u := newAcceptedUnit(t, "task-a", "del-1", string(SettleCheckpointAndRevoke))

	snap := func(context.Context) (Checkpoint, error) {
		return Checkpoint(`{"step":42}`), nil
	}
	s.Register(u, nil, nil, snap)

	report := s.OnRevocation(context.Background(), "del-1")
	o := report.Outcomes[0]
	if o.FinalStatus != workunit.WorkUnitRevoked {
		t.Errorf("final status=%q want revoked", o.FinalStatus)
	}
	if string(o.Checkpoint) != `{"step":42}` {
		t.Errorf("checkpoint=%q want {\"step\":42}", string(o.Checkpoint))
	}
}

func TestSaga_OnRevocation_CheckpointErrorCaptured(t *testing.T) {
	m := NewManager(trustplane.NewStubClient())
	s := NewSaga(m)
	u := newAcceptedUnit(t, "task-a", "del-1", string(SettleCheckpointAndRevoke))

	snapErr := errors.New("disk full")
	snap := func(context.Context) (Checkpoint, error) {
		return nil, snapErr
	}
	s.Register(u, nil, nil, snap)
	report := s.OnRevocation(context.Background(), "del-1")
	if report.Outcomes[0].SnapshotError == "" {
		t.Error("expected SnapshotError captured")
	}
	// Unit is still marked Revoked even on snapshot failure —
	// settlement doesn't stall on snapshot errors.
	if report.Outcomes[0].FinalStatus != workunit.WorkUnitRevoked {
		t.Errorf("unit should still revoke even on snapshot error")
	}
}

func TestSaga_OnRevocation_CompensatingTxnErrorsCollected(t *testing.T) {
	m := NewManager(trustplane.NewStubClient())
	s := NewSaga(m)
	u := newAcceptedUnit(t, "task-a", "del-1", string(SettleRollbackImmediately))

	failing := func(context.Context) error { return errors.New("comp boom") }
	ok := func(context.Context) error { return nil }

	s.Register(u, nil, []CompensatingTxn{ok, failing, ok}, nil)
	report := s.OnRevocation(context.Background(), "del-1")
	if len(report.Outcomes[0].CompensatingTxnErrors) != 1 {
		t.Errorf("want 1 comp error, got %d", len(report.Outcomes[0].CompensatingTxnErrors))
	}
	// Unit should still reach Revoked state.
	if report.Outcomes[0].FinalStatus != workunit.WorkUnitRevoked {
		t.Errorf("unit should reach Revoked even with failing comps")
	}
}

func TestSaga_OnRevocation_UnknownDelegationIsNoop(t *testing.T) {
	m := NewManager(trustplane.NewStubClient())
	s := NewSaga(m)
	report := s.OnRevocation(context.Background(), "nonexistent")
	if len(report.Outcomes) != 0 {
		t.Errorf("unknown delegation should produce 0 outcomes, got %d", len(report.Outcomes))
	}
}

func TestSaga_Idempotent_SecondRevokeIsNoop(t *testing.T) {
	m := NewManager(trustplane.NewStubClient())
	s := NewSaga(m)
	u := newAcceptedUnit(t, "task-a", "del-1", string(SettleRollbackImmediately))
	s.Register(u, nil, nil, nil)
	_ = s.OnRevocation(context.Background(), "del-1")
	// Second revocation: WorkUnit is no longer in the saga's
	// book (cleaned up after first settle), so no outcomes.
	report := s.OnRevocation(context.Background(), "del-1")
	if len(report.Outcomes) != 0 {
		t.Errorf("second revoke should be noop, got %d outcomes", len(report.Outcomes))
	}
}

func TestSaga_MultipleUnitsUnderSameDelegation(t *testing.T) {
	m := NewManager(trustplane.NewStubClient())
	s := NewSaga(m)
	u1 := newAcceptedUnit(t, "task-a", "del-1", string(SettleRollbackImmediately))
	u2 := newAcceptedUnit(t, "task-b", "del-1", string(SettleRollbackImmediately))
	s.Register(u1, nil, nil, nil)
	s.Register(u2, nil, nil, nil)
	report := s.OnRevocation(context.Background(), "del-1")
	if len(report.Outcomes) != 2 {
		t.Errorf("want 2 outcomes (both units under del-1), got %d", len(report.Outcomes))
	}
}

func TestSaga_Deregister(t *testing.T) {
	m := NewManager(trustplane.NewStubClient())
	s := NewSaga(m)
	u := newAcceptedUnit(t, "task-a", "del-1", string(SettleRollbackImmediately))
	s.Register(u, nil, nil, nil)
	s.Deregister(u.ID)
	report := s.OnRevocation(context.Background(), "del-1")
	if len(report.Outcomes) != 0 {
		t.Errorf("deregistered unit should not settle, got %d", len(report.Outcomes))
	}
}

func TestSaga_DefaultPolicyIsRollback(t *testing.T) {
	m := NewManager(trustplane.NewStubClient())
	s := NewSaga(m)
	u := newAcceptedUnit(t, "task-a", "del-1", "") // empty policy
	called := false
	s.Register(u, nil, []CompensatingTxn{
		func(context.Context) error { called = true; return nil },
	}, nil)
	report := s.OnRevocation(context.Background(), "del-1")
	if !called {
		t.Error("empty policy should default to rollback-immediately, running comp txns")
	}
	if report.Outcomes[0].Policy != SettleRollbackImmediately {
		t.Errorf("policy=%q want rollback-immediately (default)", report.Outcomes[0].Policy)
	}
}

func TestSaga_AlreadyTerminalUnitSkipped(t *testing.T) {
	m := NewManager(trustplane.NewStubClient())
	s := NewSaga(m)
	u := newAcceptedUnit(t, "task-a", "del-1", string(SettleRollbackImmediately))
	_ = u.Complete(context.Background(), nil) // reach terminal
	called := false
	s.Register(u, nil, []CompensatingTxn{
		func(context.Context) error { called = true; return nil },
	}, nil)
	_ = s.OnRevocation(context.Background(), "del-1")
	if called {
		t.Error("already-terminal unit should not run comp txns")
	}
}

// TestSaga_ConcurrentDuplicateRevocation reproduces the P1
// reported by codex on the first round: two NATS deliveries
// for the same delegation must NOT run comp txns + snapshot
// hooks twice. Per-delegation serialization fixes this.
func TestSaga_ConcurrentDuplicateRevocation(t *testing.T) {
	m := NewManager(trustplane.NewStubClient())
	s := NewSaga(m)
	u := newAcceptedUnit(t, "task-a", "del-1", string(SettleRollbackImmediately))

	var compCalls int32
	comp := func(context.Context) error {
		atomic.AddInt32(&compCalls, 1)
		return nil
	}
	s.Register(u, nil, []CompensatingTxn{comp, comp}, nil)

	// Fire 5 parallel OnRevocation calls for the same
	// delegation. Only one should produce outcomes; comp
	// txns should run at most once per registered comp.
	var wg sync.WaitGroup
	results := make([]SettlementReport, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = s.OnRevocation(context.Background(), "del-1")
		}(i)
	}
	wg.Wait()

	// Exactly one call should report outcomes; the others
	// should be empty (serialized; second-ish caller finds
	// the byDelegation entry already removed).
	outcomeCounts := 0
	for _, r := range results {
		if len(r.Outcomes) > 0 {
			outcomeCounts++
		}
	}
	if outcomeCounts != 1 {
		t.Errorf("outcome-producing calls=%d want 1 (others should be idempotent noops)", outcomeCounts)
	}
	if got := atomic.LoadInt32(&compCalls); got != 2 {
		t.Errorf("comp calls=%d want 2 (one per comp; duplicate revocations must not re-run)", got)
	}
}

// TestSaga_MidSettlementRegistrationPreserved reproduces the
// P1: a WorkUnit registered DURING settlement must NOT be
// orphaned by the cleanup loop.
func TestSaga_MidSettlementRegistrationPreserved(t *testing.T) {
	m := NewManager(trustplane.NewStubClient())
	s := NewSaga(m)

	// First unit registered normally.
	u1 := newAcceptedUnit(t, "task-a", "del-1", string(SettleRollbackImmediately))

	// Comp txn for u1 that registers u2 MID-settlement. This
	// mimics a supervisor that spawns follow-up work during
	// rollback.
	u2 := newAcceptedUnit(t, "task-b", "del-1", string(SettleRollbackImmediately))
	u2Registered := false
	comp := func(context.Context) error {
		if !u2Registered {
			s.Register(u2, nil, nil, nil)
			u2Registered = true
		}
		return nil
	}
	s.Register(u1, nil, []CompensatingTxn{comp}, nil)

	report1 := s.OnRevocation(context.Background(), "del-1")
	if len(report1.Outcomes) != 1 {
		t.Fatalf("first call outcomes=%d want 1 (only u1)", len(report1.Outcomes))
	}
	if report1.Outcomes[0].WorkUnitID != u1.ID {
		t.Errorf("first outcome is for %q want u1", report1.Outcomes[0].WorkUnitID)
	}

	// u2 must still be in the book. A second revocation
	// event settles u2 (not orphaned).
	report2 := s.OnRevocation(context.Background(), "del-1")
	if len(report2.Outcomes) != 1 {
		t.Fatalf("second call outcomes=%d want 1 (u2 late-registered)", len(report2.Outcomes))
	}
	if report2.Outcomes[0].WorkUnitID != u2.ID {
		t.Errorf("second outcome is for %q want u2 (%q)",
			report2.Outcomes[0].WorkUnitID, u2.ID)
	}
}

// TestSaga_DifferentDelegationsParallel confirms per-
// delegation serialization does NOT serialize UNrelated
// delegations. Two OnRevocation calls on different IDs should
// run in parallel.
func TestSaga_DifferentDelegationsParallel(t *testing.T) {
	m := NewManager(trustplane.NewStubClient())
	s := NewSaga(m)
	u1 := newAcceptedUnit(t, "a", "del-1", string(SettleRollbackImmediately))
	u2 := newAcceptedUnit(t, "b", "del-2", string(SettleRollbackImmediately))
	s.Register(u1, nil, nil, nil)
	s.Register(u2, nil, nil, nil)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = s.OnRevocation(context.Background(), "del-1") }()
	go func() { defer wg.Done(); _ = s.OnRevocation(context.Background(), "del-2") }()
	wg.Wait()

	// Both settled.
	if u1.Status != workunit.WorkUnitRevoked {
		t.Errorf("u1 status=%q want revoked", u1.Status)
	}
	if u2.Status != workunit.WorkUnitRevoked {
		t.Errorf("u2 status=%q want revoked", u2.Status)
	}
}

// TestSaga_AuditAnchorPlumbedIntoRevoke verifies the P2 fix:
// the anchor registered with a WorkUnit now flows into
// WorkUnit.Revoke, so a `revoked` audit event is emitted.
func TestSaga_AuditAnchorPlumbedIntoRevoke(t *testing.T) {
	m := NewManager(trustplane.NewStubClient())
	s := NewSaga(m)
	u := newAcceptedUnit(t, "task-a", "del-1", string(SettleRollbackImmediately))

	anchor := &countingAnchor{}
	s.Register(u, anchor, nil, nil)
	_ = s.OnRevocation(context.Background(), "del-1")

	if len(anchor.events) == 0 {
		t.Fatal("audit anchor received 0 events; expected 'revoked'")
	}
	found := false
	for _, e := range anchor.events {
		if e == "revoked" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'revoked' event in anchor log, got %v", anchor.events)
	}
}

// countingAnchor is a test-scaffold AuditAnchor.
type countingAnchor struct {
	mu     sync.Mutex
	events []string
	err    error
}

func (c *countingAnchor) AnchorWorkUnit(_ context.Context, _ *workunit.WorkUnit, event string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
	return c.err
}

// Ensure compile-time satisfaction of the interface.
var _ = func() workunit.AuditAnchor { return &countingAnchor{} }

// TestSaga_AuditAnchorErrorReportedAsString ensures the P2
// marshal-friendly string field captures anchor failures.
func TestSaga_AuditAnchorErrorReportedAsString(t *testing.T) {
	m := NewManager(trustplane.NewStubClient())
	s := NewSaga(m)
	u := newAcceptedUnit(t, "task-a", "del-1", string(SettleRollbackImmediately))

	anchor := &countingAnchor{err: fmt.Errorf("ledger full")}
	s.Register(u, anchor, nil, nil)
	report := s.OnRevocation(context.Background(), "del-1")
	if len(report.Outcomes) != 1 {
		t.Fatalf("outcomes=%d want 1", len(report.Outcomes))
	}
	if report.Outcomes[0].AuditAnchorError == "" {
		t.Error("expected AuditAnchorError captured")
	}
}

// TestSaga_CompensatingTxnErrorAsString ensures comp-txn
// errors round-trip via string (not error type that marshals
// to `{}`).
func TestSaga_CompensatingTxnErrorAsString(t *testing.T) {
	m := NewManager(trustplane.NewStubClient())
	s := NewSaga(m)
	u := newAcceptedUnit(t, "task-a", "del-1", string(SettleRollbackImmediately))

	failing := func(context.Context) error { return errors.New("boom") }
	s.Register(u, nil, []CompensatingTxn{failing}, nil)
	report := s.OnRevocation(context.Background(), "del-1")
	if len(report.Outcomes[0].CompensatingTxnErrors) != 1 {
		t.Fatal("expected 1 comp error")
	}
	if report.Outcomes[0].CompensatingTxnErrors[0] == "" {
		t.Error("error string should not be empty")
	}
}
