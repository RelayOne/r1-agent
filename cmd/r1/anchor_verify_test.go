package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/RelayOne/r1/internal/honestcrypto"
)

func writeAnchorJSON(t *testing.T, dir, name string, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// makeRecordAndProof builds a canonical record and anchors it via the active
// Anchorer, returning file paths for both.
func makeRecordAndProof(t *testing.T, dir string) (recordPath, proofPath, wantBackend string) {
	t.Helper()
	rec, err := honestcrypto.MakeRecord(honestcrypto.NewRecordInput{
		Subject: "org_1", Producer: "r1", Kind: "receipt.session",
		TS: "2026-07-08T00:00:00Z", Body: map[string]any{"session": "S1"}, PrevHash: nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	proof, err := honestcrypto.GetAnchorer().Anchor([]string{rec.Hash})
	if err != nil {
		t.Fatal(err)
	}
	return writeAnchorJSON(t, dir, "record.json", rec), writeAnchorJSON(t, dir, "proof.json", proof), proof.Backend
}

func TestRunAnchorVerify_DefaultBackend(t *testing.T) {
	honestcrypto.ResetAnchorer()
	defer honestcrypto.ResetAnchorer()

	dir := t.TempDir()
	recordPath, proofPath, backend := makeRecordAndProof(t, dir)
	if backend != honestcrypto.BackendTrustedTimestamp {
		t.Fatalf("default backend = %q", backend)
	}

	var out, errOut bytes.Buffer
	if code := runAnchorVerify(recordPath, proofPath, &out, &errOut); code != 0 {
		t.Fatalf("expected exit 0, got %d; stderr=%s", code, errOut.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("envelope: OK")) || !bytes.Contains(out.Bytes(), []byte("anchor[trusted-timestamp]: OK")) {
		t.Fatalf("unexpected output:\n%s", out.String())
	}
}

func TestRunAnchorVerify_TamperedRecordFails(t *testing.T) {
	honestcrypto.ResetAnchorer()
	defer honestcrypto.ResetAnchorer()

	dir := t.TempDir()
	recordPath, proofPath, _ := makeRecordAndProof(t, dir)

	// Tamper the record body without fixing the hash -> envelope check fails.
	var rec honestcrypto.Record
	if err := loadJSONFile(recordPath, &rec); err != nil {
		t.Fatal(err)
	}
	rec.Body = map[string]any{"session": "TAMPERED"}
	writeAnchorJSON(t, dir, "record.json", rec)

	var out, errOut bytes.Buffer
	if code := runAnchorVerify(recordPath, proofPath, &out, &errOut); code != 1 {
		t.Fatalf("tampered record must exit 1, got %d", code)
	}
}

// fakeStrongerCLIAnchorer stands in for a future operator-independent backend.
type fakeStrongerCLIAnchorer struct{}

func (fakeStrongerCLIAnchorer) Backend() string { return "fake-stronger" }
func (fakeStrongerCLIAnchorer) Anchor(hashes []string) (honestcrypto.AnchorProof, error) {
	inc := make([]any, len(hashes))
	for i, h := range hashes {
		inc[i] = h
	}
	return honestcrypto.AnchorProof{Backend: "fake-stronger", Root: "strong", Proof: map[string]any{"included": inc}, AnchoredAt: "2026-07-08T00:00:00Z"}, nil
}
func (fakeStrongerCLIAnchorer) Verify(h string, p honestcrypto.AnchorProof) bool {
	inc, _ := p.Proof["included"].([]any)
	for _, v := range inc {
		if s, _ := v.(string); s == h {
			return true
		}
	}
	return false
}
func (fakeStrongerCLIAnchorer) Upgrade(p honestcrypto.AnchorProof) (honestcrypto.AnchorProof, error) {
	return p, nil
}

// TestRunAnchorVerify_SwapProof proves the CLI verification path is unchanged when
// a stronger Anchorer backend is registered by config.
func TestRunAnchorVerify_SwapProof(t *testing.T) {
	honestcrypto.ResetAnchorer()
	defer honestcrypto.ResetAnchorer()

	honestcrypto.SetAnchorer(fakeStrongerCLIAnchorer{})
	dir := t.TempDir()
	recordPath, proofPath, backend := makeRecordAndProof(t, dir) // same helper, swapped backend
	if backend != "fake-stronger" {
		t.Fatalf("swapped backend = %q", backend)
	}

	var out, errOut bytes.Buffer
	if code := runAnchorVerify(recordPath, proofPath, &out, &errOut); code != 0 {
		t.Fatalf("swapped backend must still verify (exit 0), got %d; stderr=%s", code, errOut.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("anchor[fake-stronger]: OK")) {
		t.Fatalf("expected swapped backend in output:\n%s", out.String())
	}
}
