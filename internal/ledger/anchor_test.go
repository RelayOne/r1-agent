package ledger

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/honestcrypto"
)

func testNode(id, typ, parent string) Node {
	return Node{
		ID:                NodeID(id),
		Type:              typ,
		CreatedAt:         time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC),
		CreatedBy:         "worker-1",
		MissionID:         "m1",
		Content:           json.RawMessage(`{"k":"v"}`),
		ParentHash:        parent,
		ContentCommitment: "cc-" + id,
	}
}

func TestCanonicalRecordShapeAndChain(t *testing.T) {
	n0 := testNode("n0", "decision", "")
	r0, err := CanonicalRecord("org_1", n0)
	if err != nil {
		t.Fatal(err)
	}
	if r0.Producer != "r1" || r0.Kind != "ledger.decision" || r0.Schema != honestcrypto.Schema {
		t.Fatalf("unexpected envelope: producer=%q kind=%q schema=%q", r0.Producer, r0.Kind, r0.Schema)
	}
	if r0.PrevHash != nil {
		t.Fatal("genesis node must produce nil prevHash")
	}
	if !honestcrypto.VerifyRecordHash(r0) {
		t.Fatal("canonical record must self-verify")
	}
	// A node with a parent hash links the chain.
	n1 := testNode("n1", "review", r0.Hash)
	r1, err := CanonicalRecord("org_1", n1)
	if err != nil {
		t.Fatal(err)
	}
	if r1.PrevHash == nil || *r1.PrevHash != r0.Hash {
		t.Fatalf("prevHash must mirror ParentHash")
	}
	if idx := honestcrypto.VerifyChain([]honestcrypto.Record{r0, r1}); idx != -1 {
		t.Fatalf("linked records must verify as a chain, broke at %d", idx)
	}
}

func TestAnchorNodesThroughCanonicalSeam(t *testing.T) {
	honestcrypto.ResetAnchorer()
	defer honestcrypto.ResetAnchorer()

	nodes := []Node{testNode("a", "decision", ""), testNode("b", "review", ""), testNode("c", "note", "")}
	records, proof, err := AnchorNodes("org_1", nodes)
	if err != nil {
		t.Fatal(err)
	}
	if proof.Backend != honestcrypto.BackendTrustedTimestamp {
		t.Fatalf("default backend = %q", proof.Backend)
	}
	for _, r := range records {
		if !VerifyAnchoredRecord(r, proof) {
			t.Fatalf("record %s must verify inclusion under the anchor proof", r.RecordID)
		}
	}
}

// ledgerSwapAnchorer stands in for a stronger operator-independent backend.
type ledgerSwapAnchorer struct{}

func (ledgerSwapAnchorer) Backend() string { return "ledger-fake-stronger" }
func (ledgerSwapAnchorer) Anchor(hashes []string) (honestcrypto.AnchorProof, error) {
	inc := make([]any, len(hashes))
	for i, h := range hashes {
		inc[i] = h
	}
	return honestcrypto.AnchorProof{Backend: "ledger-fake-stronger", Root: "strong", Proof: map[string]any{"included": inc}, AnchoredAt: "2026-07-08T00:00:00Z"}, nil
}
func (ledgerSwapAnchorer) Verify(h string, p honestcrypto.AnchorProof) bool {
	inc, _ := p.Proof["included"].([]any)
	for _, v := range inc {
		if s, _ := v.(string); s == h {
			return true
		}
	}
	return false
}
func (ledgerSwapAnchorer) Upgrade(p honestcrypto.AnchorProof) (honestcrypto.AnchorProof, error) {
	return p, nil
}

// TestAnchorSwapProof proves the ledger's anchoring call site is unchanged when a
// stronger backend is registered by config.
func TestAnchorSwapProof(t *testing.T) {
	honestcrypto.ResetAnchorer()
	defer honestcrypto.ResetAnchorer()

	nodes := []Node{testNode("a", "decision", "")}
	_, proof, err := AnchorNodes("org_1", nodes) // identical call site
	if err != nil {
		t.Fatal(err)
	}
	if proof.Backend != honestcrypto.BackendTrustedTimestamp {
		t.Fatalf("default backend = %q", proof.Backend)
	}

	honestcrypto.SetAnchorer(ledgerSwapAnchorer{})
	records, proof, err := AnchorNodes("org_1", nodes) // SAME call site
	if err != nil {
		t.Fatal(err)
	}
	if proof.Backend != "ledger-fake-stronger" {
		t.Fatalf("after swap backend = %q", proof.Backend)
	}
	if !VerifyAnchoredRecord(records[0], proof) {
		t.Fatal("record must verify under swapped backend")
	}
}
