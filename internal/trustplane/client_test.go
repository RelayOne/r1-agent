package trustplane

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestStubClient_RegisterIdentity(t *testing.T) {
	c := NewStubClient()
	id, err := c.RegisterIdentity(context.Background(), IdentityRequest{
		AgentID:    "agent-a",
		StanceRole: "reviewer",
		PublicKey:  "pem-blob",
	})
	if err != nil {
		t.Fatalf("RegisterIdentity: %v", err)
	}
	if id.DID == "" {
		t.Error("DID should be populated")
	}
	if id.RegisteredAt.IsZero() {
		t.Error("RegisteredAt should be populated")
	}
}

func TestStubClient_AuditAnchor(t *testing.T) {
	c := NewStubClient()
	a, err := c.AnchorAudit(context.Background(), AuditRoot{
		LedgerID: "ledger-1",
		RootHash: "sha256:abcd",
	})
	if err != nil {
		t.Fatalf("AnchorAudit: %v", err)
	}
	if a.AnchorID == "" || a.TrustPlaneRef == "" {
		t.Errorf("anchor fields empty: %+v", a)
	}
}

func TestStubClient_HITLAutoApprove(t *testing.T) {
	// Stub auto-approves so local-dev flows exercise the full
	// approval path.
	c := NewStubClient()
	resp, err := c.RequestHITL(context.Background(), HITLRequest{
		AgentDID: "did:tp:a",
		Question: "proceed?",
	})
	if err != nil {
		t.Fatalf("RequestHITL: %v", err)
	}
	if resp.Decision != "approved" {
		t.Errorf("stub should auto-approve, got %q", resp.Decision)
	}
}

func TestStubClient_ReputationRoundTrip(t *testing.T) {
	c := NewStubClient()
	did := "did:tp:x"
	r, err := c.LookupReputation(context.Background(), did)
	if err != nil {
		t.Fatalf("LookupReputation: %v", err)
	}
	if r.Score <= 0 {
		t.Errorf("default score should be > 0, got %v", r.Score)
	}
	if err := c.RecordReputation(context.Background(), ReputationEntry{
		AgentDID:    did,
		Outcome:     "success",
		RatingDelta: 0.1,
		RecordedAt:  time.Now(),
	}); err != nil {
		t.Fatalf("RecordReputation: %v", err)
	}
	r2, _ := c.LookupReputation(context.Background(), did)
	if r2.SuccessfulHires != 1 {
		t.Errorf("SuccessfulHires=%d want 1", r2.SuccessfulHires)
	}
	if r2.TotalHires != 1 {
		t.Errorf("TotalHires=%d want 1", r2.TotalHires)
	}
}

func TestStubClient_DelegationLifecycle(t *testing.T) {
	c := NewStubClient()
	d, err := c.CreateDelegation(context.Background(), DelegationRequest{
		FromDID: "a", ToDID: "b", Expiry: time.Hour,
	})
	if err != nil {
		t.Fatalf("CreateDelegation: %v", err)
	}
	if err := c.VerifyDelegation(context.Background(), d.ID, "b"); err != nil {
		t.Errorf("fresh delegation should verify: %v", err)
	}
	if err := c.RevokeDelegation(context.Background(), d.ID); err != nil {
		t.Fatalf("RevokeDelegation: %v", err)
	}
	err = c.VerifyDelegation(context.Background(), d.ID, "b")
	if !errors.Is(err, ErrDelegationInvalid) {
		t.Errorf("revoked delegation should fail verify, got %v", err)
	}
}

func TestStubClient_DelegationExpiry(t *testing.T) {
	c := NewStubClient()
	d, _ := c.CreateDelegation(context.Background(), DelegationRequest{
		FromDID: "a", ToDID: "b", Expiry: -time.Hour, // already expired
	})
	err := c.VerifyDelegation(context.Background(), d.ID, "b")
	if !errors.Is(err, ErrDelegationInvalid) {
		t.Errorf("expired delegation should fail, got %v", err)
	}
}

func TestStubClient_DelegationNotFound(t *testing.T) {
	c := NewStubClient()
	err := c.VerifyDelegation(context.Background(), "nonexistent", "b")
	if !errors.Is(err, ErrDelegationInvalid) {
		t.Errorf("unknown delegation should fail, got %v", err)
	}
}

func TestStubClient_PolicyEvalRejectsEmptyBundle(t *testing.T) {
	c := NewStubClient()
	err := c.EvaluatePolicy(context.Background(), PolicyRequest{
		PolicyBundle: "", // empty -> deny
	})
	if !errors.Is(err, ErrPolicyDenied) {
		t.Errorf("empty bundle should be denied, got %v", err)
	}
}

func TestStubClient_PolicyEvalPermissive(t *testing.T) {
	// Stub permissive: non-empty bundle always allowed. Real
	// evaluator has actual Cedar logic.
	c := NewStubClient()
	if err := c.EvaluatePolicy(context.Background(), PolicyRequest{
		PolicyBundle: "personal-assistant",
		Action:       "calendar_read",
	}); err != nil {
		t.Errorf("stub permissive eval failed: %v", err)
	}
}

// Compile-time assertion that StubClient satisfies Client.
var _ Client = (*StubClient)(nil)
