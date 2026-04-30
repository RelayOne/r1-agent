package skillmfr

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

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
