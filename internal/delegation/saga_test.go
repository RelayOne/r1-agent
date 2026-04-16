package delegation

import (
	"context"
	"errors"
	"testing"

	"github.com/ericmacdougall/stoke/internal/trustplane"
	"github.com/ericmacdougall/stoke/internal/workunit"
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
	if report.Outcomes[0].SnapshotError == nil {
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
