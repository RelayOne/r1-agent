package truecom

import (
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/honestcrypto"
)

func fixedNow(ts string) func() time.Time {
	t, _ := time.Parse(time.RFC3339, ts)
	return func() time.Time { return t }
}

func TestCanonicalAuditRecordShape(t *testing.T) {
	rec, err := CanonicalAuditRecord("org_1", AuditRoot{
		LedgerID:  "ledger-1",
		RootHash:  "deadbeef",
		EmittedAt: time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC),
		Meta:      map[string]string{"mission": "m1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Producer != "r1" || rec.Kind != "audit.anchor" || rec.Schema != honestcrypto.Schema {
		t.Fatalf("unexpected envelope: %+v", rec)
	}
	if !honestcrypto.VerifyRecordHash(rec) {
		t.Fatal("canonical audit record must self-verify")
	}
}

func TestGatewayAnchorerImplementsCanonicalAnchorer(t *testing.T) {
	g := NewGatewayAnchorer(NewStubClient(), "org_1", fixedNow("2026-07-08T00:00:00Z"))
	proof, err := g.Anchor([]string{"h1", "h2"})
	if err != nil {
		t.Fatal(err)
	}
	if proof.Backend != BackendGatewayTimestamp {
		t.Fatalf("backend = %q, want %q", proof.Backend, BackendGatewayTimestamp)
	}
	if proof.Proof["gatewayAnchorId"] == nil {
		t.Fatal("gateway anchor id must be stamped into the proof")
	}
	if !g.Verify("h1", proof) {
		t.Fatal("included hash must verify")
	}
	if g.Verify("nope", proof) {
		t.Fatal("absent hash must not verify")
	}
}

// TestGatewayAnchorerSwapProof proves the gateway backend drops into the canonical
// seam: a caller that only uses honestcrypto.GetAnchorer() routes through it with
// zero call-site changes.
func TestGatewayAnchorerSwapProof(t *testing.T) {
	honestcrypto.ResetAnchorer()
	defer honestcrypto.ResetAnchorer()

	callerAnchorsAndVerifies := func(hash string) (string, bool) {
		a := honestcrypto.GetAnchorer()
		proof, err := a.Anchor([]string{hash})
		if err != nil {
			t.Fatal(err)
		}
		return proof.Backend, a.Verify(hash, proof)
	}

	backend, ok := callerAnchorsAndVerifies("rec-1")
	if backend != honestcrypto.BackendTrustedTimestamp || !ok {
		t.Fatalf("default: backend=%q ok=%v", backend, ok)
	}

	honestcrypto.SetAnchorer(NewGatewayAnchorer(NewStubClient(), "org_1", fixedNow("2026-07-08T00:00:00Z")))
	backend, ok = callerAnchorsAndVerifies("rec-1") // identical call site
	if backend != BackendGatewayTimestamp || !ok {
		t.Fatalf("after swap: backend=%q ok=%v", backend, ok)
	}
}
