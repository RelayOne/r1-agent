package token

import (
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/beacon/identity"
)

func TestIssueVerifyAuthorizeAndDelegate(t *testing.T) {
	issuer, issuerPriv, err := identity.NewOperator("issuer@example.com")
	if err != nil {
		t.Fatalf("NewOperator issuer: %v", err)
	}
	subject, _, err := identity.NewOperator("subject@example.com")
	if err != nil {
		t.Fatalf("NewOperator subject: %v", err)
	}
	tok, err := Issue(issuer, issuerPriv, CapabilityToken{
		SubjectOperatorID:  subject.OperatorID,
		BeaconIDs:          []string{"bc-123"},
		Allow:              []string{"approve:*", "mission:view"},
		Deny:               []string{"approve:prod"},
		CostCapUSD:         10,
		DelegationDepthMax: 2,
		ConstitutionHash:   "constitution-v1",
		ExpiresAt:          time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if err := Verify(tok, issuer.PublicKey, time.Now()); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if err := Authorize(tok, "bc-123", "approve:dev", 2); err != nil {
		t.Fatalf("Authorize allow: %v", err)
	}
	if err := Authorize(tok, "bc-123", "approve:prod", 2); err == nil {
		t.Fatal("expected deny rule to block approval")
	}
	child, err := Delegate(tok, issuer, issuerPriv, CapabilityToken{
		SubjectOperatorID: subject.OperatorID,
		BeaconIDs:         []string{"bc-123"},
		Allow:             []string{"mission:view"},
		CostCapUSD:        5,
		ConstitutionHash:  "constitution-v1",
		ExpiresAt:         time.Now().Add(30 * time.Minute),
	})
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}
	if child.ParentTokenID != tok.TokenID || child.DelegationDepthUsed != 1 {
		t.Fatalf("unexpected delegated token lineage: %+v", child)
	}
}
