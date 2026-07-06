package skillmfr

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// writeTrustTestPack creates a minimal valid pack directory.
func writeTrustTestPack(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pack.yaml"),
		[]byte("name: trust-pack\nversion: 0.1.0\nskill_count: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(dir, "t.echo")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"t.echo","version":"1.0.0","description":"trust test skill","inputSchema":{"type":"object"},"outputSchema":{"type":"object"},"whenToUse":["a","b"],"whenNotToUse":["c","d"],"behaviorFlags":{"mutatesState":false,"requiresNetwork":false},"useIR":false}`
	if err := os.WriteFile(filepath.Join(skillDir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func signTestPack(t *testing.T, dir, keyID string) *PackSignature {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := SignPack(dir, keyID, priv)
	if err != nil {
		t.Fatal(err)
	}
	if err := WritePackSignature(dir, sig); err != nil {
		t.Fatal(err)
	}
	return sig
}

func TestVerifyPackTrusted_UnsignedRejectedByDefault(t *testing.T) {
	dir := writeTrustTestPack(t)
	if _, err := VerifyPackTrusted(dir, PackTrustPolicy{}); !errors.Is(err, ErrPackUnsigned) {
		t.Fatalf("unsigned pack: err = %v, want ErrPackUnsigned", err)
	}
}

func TestVerifyPackTrusted_UnsignedAllowedByOptIn(t *testing.T) {
	dir := writeTrustTestPack(t)
	sig, err := VerifyPackTrusted(dir, PackTrustPolicy{AllowUnsigned: true})
	if err != nil || sig != nil {
		t.Fatalf("unsigned + opt-in: sig=%v err=%v, want nil/nil", sig, err)
	}
}

func TestVerifyPackTrusted_SelfSignedRejectedByDefault(t *testing.T) {
	// A validly self-signed pack (signer key NOT in any trust anchor) must be
	// rejected fail-closed — this is the forgeable case the gap flags.
	dir := writeTrustTestPack(t)
	signTestPack(t, dir, "attacker-key")
	if _, err := VerifyPackTrusted(dir, PackTrustPolicy{}); !errors.Is(err, ErrPackUntrusted) {
		t.Fatalf("self-signed pack: err = %v, want ErrPackUntrusted", err)
	}
}

func TestVerifyPackTrusted_SignedByTrustedKeyAccepted(t *testing.T) {
	dir := writeTrustTestPack(t)
	sig := signTestPack(t, dir, "publisher-key")
	// Trust by key_id.
	got, err := VerifyPackTrusted(dir, PackTrustPolicy{TrustedKeys: map[string]bool{"publisher-key": true}})
	if err != nil || got == nil {
		t.Fatalf("trusted key_id: got=%v err=%v", got, err)
	}
	// Trust by public key too.
	got, err = VerifyPackTrusted(dir, PackTrustPolicy{TrustedKeys: map[string]bool{sig.PublicKey: true}})
	if err != nil || got == nil {
		t.Fatalf("trusted public_key: got=%v err=%v", got, err)
	}
}

func TestVerifyPackTrusted_SignedByUntrustedKeyRejected(t *testing.T) {
	dir := writeTrustTestPack(t)
	signTestPack(t, dir, "some-key")
	policy := PackTrustPolicy{TrustedKeys: map[string]bool{"a-different-trusted-key": true}}
	if _, err := VerifyPackTrusted(dir, policy); !errors.Is(err, ErrPackUntrusted) {
		t.Fatalf("untrusted key: err = %v, want ErrPackUntrusted", err)
	}
}

func TestVerifyPackTrusted_TamperedSignatureAlwaysRejected(t *testing.T) {
	dir := writeTrustTestPack(t)
	signTestPack(t, dir, "k")
	// Tamper a pack file after signing so the digest no longer matches.
	if err := os.WriteFile(filepath.Join(dir, "t.echo", "manifest.json"), []byte(`{"name":"t.echo","tampered":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Even with the permissive opt-in, a broken signature is a hard failure.
	if _, err := VerifyPackTrusted(dir, PackTrustPolicy{AllowUnsigned: true}); !errors.Is(err, ErrPackSignatureInvalid) {
		t.Fatalf("tampered signed pack: err = %v, want ErrPackSignatureInvalid", err)
	}
}

func TestParseTrustedPackKeys(t *testing.T) {
	got := ParseTrustedPackKeys("ed25519:aa, ed25519:bb\n" + base64.StdEncoding.EncodeToString([]byte("x")))
	if len(got) != 3 {
		t.Fatalf("parsed %d keys, want 3: %v", len(got), got)
	}
	if ParseTrustedPackKeys("   ") != nil {
		t.Fatal("blank input should parse to nil")
	}
}
