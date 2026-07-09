package honestcrypto

import (
	"testing"
	"time"
)

// goldenVector is the PORTS.md section 5 conformance hash. Every language port
// MUST reproduce this exact hex for the fixed input; if a port differs, its
// canonicalizer or field order is wrong — fix the port, never the vector.
const goldenVector = "52665c72b3e26cbfaaddbe35a71e92ffec1dfc8e3d1af69851a568fe1398c2cf"

func TestGoldenConformanceVector(t *testing.T) {
	got, err := ComputeRecordHash(
		"org_1", "relayone", "audit.event", "2026-07-08T00:00:00Z",
		map[string]any{"a": 1, "b": 2}, nil,
	)
	if err != nil {
		t.Fatalf("ComputeRecordHash: %v", err)
	}
	if got != goldenVector {
		t.Fatalf("golden vector mismatch:\n got  %s\n want %s", got, goldenVector)
	}
}

func TestCanonicalizeKeyOrderIndependent(t *testing.T) {
	h1, err := ComputeRecordHash("s", "p", "k", "t", map[string]any{"a": 1, "b": 2}, nil)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := ComputeRecordHash("s", "p", "k", "t", map[string]any{"b": 2, "a": 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("canonicalization must be key-order independent: %s != %s", h1, h2)
	}
	canon, err := Canonicalize(map[string]any{"b": 1, "a": 2})
	if err != nil {
		t.Fatal(err)
	}
	if string(canon) != `{"a":2,"b":1}` {
		t.Fatalf("unexpected canonical form: %s", canon)
	}
}

func TestMakeRecordSealsHash(t *testing.T) {
	r, err := MakeRecord(NewRecordInput{
		Subject: "org_1", Producer: "relayone", Kind: "audit.event",
		TS: "2026-07-08T00:00:00Z", Body: map[string]any{"b": 2, "a": 1}, PrevHash: nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Hash) != 64 {
		t.Fatalf("hash length = %d, want 64", len(r.Hash))
	}
	if r.RecordID != r.Hash {
		t.Fatalf("recordId %q must equal hash %q", r.RecordID, r.Hash)
	}
	if !VerifyRecordHash(r) {
		t.Fatal("VerifyRecordHash must be true for a freshly sealed record")
	}
}

func TestDetectsTamperedBody(t *testing.T) {
	r, err := MakeRecord(NewRecordInput{
		Subject: "s", Producer: "p", Kind: "k", TS: "t",
		Body: map[string]any{"amount": 100}, PrevHash: nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.Body = map[string]any{"amount": 999}
	if VerifyRecordHash(r) {
		t.Fatal("tampered body must fail VerifyRecordHash")
	}
}

func TestVerifyChain(t *testing.T) {
	r0, _ := MakeRecord(NewRecordInput{Subject: "s", Producer: "p", Kind: "k", TS: "t0", Body: map[string]any{"i": 0}, PrevHash: nil})
	r1, _ := MakeRecord(NewRecordInput{Subject: "s", Producer: "p", Kind: "k", TS: "t1", Body: map[string]any{"i": 1}, PrevHash: Ptr(r0.Hash)})
	r2, _ := MakeRecord(NewRecordInput{Subject: "s", Producer: "p", Kind: "k", TS: "t2", Body: map[string]any{"i": 2}, PrevHash: Ptr(r1.Hash)})
	if idx := VerifyChain([]Record{r0, r1, r2}); idx != -1 {
		t.Fatalf("intact chain must verify, got broken at %d", idx)
	}
	broken := r2
	broken.PrevHash = Ptr("deadbeef")
	if idx := VerifyChain([]Record{r0, r1, broken}); idx != 2 {
		t.Fatalf("broken link must be located at 2, got %d", idx)
	}
}

func TestEd25519SignVerifyRecord(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	r, _ := MakeRecord(NewRecordInput{Subject: "s", Producer: "relaygate", Kind: "receipt.model_call", TS: "t", Body: map[string]any{"cost": 5}, PrevHash: nil})
	sig, err := SignRecord(r, kp.PrivateKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyRecordSignature(r, sig, kp.PublicKeyPEM) {
		t.Fatal("valid record signature must verify")
	}
	// A tampered body must fail signature verification (hash recomputed).
	r.Body = map[string]any{"cost": 9999}
	if VerifyRecordSignature(r, sig, kp.PublicKeyPEM) {
		t.Fatal("tampered record must fail signature verification")
	}
}

func TestEd25519RejectsWrongKey(t *testing.T) {
	a, _ := GenerateKeyPair()
	b, _ := GenerateKeyPair()
	sig, err := SignBytes([]byte("hello"), a.PrivateKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyBytes([]byte("hello"), sig, a.PublicKeyPEM) {
		t.Fatal("signature must verify under its own key")
	}
	if VerifyBytes([]byte("hello"), sig, b.PublicKeyPEM) {
		t.Fatal("signature must not verify under a different key")
	}
}

func TestMerkleInclusion(t *testing.T) {
	values := []string{"a", "b", "c", "d", "e"}
	leaves := make([]string, len(values))
	for i, v := range values {
		leaves[i] = LeafHash(v)
	}
	root := MerkleRoot(leaves)
	for i := range leaves {
		p, ok := InclusionProofFor(leaves, i)
		if !ok {
			t.Fatalf("inclusion proof for %d not ok", i)
		}
		if !VerifyInclusion(leaves[i], p, root) {
			t.Fatalf("inclusion proof for leaf %d must verify", i)
		}
	}
	p0, _ := InclusionProofFor(leaves, 0)
	if VerifyInclusion(LeafHash("z"), p0, root) {
		t.Fatal("wrong leaf must not verify")
	}
}

func TestEmptyIntervalProof(t *testing.T) {
	values := []string{"t1", "t2", "t3", "t4"}
	leaves := make([]string, len(values))
	for i, v := range values {
		leaves[i] = LeafHash(v)
	}
	root := MerkleRoot(leaves)
	p1, _ := InclusionProofFor(leaves, 1)
	p2, _ := InclusionProofFor(leaves, 2)
	good := EmptyIntervalProof{
		Root:   root,
		Before: &IntervalBracket{Leaf: leaves[1], Proof: p1},
		After:  &IntervalBracket{Leaf: leaves[2], Proof: p2},
	}
	if !VerifyEmptyInterval(good) {
		t.Fatal("adjacent-bracket empty-interval proof must verify")
	}
	p0, _ := InclusionProofFor(leaves, 0)
	bad := EmptyIntervalProof{
		Root:   root,
		Before: &IntervalBracket{Leaf: leaves[0], Proof: p0},
		After:  &IntervalBracket{Leaf: leaves[2], Proof: p2},
	}
	if VerifyEmptyInterval(bad) {
		t.Fatal("non-adjacent brackets must fail")
	}
}

func fixedClock(ts string) func() time.Time {
	t, _ := time.Parse(time.RFC3339, ts)
	return func() time.Time { return t }
}

func TestAnchorerDefaultBackend(t *testing.T) {
	a := NewTrustedTimestampAnchorer(fixedClock("2026-07-08T00:00:00Z"))
	proof, err := a.Anchor([]string{"h1", "h2", "h3"})
	if err != nil {
		t.Fatal(err)
	}
	if proof.Backend != BackendTrustedTimestamp {
		t.Fatalf("backend = %q, want %q", proof.Backend, BackendTrustedTimestamp)
	}
	if !a.Verify("h2", proof) {
		t.Fatal("included hash must verify")
	}
	if a.Verify("nope", proof) {
		t.Fatal("absent hash must not verify")
	}
}

// fakeStrongerAnchorer stands in for a future operator-independent backend.
type fakeStrongerAnchorer struct{}

func (fakeStrongerAnchorer) Backend() string { return "fake-stronger" }
func (fakeStrongerAnchorer) Anchor(recordHashes []string) (AnchorProof, error) {
	included := make([]any, len(recordHashes))
	for i, h := range recordHashes {
		included[i] = h
	}
	return AnchorProof{Backend: "fake-stronger", Root: "strong", Proof: map[string]any{"included": included}, AnchoredAt: "2026-07-08T00:00:00Z"}, nil
}
func (fakeStrongerAnchorer) Verify(recordHash string, proof AnchorProof) bool {
	inc, _ := proof.Proof["included"].([]any)
	for _, v := range inc {
		if s, _ := v.(string); s == recordHash {
			return true
		}
	}
	return false
}
func (fakeStrongerAnchorer) Upgrade(proof AnchorProof) (AnchorProof, error) { return proof, nil }

// TestAnchorerSwapProof is the flagship swap-proof: a caller that only uses
// GetAnchorer() (never names a concrete backend) routes through the default,
// then through a second backend after SetAnchorer, with zero call-site changes.
func TestAnchorerSwapProof(t *testing.T) {
	ResetAnchorer()
	defer ResetAnchorer()

	callerAnchorsAndVerifies := func(recordHash string) (string, bool) {
		anchorer := GetAnchorer()
		proof, err := anchorer.Anchor([]string{recordHash})
		if err != nil {
			t.Fatal(err)
		}
		return proof.Backend, anchorer.Verify(recordHash, proof)
	}

	backend, ok := callerAnchorsAndVerifies("rec-1")
	if backend != BackendTrustedTimestamp || !ok {
		t.Fatalf("default: backend=%q ok=%v", backend, ok)
	}

	SetAnchorer(fakeStrongerAnchorer{})
	backend, ok = callerAnchorsAndVerifies("rec-1") // identical call site
	if backend != "fake-stronger" || !ok {
		t.Fatalf("after swap: backend=%q ok=%v", backend, ok)
	}
}

func TestActionAuthorizerDefault(t *testing.T) {
	kp, _ := GenerateKeyPair()
	spec, err := SignActionSpec("sub_1", []string{"model.call"}, []string{"pay"}, "2999-01-01T00:00:00Z", kp.PrivateKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	authz, err := NewSignedAllowlistAuthorizer(spec, kp.PublicKeyPEM, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d := authz.Authorize(ActionRequest{Action: "model.call"}).Decision; d != DecisionAllow {
		t.Fatalf("model.call = %v, want Allow", d)
	}
	if d := authz.Authorize(ActionRequest{Action: "tool.exec"}).Decision; d != DecisionDeny {
		t.Fatalf("tool.exec = %v, want Deny", d)
	}
	if d := authz.Authorize(ActionRequest{Action: "pay"}).Decision; d != DecisionRequireApproval {
		t.Fatalf("pay = %v, want RequireApproval", d)
	}
	if len(authz.SessionSpecHash()) != 64 {
		t.Fatalf("spec hash length = %d, want 64", len(authz.SessionSpecHash()))
	}

	// A tampered spec must refuse to construct.
	tampered := spec
	tampered.Allow = []string{"model.call", "tool.exec"}
	if _, err := NewSignedAllowlistAuthorizer(tampered, kp.PublicKeyPEM, nil); err == nil {
		t.Fatal("tampered spec must fail to construct (fail-closed)")
	}

	// Expired spec denies.
	expiredSpec, _ := SignActionSpec("sub_1", []string{"model.call"}, nil, "2000-01-01T00:00:00Z", kp.PrivateKeyPEM)
	expired, err := NewSignedAllowlistAuthorizer(expiredSpec, kp.PublicKeyPEM, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d := expired.Authorize(ActionRequest{Action: "model.call"}).Decision; d != DecisionDeny {
		t.Fatalf("expired = %v, want Deny", d)
	}
}

// alwaysAllow stands in for a stronger authorizer backend.
type alwaysAllow struct{}

func (alwaysAllow) Authorize(ActionRequest) AuthorizationResult {
	return AuthorizationResult{Decision: DecisionAllow, Reason: "fake"}
}
func (alwaysAllow) SessionSpecHash() string { return "" }
func (alwaysAllow) Backend() string         { return "fake-allow" }

func TestActionAuthorizerSwapProof(t *testing.T) {
	ResetActionAuthorizer()
	defer ResetActionAuthorizer()
	if _, err := GetActionAuthorizer(); err == nil {
		t.Fatal("no authorizer installed must fail closed")
	}
	SetActionAuthorizer(alwaysAllow{})
	authz, err := GetActionAuthorizer()
	if err != nil {
		t.Fatal(err)
	}
	if d := authz.Authorize(ActionRequest{Action: "anything"}).Decision; d != DecisionAllow {
		t.Fatalf("swapped authorizer decision = %v, want Allow", d)
	}
}

func TestSeedResolverDefault(t *testing.T) {
	r := NewStaticSeedResolver(map[string]SeedTiers{
		"sales": {Role: "You are a sales agent.", Task: "Close the lead."},
	})
	out := r.Resolve("sales", nil)
	if out.Prefix != "You are a sales agent.\n\nClose the lead." {
		t.Fatalf("unexpected prefix: %q", out.Prefix)
	}
	if len(out.Meta.Tiers) != 2 || out.Meta.Tiers[0] != "role" || out.Meta.Tiers[1] != "task" {
		t.Fatalf("unexpected tiers: %v", out.Meta.Tiers)
	}
	// Unknown profile -> empty prefix, never panics.
	if got := r.Resolve("unknown", nil).Prefix; got != "" {
		t.Fatalf("unknown profile prefix = %q, want empty", got)
	}
}
