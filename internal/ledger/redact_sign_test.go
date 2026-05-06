package ledger

import (
	"context"
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newSignTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	for _, sub := range []string{"nodes", "chain", "content", "edges", "index"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store, dir
}

func TestLoadOrGenerateSigningKey_FreshGenerate(t *testing.T) {
	dir := t.TempDir()
	priv, fp, err := LoadOrGenerateSigningKey(dir)
	if err != nil {
		t.Fatalf("LoadOrGenerateSigningKey: %v", err)
	}
	if len(priv) != ed25519.PrivateKeySize {
		t.Errorf("priv size = %d, want %d", len(priv), ed25519.PrivateKeySize)
	}
	if len(fp) != 12 {
		t.Errorf("fingerprint length = %d, want 12 hex chars", len(fp))
	}
	if _, err := os.Stat(filepath.Join(dir, "redactions", signingPrivFile)); err != nil {
		t.Errorf("priv file not persisted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "redactions", signingPubFile)); err != nil {
		t.Errorf("pub file not persisted: %v", err)
	}
}

func TestLoadOrGenerateSigningKey_StableAcrossCalls(t *testing.T) {
	dir := t.TempDir()
	priv1, fp1, err := LoadOrGenerateSigningKey(dir)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	priv2, fp2, err := LoadOrGenerateSigningKey(dir)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if string(priv1) != string(priv2) {
		t.Error("private key should be stable across calls")
	}
	if fp1 != fp2 {
		t.Errorf("fingerprint = %s vs %s, should be stable", fp1, fp2)
	}
}

func TestSignRecord_RejectsEmptyFields(t *testing.T) {
	dir := t.TempDir()
	priv, _, _ := LoadOrGenerateSigningKey(dir)
	cases := []SignedRedactionEvent{
		{NodeID: "", RedactedAt: "2026-05-05T00:00:00Z", Reason: "x"},
		{NodeID: "n", RedactedAt: "", Reason: "x"},
		{NodeID: "n", RedactedAt: "2026-05-05T00:00:00Z", Reason: ""},
	}
	for i, c := range cases {
		if _, err := SignRecord(c, priv); err == nil {
			t.Errorf("case %d: expected validation error, got nil", i)
		}
	}
}

func TestSignRecord_RoundtripVerifies(t *testing.T) {
	dir := t.TempDir()
	priv, fp, _ := LoadOrGenerateSigningKey(dir)
	pub := priv.Public().(ed25519.PublicKey)
	signed, err := SignRecord(SignedRedactionEvent{
		NodeID:     "n-1",
		RedactedAt: "2026-05-05T12:00:00Z",
		Reason:     "retention_policy",
	}, priv)
	if err != nil {
		t.Fatalf("SignRecord: %v", err)
	}
	if signed.Signer != fp {
		t.Errorf("Signer = %q, want %q", signed.Signer, fp)
	}
	if signed.SignatureHex == "" {
		t.Error("SignatureHex should be populated")
	}
	if err := VerifyRecord(signed, pub); err != nil {
		t.Errorf("Verify on freshly-signed record: %v", err)
	}
}

func TestVerifyRecord_DetectsTamperedReason(t *testing.T) {
	dir := t.TempDir()
	priv, _, _ := LoadOrGenerateSigningKey(dir)
	pub := priv.Public().(ed25519.PublicKey)
	signed, _ := SignRecord(SignedRedactionEvent{
		NodeID:     "n-1",
		RedactedAt: "2026-05-05T12:00:00Z",
		Reason:     "retention_policy",
	}, priv)
	signed.Reason = "operator_request" // tamper
	err := VerifyRecord(signed, pub)
	if !errors.Is(err, ErrSignatureMismatch) {
		t.Errorf("expected ErrSignatureMismatch, got %v", err)
	}
}

func TestVerifyRecord_DetectsTamperedSigner(t *testing.T) {
	dir := t.TempDir()
	priv, _, _ := LoadOrGenerateSigningKey(dir)
	pub := priv.Public().(ed25519.PublicKey)
	signed, _ := SignRecord(SignedRedactionEvent{
		NodeID: "n-1", RedactedAt: "2026-05-05T12:00:00Z", Reason: "retention_policy",
	}, priv)
	signed.Signer = "deadbeef0042"
	err := VerifyRecord(signed, pub)
	if !errors.Is(err, ErrSignatureMismatch) {
		t.Errorf("expected ErrSignatureMismatch on swapped Signer, got %v", err)
	}
}

func TestVerifyRecord_UnsignedReturnsErrUnsigned(t *testing.T) {
	dir := t.TempDir()
	priv, _, _ := LoadOrGenerateSigningKey(dir)
	pub := priv.Public().(ed25519.PublicKey)
	rec := SignedRedactionEvent{NodeID: "n-1", RedactedAt: "2026-05-05T12:00:00Z", Reason: "x"}
	if err := VerifyRecord(rec, pub); !errors.Is(err, ErrUnsigned) {
		t.Errorf("expected ErrUnsigned, got %v", err)
	}
}

func TestRedactAndLog_AutoSigns(t *testing.T) {
	store, _ := newSignTestStore(t)
	chainPath := filepath.Join(store.chainDirFor(), "n-test.json")
	if err := os.WriteFile(chainPath, []byte(`{"id":"n-test"}`), 0o644); err != nil {
		t.Fatalf("seed chain: %v", err)
	}
	contentPath := filepath.Join(store.contentDirFor(), "n-test.json")
	if err := os.WriteFile(contentPath, []byte(`{"content":"sensitive"}`), 0o644); err != nil {
		t.Fatalf("seed content: %v", err)
	}
	ev, err := store.RedactAndLog(context.Background(), "n-test", "retention_policy")
	if err != nil {
		t.Fatalf("RedactAndLog: %v", err)
	}
	if ev.Signer == "" || ev.SignatureHex == "" {
		t.Errorf("RedactAndLog should auto-sign; got Signer=%q SignatureHex=%q", ev.Signer, ev.SignatureHex)
	}
}

func TestRedactionsForVerified_FlagsTampered(t *testing.T) {
	store, root := newSignTestStore(t)
	chainPath := filepath.Join(store.chainDirFor(), "n-1.json")
	_ = os.WriteFile(chainPath, []byte(`{"id":"n-1"}`), 0o644)
	contentPath := filepath.Join(store.contentDirFor(), "n-1.json")
	_ = os.WriteFile(contentPath, []byte(`{"content":"x"}`), 0o644)
	if _, err := store.RedactAndLog(context.Background(), "n-1", "retention_policy"); err != nil {
		t.Fatalf("RedactAndLog: %v", err)
	}
	// Tamper with the on-disk log file.
	logPath := filepath.Join(root, "redactions", "n-1.ndjson")
	raw, _ := os.ReadFile(logPath)
	tampered := strings.Replace(string(raw), "retention_policy", "operator_request", 1)
	if err := os.WriteFile(logPath, []byte(tampered), 0o644); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	verified, err := store.RedactionsForVerified("n-1")
	if err != nil {
		t.Fatalf("RedactionsForVerified: %v", err)
	}
	if len(verified) != 1 {
		t.Fatalf("got %d events, want 1", len(verified))
	}
	if verified[0].Verified {
		t.Error("tampered entry should flag Verified=false")
	}
	if verified[0].VerifyErr == "" {
		t.Error("VerifyErr should be populated for tampered entry")
	}
}

func TestRedactionsForVerified_HappyPath(t *testing.T) {
	store, _ := newSignTestStore(t)
	chainPath := filepath.Join(store.chainDirFor(), "n-clean.json")
	_ = os.WriteFile(chainPath, []byte(`{"id":"n-clean"}`), 0o644)
	contentPath := filepath.Join(store.contentDirFor(), "n-clean.json")
	_ = os.WriteFile(contentPath, []byte(`{"content":"x"}`), 0o644)
	if _, err := store.RedactAndLog(context.Background(), "n-clean", "retention_policy"); err != nil {
		t.Fatalf("RedactAndLog: %v", err)
	}
	verified, err := store.RedactionsForVerified("n-clean")
	if err != nil {
		t.Fatalf("RedactionsForVerified: %v", err)
	}
	if len(verified) != 1 || !verified[0].Verified {
		t.Errorf("expected one verified entry, got %+v", verified)
	}
}
