package ledger

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// seedChain writes n chained nodes into store under the given mission, each
// with parent_hash stamped via hashStructuralHeader of its predecessor. The
// returned slice is in chain order (index 0 is the root). Nodes are spaced 1
// second apart so CreatedAt ordering is unambiguous inside VerifyChain.
func seedChain(t *testing.T, store *Store, mission string, n int) []Node {
	t.Helper()
	base := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
	nodes := make([]Node, 0, n)
	var prev *Node
	for i := 0; i < n; i++ {
		body, _ := json.Marshal(map[string]any{"seq": i, "mission": mission})
		node := Node{
			Type:          "decision",
			SchemaVersion: 1,
			CreatedAt:     base.Add(time.Duration(i) * time.Second),
			CreatedBy:     "stance-test",
			MissionID:     mission,
			Content:       body,
		}
		if prev != nil {
			h, err := hashStructuralHeader(*prev)
			if err != nil {
				t.Fatalf("hashStructuralHeader: %v", err)
			}
			node.ParentHash = h
		}
		// T6: populate salt + content_commitment so computeID has a
		// well-formed commitment to hash into the chain ID. Deterministic
		// per-seq salt keeps this seeder reproducible without hitting
		// crypto/rand on every test iteration.
		node.Salt = "seed-salt-" + mission + "-" + time.Time{}.Add(time.Duration(i)*time.Millisecond).Format("150405")
		node.ContentCommitment = contentCommitment(node.Salt, node.Content)
		node.ID = computeID(node)
		if err := store.WriteNode(node); err != nil {
			t.Fatalf("WriteNode seq %d: %v", i, err)
		}
		nodes = append(nodes, node)
		n := node
		prev = &n
	}
	return nodes
}

func TestVerifyChain_Clean(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(root)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	seedChain(t, store, "m-clean", 20)

	if err := store.VerifyChain(context.Background()); err != nil {
		t.Fatalf("VerifyChain on clean 20-node chain returned error: %v", err)
	}
}

func TestVerifyChain_Redacted_StillValid(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(root)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	nodes := seedChain(t, store, "m-redacted", 20)

	// Crypto-shred node 10's content tier. The chain-tier file (nodes/{id}.json)
	// is left untouched so VerifyChain should still return nil.
	rec, err := store.Redact(context.Background(), nodes[10].ID, "retention_policy:test")
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if rec.NodeID != nodes[10].ID {
		t.Fatalf("RedactionRecord.NodeID = %q, want %q", rec.NodeID, nodes[10].ID)
	}

	if err := store.VerifyChain(context.Background()); err != nil {
		t.Fatalf("VerifyChain after redacting node 10 returned error: %v", err)
	}

	// Sanity: IsRedacted confirms the tombstone is in place, i.e. we really
	// did exercise the redaction path — not just skip it.
	red, err := store.IsRedacted(nodes[10].ID)
	if err != nil {
		t.Fatalf("IsRedacted: %v", err)
	}
	if !red {
		t.Fatal("IsRedacted returned false on redacted node; test setup drifted")
	}
}

func TestVerifyChain_Tampered_Fails(t *testing.T) {
	root := t.TempDir()
	store, err := NewStore(root)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	nodes := seedChain(t, store, "m-tampered", 20)

	// Flip a byte inside the on-disk chain entry for node 7. Any change to
	// the structural header (Type field, CreatedBy, MissionID, SchemaVersion)
	// changes hashStructuralHeader, which breaks node 8's parent_hash link.
	tamperID := nodes[7].ID
	path := filepath.Join(root, "chain", tamperID+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read tamper target: %v", err)
	}
	var cr chainRecord
	if err := json.Unmarshal(raw, &cr); err != nil {
		t.Fatalf("unmarshal tamper target: %v", err)
	}
	// Structural-header mutation: change CreatedBy. This is part of the
	// canonical-header hash input so it flips the predecessor hash and
	// invalidates node 8's ParentHash link.
	cr.CreatedBy = cr.CreatedBy + "-tampered"
	mutated, err := json.MarshalIndent(cr, "", "  ")
	if err != nil {
		t.Fatalf("remarshal tampered: %v", err)
	}
	if err := os.WriteFile(path, mutated, 0o644); err != nil {
		t.Fatalf("write tampered: %v", err)
	}

	err = store.VerifyChain(context.Background())
	if err == nil {
		t.Fatal("VerifyChain returned nil on tampered chain; want error naming the sequence")
	}

	// The error must name the sequence (mission "m-tampered", seq 8 — node 8
	// is the first one whose parent_hash no longer matches its predecessor).
	msg := err.Error()
	if !strings.Contains(msg, "m-tampered") {
		t.Errorf("error message missing mission id: %q", msg)
	}
	if !strings.Contains(msg, "seq 8") {
		t.Errorf("error message missing sequence index 'seq 8': %q", msg)
	}
}
