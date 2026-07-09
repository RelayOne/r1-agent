package authz

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/RelayOne/r1/internal/honestcrypto"
)

// seedProfileWithSpec writes a signed action spec + principal pubkey under
// <root>/<profile>/ and returns the root.
func seedProfileWithSpec(t *testing.T, profile string, spec honestcrypto.SignedActionSpec, pubPEM string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, profile)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, SpecFileName), b, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, PubKeyFileName), []byte(pubPEM), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func signedSpec(t *testing.T) (honestcrypto.SignedActionSpec, honestcrypto.KeyPair) {
	t.Helper()
	kp, err := honestcrypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	spec, err := honestcrypto.SignActionSpec("sub_1", []string{"read_file", "grep"}, []string{"bash"}, "2999-01-01T00:00:00Z", kp.PrivateKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return spec, kp
}

func TestLoadFromSeedProfile_AbsentIsDisabled(t *testing.T) {
	root := t.TempDir() // no profile dir / spec
	a, err := LoadFromSeedProfile(root, "missing")
	if err != nil || a != nil {
		t.Fatalf("absent spec must be (nil,nil), got (%v,%v)", a, err)
	}
	// Empty inputs also disabled.
	if a, err := LoadFromSeedProfile("", ""); a != nil || err != nil {
		t.Fatal("empty inputs must disable authz")
	}
}

func TestLoadFromSeedProfile_ValidConstructsAuthorizer(t *testing.T) {
	spec, kp := signedSpec(t)
	root := seedProfileWithSpec(t, "prof", spec, kp.PublicKeyPEM)
	a, err := LoadFromSeedProfile(root, "prof")
	if err != nil {
		t.Fatalf("valid spec must load: %v", err)
	}
	if a == nil || a.Backend() != honestcrypto.BackendSignedAllowlist {
		t.Fatalf("expected signed-allowlist authorizer, got %+v", a)
	}
	if Decide(a, "read_file", nil).Decision != honestcrypto.DecisionAllow {
		t.Fatal("read_file must be allowed")
	}
	if Decide(a, "write_file", nil).Decision != honestcrypto.DecisionDeny {
		t.Fatal("write_file (not in allowlist) must be denied — reject-before-execute")
	}
	if Decide(a, "bash", nil).Decision != honestcrypto.DecisionRequireApproval {
		t.Fatal("bash must require approval")
	}
}

func TestLoadFromSeedProfile_TamperedFailsClosed(t *testing.T) {
	spec, kp := signedSpec(t)
	// Tamper the allowlist after signing — signature no longer matches.
	spec.Allow = append(spec.Allow, "write_file")
	root := seedProfileWithSpec(t, "prof", spec, kp.PublicKeyPEM)
	if _, err := LoadFromSeedProfile(root, "prof"); err == nil {
		t.Fatal("tampered spec must fail closed (error), not load")
	}
}

func TestLoadFromSeedProfile_MissingPubkeyFailsClosed(t *testing.T) {
	spec, _ := signedSpec(t)
	root := t.TempDir()
	dir := filepath.Join(root, "prof")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(spec)
	if err := os.WriteFile(filepath.Join(dir, SpecFileName), b, 0o644); err != nil {
		t.Fatal(err)
	}
	// No pubkey file present.
	if _, err := LoadFromSeedProfile(root, "prof"); err == nil {
		t.Fatal("spec present but pubkey missing must fail closed")
	}
}

func TestDecide_NilAuthorizerAllows(t *testing.T) {
	if Decide(nil, "anything", nil).Decision != honestcrypto.DecisionAllow {
		t.Fatal("nil authorizer (authz disabled) must allow")
	}
}

func TestDecisionRecordIsAnchorable(t *testing.T) {
	res := honestcrypto.AuthorizationResult{Decision: honestcrypto.DecisionDeny, Reason: "not in allowlist"}
	rec, err := DecisionRecord("sub_1", "write_file", res, "specHASH", "2026-07-08T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Kind != "authz.decision" || rec.Producer != "r1" {
		t.Fatalf("unexpected record envelope: %+v", rec)
	}
	if !honestcrypto.VerifyRecordHash(rec) {
		t.Fatal("decision record must self-verify (anchorable)")
	}
}

// alwaysAllow stands in for a stronger authorizer backend.
type alwaysAllow struct{}

func (alwaysAllow) Authorize(honestcrypto.ActionRequest) honestcrypto.AuthorizationResult {
	return honestcrypto.AuthorizationResult{Decision: honestcrypto.DecisionAllow, Reason: "stronger backend"}
}
func (alwaysAllow) SessionSpecHash() string { return "f" }
func (alwaysAllow) Backend() string         { return "fake-stronger-authz" }

// TestSwapProof: the SAME Decide() call site routes through a swapped authorizer
// with zero changes — the signed-allowlist default denies write_file, a stronger
// backend allows it, decided purely by which authorizer is injected.
func TestSwapProof(t *testing.T) {
	spec, kp := signedSpec(t)
	root := seedProfileWithSpec(t, "prof", spec, kp.PublicKeyPEM)
	def, err := LoadFromSeedProfile(root, "prof")
	if err != nil {
		t.Fatal(err)
	}
	if Decide(def, "write_file", nil).Decision != honestcrypto.DecisionDeny {
		t.Fatal("default signed-allowlist must deny write_file")
	}
	// Swap the backend at the same call site.
	if Decide(alwaysAllow{}, "write_file", nil).Decision != honestcrypto.DecisionAllow {
		t.Fatal("swapped stronger backend must allow — zero call-site change")
	}
}
