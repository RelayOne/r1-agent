package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/ledger/nodes"
	"github.com/RelayOne/r1/internal/promptguard"
	"github.com/RelayOne/r1/internal/r1dir"
)

// seedRepoWithFP stands up a temp repo containing a ledger with one
// SystemPromptFingerprint node signed against the env-configured key,
// and writes the prompt under .r1/promptguard/system_prompt.txt so
// the verify CLI can re-hash it. Returns the repo path.
func seedRepoWithFP(t *testing.T, prompt string, mutateSig bool) string {
	t.Helper()
	repo := t.TempDir()

	// Generate a fresh keypair and stash it as a hex env var so the
	// CLI's DefaultSigner/DefaultVerifier pick it up.
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519 gen: %v", err)
	}
	t.Setenv("R1_PROMPTGUARD_SIGNING_KEY", hex.EncodeToString(priv))

	// Sign the prompt and persist to the ledger.
	fp, err := promptguard.SignSystemPromptWithKey(prompt, priv, time.Now().UTC())
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if mutateSig {
		// Flip a byte in the signature so VerifySystemPrompt returns
		// Tampered.
		sigBytes, derr := hex.DecodeString(fp.Signature)
		if derr != nil {
			t.Fatalf("hex decode: %v", derr)
		}
		sigBytes[0] ^= 0x01
		fp.Signature = hex.EncodeToString(sigBytes)
	}
	node := nodes.SystemPromptFingerprint{
		PromptSHA256:   fp.PromptSHA256,
		KeyFingerprint: fp.KeyFingerprint,
		Signature:      fp.Signature,
		SignedAt:       fp.SignedAt,
		AssemblyTrace:  "test",
		Version:        1,
	}
	content, err := json.Marshal(node)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	lg, err := ledger.New(r1dir.CanonicalPathFor(repo, "ledger"))
	if err != nil {
		t.Fatalf("ledger.New: %v", err)
	}
	t.Cleanup(func() { _ = lg.Close() })
	if _, err := lg.AddNode(context.Background(), ledger.Node{
		Type:          "system_prompt_fingerprint",
		SchemaVersion: 1,
		CreatedBy:     "test seed",
		Content:       content,
		CreatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatalf("AddNode: %v", err)
	}

	// Write the prompt to the conventional verify-CLI path.
	dir := r1dir.CanonicalPathFor(repo, "promptguard")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "system_prompt.txt"), []byte(prompt), 0o600); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	return repo
}

// TestVerifySystemPromptCmd_VerifiedExit0 covers the happy path: signed
// prompt unchanged on disk → exit 0, stdout reports Verified.
func TestVerifySystemPromptCmd_VerifiedExit0(t *testing.T) {
	repo := seedRepoWithFP(t, "the assembled system prompt", false)
	var stdout, stderr bytes.Buffer
	code := runVerifySystemPromptCmd([]string{"--repo", repo}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Verified") {
		t.Errorf("stdout missing Verified: %s", stdout.String())
	}
}

// TestVerifySystemPromptCmd_TamperedExit2 asserts the operator-
// distinguishable Tampered surface: exit 2, stdout names Tampered.
func TestVerifySystemPromptCmd_TamperedExit2(t *testing.T) {
	repo := seedRepoWithFP(t, "the assembled system prompt", true)
	var stdout, stderr bytes.Buffer
	code := runVerifySystemPromptCmd([]string{"--repo", repo}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit=%d, want 2; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "Tampered") {
		t.Errorf("stdout missing Tampered: %s", stdout.String())
	}
}

// TestVerifySystemPromptCmd_ModifiedExit0 mutates the on-disk prompt
// after sealing the fingerprint. Verify must surface Modified at exit 0
// (operator-actionable; not a fatal Tampered).
func TestVerifySystemPromptCmd_ModifiedExit0(t *testing.T) {
	repo := seedRepoWithFP(t, "the assembled system prompt", false)
	// Rewrite the on-disk prompt with extra bytes.
	if err := os.WriteFile(
		filepath.Join(r1dir.CanonicalPathFor(repo, "promptguard"), "system_prompt.txt"),
		[]byte("the assembled system prompt, modified"),
		0o600,
	); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	var stdout, stderr bytes.Buffer
	code := runVerifySystemPromptCmd([]string{"--repo", repo}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d, want 0; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "Modified") {
		t.Errorf("stdout missing Modified: %s", stdout.String())
	}
}

// TestPromptguardCmd_DispatchUnknownVerb covers the verb-dispatch
// surface: an unrecognized verb exits 2 and surfaces a usage error.
func TestPromptguardCmd_DispatchUnknownVerb(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runPromptguardCmd([]string{"do-something-weird"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit=%d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown verb") {
		t.Errorf("stderr=%s", stderr.String())
	}
}
