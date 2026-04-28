package delegation

import (
	"context"
	"errors"
	"testing"

	"github.com/RelayOne/r1/internal/truecom"
)

func TestDelegationContext_HasScope(t *testing.T) {
	d := DelegationContext{Scopes: []string{"a", "b"}}
	if !d.HasScope("a") {
		t.Error(`HasScope("a") = false`)
	}
	if d.HasScope("c") {
		t.Error(`HasScope("c") should be false`)
	}
}

func TestAuthorize_EmptyBundleRejected(t *testing.T) {
	m := NewManager(truecom.NewStubClient())
	err := m.Authorize(context.Background(), DelegationContext{
		DelegateeDID: "b",
		PolicyBundle: "", // empty → default-deny
	}, "calendar_list_events", nil)
	if !errors.Is(err, ErrActionDenied) {
		t.Errorf("want ErrActionDenied on empty bundle, got %v", err)
	}
}

func TestAuthorize_PermissiveStubAllows(t *testing.T) {
	// Stub eval is permissive on any non-empty bundle. A non-
	// stub evaluator would apply real Cedar logic; we're
	// testing that Authorize forwards correctly.
	m := NewManager(truecom.NewStubClient())
	err := m.Authorize(context.Background(), DelegationContext{
		DelegateeDID: "b",
		PolicyBundle: "read-only-calendar",
	}, "calendar_list_events", nil)
	if err != nil {
		t.Errorf("stub should allow non-empty bundle, got %v", err)
	}
}

// restrictiveStub is a Client that denies every action for
// testing the deny path end-to-end.
type restrictiveStub struct {
	*truecom.StubClient
}

func (r *restrictiveStub) EvaluatePolicy(_ context.Context, _ truecom.PolicyRequest) error {
	return truecom.ErrPolicyDenied
}

func TestAuthorize_CedarDenyWrapped(t *testing.T) {
	m := NewManager(&restrictiveStub{truecom.NewStubClient()})
	err := m.Authorize(context.Background(), DelegationContext{
		DelegateeDID: "b",
		PolicyBundle: "anything",
	}, "execute_code", nil)
	if !errors.Is(err, ErrActionDenied) {
		t.Errorf("want ErrActionDenied on Cedar deny, got %v", err)
	}
}

func TestApplyPolicyBundle(t *testing.T) {
	m := NewManager(truecom.NewStubClient())
	dctx, err := m.ApplyPolicyBundle("read-only-calendar", "did:a", "did:b", "del-1")
	if err != nil {
		t.Fatalf("ApplyPolicyBundle: %v", err)
	}
	if dctx.DelegatorDID != "did:a" || dctx.DelegateeDID != "did:b" || dctx.DelegationID != "del-1" {
		t.Errorf("fields off: %+v", dctx)
	}
	if !dctx.HasScope("calendar_list_events") {
		t.Error("expected calendar_list_events in scopes")
	}
	if dctx.PolicyBundle != "read-only-calendar" {
		t.Errorf("PolicyBundle=%q want read-only-calendar", dctx.PolicyBundle)
	}
}

func TestApplyPolicyBundle_Unknown(t *testing.T) {
	m := NewManager(truecom.NewStubClient())
	_, err := m.ApplyPolicyBundle("ghost", "a", "b", "d")
	if !errors.Is(err, ErrUnknownBundle) {
		t.Errorf("want ErrUnknownBundle, got %v", err)
	}
}
