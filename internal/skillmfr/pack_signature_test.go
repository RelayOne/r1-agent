package skillmfr

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mutateTrailingBitB64 implements the PORTS.md §7 mutation recipe: flip the
// lowest bit of the final significant char's alphabet index. A lenient
// decoder yields the identical bytes — a strict verifier must reject the
// mutated string.
func mutateTrailingBitB64(t *testing.T, b64 string) string {
	t.Helper()
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	i := strings.IndexByte(b64, '=')
	if i == -1 {
		i = len(b64)
	}
	i-- // final significant char
	idx := strings.IndexByte(alphabet, b64[i])
	if idx < 0 {
		t.Fatalf("final significant char %q not in base64 alphabet", b64[i])
	}
	mutated := b64[:i] + string(alphabet[idx^1]) + b64[i+1:]
	a, err1 := base64.StdEncoding.DecodeString(b64)
	b, err2 := base64.StdEncoding.DecodeString(mutated)
	if err1 != nil || err2 != nil || !bytes.Equal(a, b) {
		t.Fatalf("mutation sanity: lenient decodes must be byte-identical (err1=%v err2=%v)", err1, err2)
	}
	return mutated
}

// TestVerifyPackSignatureRejectsNonCanonicalBase64 — PORTS.md §7: the verify
// path must reject non-canonical base64 for both the signature and the
// public key (mint side SignPack emits canonical StdEncoding).
func TestVerifyPackSignatureRejectsNonCanonicalBase64(t *testing.T) {
	t.Parallel()

	packDir := writeSignedPackFixture(t, "canon-pack")
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey(): %v", err)
	}
	signature, err := SignPack(packDir, "canon-key", privateKey)
	if err != nil {
		t.Fatalf("SignPack(): %v", err)
	}

	// Trailing-bit variant of the signature — identical bytes leniently.
	mutated := *signature
	mutated.Signature = mutateTrailingBitB64(t, signature.Signature)
	if err := WritePackSignature(packDir, &mutated); err != nil {
		t.Fatalf("WritePackSignature(): %v", err)
	}
	if _, err := VerifyPackSignature(packDir); !errors.Is(err, ErrPackSignatureInvalid) {
		t.Fatalf("non-canonical signature encoding: error = %v, want ErrPackSignatureInvalid", err)
	}

	// Trailing-bit variant of the public key must be rejected too.
	mutated = *signature
	mutated.PublicKey = mutateTrailingBitB64(t, signature.PublicKey)
	if err := WritePackSignature(packDir, &mutated); err != nil {
		t.Fatalf("WritePackSignature(): %v", err)
	}
	if _, err := VerifyPackSignature(packDir); !errors.Is(err, ErrPackSignatureInvalid) {
		t.Fatalf("non-canonical public key encoding: error = %v, want ErrPackSignatureInvalid", err)
	}

	// The canonical form still verifies.
	if err := WritePackSignature(packDir, signature); err != nil {
		t.Fatalf("WritePackSignature(): %v", err)
	}
	if _, err := VerifyPackSignature(packDir); err != nil {
		t.Fatalf("canonical signature must verify: %v", err)
	}
}

func TestSignAndVerifyPack(t *testing.T) {
	t.Parallel()

	packDir := writeSignedPackFixture(t, "billing-pack")
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey(): %v", err)
	}
	signature, err := SignPack(packDir, "billing-key", privateKey)
	if err != nil {
		t.Fatalf("SignPack(): %v", err)
	}
	if err := WritePackSignature(packDir, signature); err != nil {
		t.Fatalf("WritePackSignature(): %v", err)
	}
	verified, err := VerifyPackSignature(packDir)
	if err != nil {
		t.Fatalf("VerifyPackSignature(): %v", err)
	}
	if verified.KeyID != "billing-key" {
		t.Fatalf("KeyID = %q, want billing-key", verified.KeyID)
	}
	if verified.PackDigest != signature.PackDigest {
		t.Fatalf("PackDigest = %q, want %q", verified.PackDigest, signature.PackDigest)
	}
}

func TestVerifyPackSignatureRejectsTamper(t *testing.T) {
	t.Parallel()

	packDir := writeSignedPackFixture(t, "tampered-pack")
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey(): %v", err)
	}
	signature, err := SignPack(packDir, "", privateKey)
	if err != nil {
		t.Fatalf("SignPack(): %v", err)
	}
	if err := WritePackSignature(packDir, signature); err != nil {
		t.Fatalf("WritePackSignature(): %v", err)
	}
	manifestPath := filepath.Join(packDir, "tampered-pack.skill", "manifest.json")
	if err := os.WriteFile(manifestPath, []byte(`{"name":"tampered-pack.skill","version":"0.1.1","description":"tampered","inputSchema":{"type":"object"},"outputSchema":{"type":"object"},"whenToUse":["tamper"],"whenNotToUse":["other","different"],"behaviorFlags":{"mutatesState":false,"requiresNetwork":false}}`), 0o644); err != nil {
		t.Fatalf("WriteFile(manifest): %v", err)
	}
	if _, err := VerifyPackSignature(packDir); !errors.Is(err, ErrPackSignatureInvalid) {
		t.Fatalf("VerifyPackSignature() error = %v, want ErrPackSignatureInvalid", err)
	}
}

func TestVerifyPackSignatureIfPresentUnsignedPack(t *testing.T) {
	t.Parallel()

	packDir := writeSignedPackFixture(t, "unsigned-pack")
	signature, err := VerifyPackSignatureIfPresent(packDir)
	if err != nil {
		t.Fatalf("VerifyPackSignatureIfPresent(): %v", err)
	}
	if signature != nil {
		t.Fatalf("VerifyPackSignatureIfPresent() = %#v, want nil signature", signature)
	}
}

func writeSignedPackFixture(t *testing.T, packName string) string {
	t.Helper()

	packDir := filepath.Join(t.TempDir(), packName)
	manifestDir := filepath.Join(packDir, packName+".skill")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(manifestDir): %v", err)
	}
	if err := os.WriteFile(filepath.Join(packDir, "pack.yaml"), []byte("name: "+packName+"\nversion: 0.1.0\nskill_count: 1\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(pack.yaml): %v", err)
	}
	manifest := `{
  "name": "` + packName + `.skill",
  "version": "0.1.0",
  "description": "Fixture manifest",
  "inputSchema": {"type":"object"},
  "outputSchema": {"type":"object"},
  "whenToUse": ["Need fixture coverage"],
  "whenNotToUse": ["Need a different fixture", "Need a different service"],
  "behaviorFlags": {"mutatesState": false, "requiresNetwork": false}
}`
	if err := os.WriteFile(filepath.Join(manifestDir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("WriteFile(manifest): %v", err)
	}
	return packDir
}
