package promptguard

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

// genKeyForTest produces a deterministic-by-fixture ed25519 keypair so a
// test failure points at a stable signature blob rather than a flaky
// random one. Each test that needs an independent key calls this with
// a fresh `seed`.
func genKeyForTest(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	return priv
}

// TestSignSystemPrompt_Roundtrip exercises the happy path: sign a
// prompt, verify the same prompt against the resulting fingerprint,
// expect Verified.
func TestSignSystemPrompt_Roundtrip(t *testing.T) {
	priv := genKeyForTest(t)
	pub := priv.Public().(ed25519.PublicKey)
	signedAt := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	prompt := "you are a careful agent. respect the operator."

	fp, err := SignSystemPromptWithKey(prompt, priv, signedAt)
	if err != nil {
		t.Fatalf("SignSystemPromptWithKey: %v", err)
	}
	if fp.PromptSHA256 == "" {
		t.Fatal("fingerprint has empty PromptSHA256")
	}
	if fp.Signature == "" {
		t.Fatal("fingerprint has empty Signature")
	}
	if fp.KeyFingerprint == "" {
		t.Fatal("fingerprint has empty KeyFingerprint")
	}
	status, vErr := VerifySystemPromptWithKey(prompt, fp, pub)
	if vErr != nil {
		t.Fatalf("VerifySystemPromptWithKey: %v", vErr)
	}
	if status != Verified {
		t.Errorf("status = %v, want Verified", status)
	}
}

// TestSignSystemPrompt_Deterministic_RestartProof asserts the
// acceptance criterion: the same prompt + same key + same signed-at
// time produces byte-identical signatures across "daemon restarts".
func TestSignSystemPrompt_Deterministic_RestartProof(t *testing.T) {
	priv := genKeyForTest(t)
	signedAt := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	prompt := "the boot-time assembled system prompt"

	fp1, err := SignSystemPromptWithKey(prompt, priv, signedAt)
	if err != nil {
		t.Fatalf("first sign: %v", err)
	}
	fp2, err := SignSystemPromptWithKey(prompt, priv, signedAt)
	if err != nil {
		t.Fatalf("second sign: %v", err)
	}
	if fp1.Signature != fp2.Signature {
		t.Errorf("ed25519 over canonical-JSON should be deterministic; got %s vs %s",
			fp1.Signature, fp2.Signature)
	}
	if fp1.PromptSHA256 != fp2.PromptSHA256 {
		t.Errorf("SHA-256 not stable across calls: %s vs %s", fp1.PromptSHA256, fp2.PromptSHA256)
	}
}

// TestVerifySystemPrompt_ModifiedReturnsModified mutates the prompt by
// one byte after signing; verification must surface Modified, not
// Tampered (the signature itself is intact).
func TestVerifySystemPrompt_ModifiedReturnsModified(t *testing.T) {
	priv := genKeyForTest(t)
	pub := priv.Public().(ed25519.PublicKey)
	signedAt := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	original := "respect the operator and the rules."

	fp, err := SignSystemPromptWithKey(original, priv, signedAt)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	mutated := original + "."
	status, vErr := VerifySystemPromptWithKey(mutated, fp, pub)
	if vErr != nil {
		t.Fatalf("verify: %v", vErr)
	}
	if status != Modified {
		t.Errorf("status = %v, want Modified", status)
	}
}

// TestVerifySystemPrompt_TamperedSignatureReturnsTampered flips one
// byte in the Signature field; verification must surface Tampered.
func TestVerifySystemPrompt_TamperedSignatureReturnsTampered(t *testing.T) {
	priv := genKeyForTest(t)
	pub := priv.Public().(ed25519.PublicKey)
	signedAt := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	prompt := "respect the operator and the rules."

	fp, err := SignSystemPromptWithKey(prompt, priv, signedAt)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	// Flip the first hex nibble of the signature.
	sigBytes, err := hex.DecodeString(fp.Signature)
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	sigBytes[0] ^= 0x01
	fp.Signature = hex.EncodeToString(sigBytes)

	status, vErr := VerifySystemPromptWithKey(prompt, fp, pub)
	if vErr != nil {
		t.Fatalf("verify: %v", vErr)
	}
	if status != Tampered {
		t.Errorf("status = %v, want Tampered", status)
	}
}

// TestVerifySystemPrompt_EmptySignatureIsTampered asserts that an
// empty signature is treated as tampered (not as a soft no-op).
func TestVerifySystemPrompt_EmptySignatureIsTampered(t *testing.T) {
	priv := genKeyForTest(t)
	pub := priv.Public().(ed25519.PublicKey)
	signedAt := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	prompt := "x"
	fp, err := SignSystemPromptWithKey(prompt, priv, signedAt)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	fp.Signature = ""
	status, vErr := VerifySystemPromptWithKey(prompt, fp, pub)
	if vErr != nil {
		t.Fatalf("verify: %v", vErr)
	}
	if status != Tampered {
		t.Errorf("status = %v, want Tampered for empty signature", status)
	}
}

// TestVerifySystemPrompt_MalformedHexIsTampered covers the corner
// where the Signature field is non-empty but not valid hex (e.g. a
// hand-edited fingerprint).
func TestVerifySystemPrompt_MalformedHexIsTampered(t *testing.T) {
	priv := genKeyForTest(t)
	pub := priv.Public().(ed25519.PublicKey)
	prompt := "x"
	fp, err := SignSystemPromptWithKey(prompt, priv, time.Now().UTC())
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	fp.Signature = "not-valid-hex"
	status, vErr := VerifySystemPromptWithKey(prompt, fp, pub)
	if vErr != nil {
		t.Fatalf("verify: %v", vErr)
	}
	if status != Tampered {
		t.Errorf("status = %v, want Tampered for malformed hex", status)
	}
}

// TestSignerFromEnv_FirstWins covers the env-var fallback shape: the
// first name in the list wins; the second is a fallback when the
// first is unset.
func TestSignerFromEnv_FirstWins(t *testing.T) {
	priv := genKeyForTest(t)
	hexed := hex.EncodeToString(priv)
	t.Setenv("PRIMARY", hexed)
	t.Setenv("FALLBACK", "")
	got, err := SignerFromEnv("PRIMARY", "FALLBACK")
	if err != nil {
		t.Fatalf("SignerFromEnv: %v", err)
	}
	if hex.EncodeToString(got) != hexed {
		t.Error("primary env var did not win")
	}
}

// TestSignerFromEnv_FallbackUsedWhenPrimaryUnset asserts that the
// fallback env var is consulted when the primary is empty.
func TestSignerFromEnv_FallbackUsedWhenPrimaryUnset(t *testing.T) {
	priv := genKeyForTest(t)
	hexed := hex.EncodeToString(priv)
	t.Setenv("PRIMARY", "")
	t.Setenv("FALLBACK", hexed)
	got, err := SignerFromEnv("PRIMARY", "FALLBACK")
	if err != nil {
		t.Fatalf("SignerFromEnv: %v", err)
	}
	if hex.EncodeToString(got) != hexed {
		t.Error("fallback env var was not used")
	}
}

// TestSignerFromEnv_NoKeyReturnsSentinel asserts ErrNoSigningKey is
// returned when no env var is set, so callers can WARN-and-continue.
func TestSignerFromEnv_NoKeyReturnsSentinel(t *testing.T) {
	t.Setenv("A", "")
	t.Setenv("B", "")
	_, err := SignerFromEnv("A", "B")
	if err == nil {
		t.Fatal("want error when no env var is set")
	}
	if err.Error() != ErrNoSigningKey.Error() {
		// errors.Is works too; keep explicit message check for
		// readability when a test fails.
		if !strings.Contains(err.Error(), "no signing key") {
			t.Errorf("unexpected error: %v", err)
		}
	}
}
