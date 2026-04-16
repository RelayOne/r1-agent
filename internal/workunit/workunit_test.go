package workunit

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type mockVerifier struct {
	err error
}

func (m mockVerifier) VerifyDelegation(_ context.Context, _, _ string) error {
	return m.err
}

type mockAnchor struct {
	events []string
	err    error
}

func (m *mockAnchor) AnchorWorkUnit(_ context.Context, _ *WorkUnit, event string) error {
	m.events = append(m.events, event)
	return m.err
}

func TestBind_RequiresFields(t *testing.T) {
	if _, err := Bind("", "del", "", ""); err == nil {
		t.Error("expected error on missing a2a_task_id")
	}
	if _, err := Bind("task", "", "", ""); err == nil {
		t.Error("expected error on missing delegation_id")
	}
	u, err := Bind("task", "del", "audit", "parent-unit")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if u.Status != WorkUnitPending {
		t.Errorf("status=%q want pending", u.Status)
	}
	if u.SettlementPolicy != "rollback-immediately" {
		t.Errorf("default settlement policy=%q want rollback-immediately", u.SettlementPolicy)
	}
	if u.ID == "" {
		t.Error("ID should be populated")
	}
	if u.CreatedAt.IsZero() {
		t.Error("CreatedAt should be populated")
	}
}

func TestAccept_DelegationVerifies(t *testing.T) {
	u, _ := Bind("task", "del", "audit", "")
	anchor := &mockAnchor{}
	if err := u.Accept(context.Background(), "delegatee", mockVerifier{}, anchor); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if u.Status != WorkUnitAccepted {
		t.Errorf("status=%q want accepted", u.Status)
	}
	if u.AcceptedAt == nil {
		t.Error("AcceptedAt should be set")
	}
	if len(anchor.events) != 1 || anchor.events[0] != "accepted" {
		t.Errorf("anchor events=%v want [accepted]", anchor.events)
	}
}

func TestAccept_DelegationFails(t *testing.T) {
	u, _ := Bind("task", "del", "", "")
	err := u.Accept(context.Background(), "delegatee", mockVerifier{err: errors.New("expired")}, &mockAnchor{})
	if err == nil {
		t.Fatal("expected error on delegation failure")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("error should wrap verifier err, got %v", err)
	}
	if u.Status != WorkUnitPending {
		t.Errorf("status=%q — should remain pending on verify failure", u.Status)
	}
}

func TestAccept_NilVerifier_SkipsCheck(t *testing.T) {
	// Test scaffold path: nil verifier is allowed (caller
	// verified out-of-band). Shouldn't error.
	u, _ := Bind("task", "del", "", "")
	if err := u.Accept(context.Background(), "delegatee", nil, nil); err != nil {
		t.Fatalf("Accept with nil verifier: %v", err)
	}
	if u.Status != WorkUnitAccepted {
		t.Errorf("status=%q want accepted", u.Status)
	}
}

func TestAccept_WrongStatus(t *testing.T) {
	u, _ := Bind("task", "del", "", "")
	_ = u.Accept(context.Background(), "x", nil, nil)
	if err := u.Accept(context.Background(), "x", nil, nil); err == nil {
		t.Error("expected error accepting a non-pending unit")
	}
}

func TestReverify_HappyPath(t *testing.T) {
	u, _ := Bind("task", "del", "", "")
	_ = u.Accept(context.Background(), "delegatee", nil, nil)
	if err := u.Reverify(context.Background(), "delegatee", mockVerifier{}); err != nil {
		t.Fatalf("Reverify: %v", err)
	}
}

func TestReverify_FailSurfacesError(t *testing.T) {
	u, _ := Bind("task", "del", "", "")
	_ = u.Accept(context.Background(), "delegatee", nil, nil)
	err := u.Reverify(context.Background(), "delegatee", mockVerifier{err: errors.New("revoked")})
	if err == nil {
		t.Error("expected reverify failure")
	}
	// Status stays Accepted — caller decides whether to Revoke.
	if u.Status != WorkUnitAccepted {
		t.Errorf("status=%q — Reverify should not mutate status", u.Status)
	}
}

func TestComplete(t *testing.T) {
	u, _ := Bind("task", "del", "", "")
	_ = u.Accept(context.Background(), "x", nil, nil)
	anchor := &mockAnchor{}
	if err := u.Complete(context.Background(), anchor); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if u.Status != WorkUnitCompleted {
		t.Errorf("status=%q want completed", u.Status)
	}
	if !u.IsTerminal() {
		t.Error("completed unit should be terminal")
	}
	if anchor.events[0] != "completed" {
		t.Errorf("anchor got %v", anchor.events)
	}
}

func TestFail_FromPending(t *testing.T) {
	// Fail can fire from Pending (e.g. delegation verify failed
	// before accept).
	u, _ := Bind("task", "del", "", "")
	if err := u.Fail(context.Background(), "delegation invalid", nil); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if u.Status != WorkUnitFailed {
		t.Errorf("status=%q want failed", u.Status)
	}
}

func TestRevoke_Idempotent(t *testing.T) {
	u, _ := Bind("task", "del", "", "")
	_ = u.Accept(context.Background(), "x", nil, nil)
	if err := u.Revoke(context.Background(), nil); err != nil {
		t.Fatalf("first Revoke: %v", err)
	}
	if u.Status != WorkUnitRevoked {
		t.Errorf("status=%q want revoked", u.Status)
	}
	// Second revoke is a no-op (doesn't error, doesn't re-write
	// anchor).
	anchor := &mockAnchor{}
	if err := u.Revoke(context.Background(), anchor); err != nil {
		t.Errorf("second Revoke should be idempotent, got %v", err)
	}
	if len(anchor.events) != 0 {
		t.Errorf("second Revoke should not emit anchor event, got %v", anchor.events)
	}
}

func TestValidate_Shapes(t *testing.T) {
	u, _ := Bind("task", "del", "", "")
	if err := u.Validate(); err != nil {
		t.Errorf("pending unit should Validate ok, got %v", err)
	}
	// Empty ID.
	u.ID = ""
	if err := u.Validate(); err == nil {
		t.Error("empty id should fail Validate")
	}
}
