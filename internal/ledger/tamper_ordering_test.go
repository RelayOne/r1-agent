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

// --- Gap 1: content-tier tamper detection (fail-closed on read) ---

func writeValidNode(t *testing.T, s *Store, id, mission, salt string, content json.RawMessage) {
	t.Helper()
	n := Node{
		ID:                id,
		Type:              "note",
		SchemaVersion:     1,
		CreatedAt:         time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		CreatedBy:         "tester",
		MissionID:         mission,
		Salt:              salt,
		Content:           content,
		ContentCommitment: ComputeContentCommitment(salt, content),
	}
	if err := s.WriteNode(n); err != nil {
		t.Fatalf("WriteNode(%s): %v", id, err)
	}
}

func TestReadNode_RejectsTamperedContent(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	writeValidNode(t, s, "note-1", "m", "0011223344556677", json.RawMessage(`{"secret":"original"}`))

	// Clean read succeeds and returns the original content.
	got, err := s.ReadNode("note-1")
	if err != nil {
		t.Fatalf("clean ReadNode: %v", err)
	}
	if !strings.Contains(string(got.Content), "original") {
		t.Fatalf("clean content = %s", got.Content)
	}

	// Tamper the content tier: swap the payload VALUE, leaving the chain
	// tier (and its commitment) untouched — exactly the attack the
	// commitment is meant to catch.
	cp := filepath.Join(dir, "content", "note-1.json")
	tampered := []byte(`{"salt":"0011223344556677","content":{"secret":"EVIL"}}`)
	if err := os.WriteFile(cp, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadNode("note-1"); err == nil {
		t.Fatal("tampered content was accepted; expected fail-closed rejection")
	} else if !strings.Contains(err.Error(), "tamper") {
		t.Fatalf("unexpected error (want tamper): %v", err)
	}
}

func TestReadNode_TamperedSaltRejected(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)
	writeValidNode(t, s, "note-2", "m", "aaaaaaaaaaaaaaaa", json.RawMessage(`{"v":1}`))
	// Change only the salt: commitment binds salt too, so this must fail.
	cp := filepath.Join(dir, "content", "note-2.json")
	if err := os.WriteFile(cp, []byte(`{"salt":"bbbbbbbbbbbbbbbb","content":{"v":1}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadNode("note-2"); err == nil {
		t.Fatal("tampered salt accepted; expected rejection")
	}
}

func TestReadNode_AcceptsBenignReformatting(t *testing.T) {
	// A whitespace/indentation-only reformat of the content tier is NOT a
	// value change and must NOT trip the commitment check (the commitment is
	// canonical). This guards against the fix rejecting the very reformatting
	// the content tier undergoes on write.
	dir := t.TempDir()
	s, _ := NewStore(dir)
	writeValidNode(t, s, "note-3", "m", "cccccccccccccccc", json.RawMessage(`{"a":1,"b":2}`))
	cp := filepath.Join(dir, "content", "note-3.json")
	reformatted := []byte("{\n  \"salt\": \"cccccccccccccccc\",\n  \"content\": {\n    \"a\": 1,\n    \"b\": 2\n  }\n}")
	if err := os.WriteFile(cp, reformatted, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadNode("note-3"); err != nil {
		t.Fatalf("benign reformat rejected: %v", err)
	}
}

func TestReadNode_LegacyEmptyCommitmentAccepted(t *testing.T) {
	// A node whose chain tier predates the commitment scheme (empty
	// ContentCommitment) has nothing to verify against and must still read.
	dir := t.TempDir()
	s, _ := NewStore(dir)
	n := Node{
		ID: "legacy-1", Type: "note", SchemaVersion: 1,
		CreatedAt: time.Now().UTC(), CreatedBy: "old",
		Content: json.RawMessage(`{"legacy":true}`),
		// ContentCommitment intentionally empty.
	}
	if err := s.WriteNode(n); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadNode("legacy-1"); err != nil {
		t.Fatalf("legacy empty-commitment node rejected: %v", err)
	}
}

func TestChainRootHashForSession_SurvivesContentTamper(t *testing.T) {
	// The chain root is a STRUCTURAL (chain-tier) quantity: it must remain
	// computable even when a node's content tier is tampered or absent —
	// content-tier integrity is enforced separately by ReadNode.
	dir := t.TempDir()
	s, _ := NewStore(dir)
	writeValidNode(t, s, "n-1", "sess", "1111111111111111", json.RawMessage(`{"i":1}`))
	writeValidNode(t, s, "n-2", "sess", "2222222222222222", json.RawMessage(`{"i":2}`))
	before, err := s.ChainRootHashForSession("sess")
	if err != nil || before == "" {
		t.Fatalf("root before tamper: %q err=%v", before, err)
	}
	// Corrupt one content blob.
	if err := os.WriteFile(filepath.Join(dir, "content", "n-1.json"),
		[]byte(`{"salt":"1111111111111111","content":{"i":999}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// ReadNode now fails closed for the tampered node...
	if _, err := s.ReadNode("n-1"); err == nil {
		t.Fatal("tampered node read without error")
	}
	// ...but the structural chain root still computes and is unchanged.
	after, err := s.ChainRootHashForSession("sess")
	if err != nil {
		t.Fatalf("root after tamper errored: %v", err)
	}
	if after != before {
		t.Fatalf("structural chain root changed after content tamper: %s -> %s", before, after)
	}
}

// --- Gap 2: equal-timestamp total-order (linkage <-> verification) ---

func TestVerifyChain_EqualTimestamp_TotalOrderEnforced(t *testing.T) {
	ts := time.Date(2026, 3, 3, 12, 0, 0, 0, time.UTC)
	mk := func(id, cmt string) Node {
		return Node{ID: id, Type: "n", SchemaVersion: 1, CreatedAt: ts, CreatedBy: "x", MissionID: "m", ContentCommitment: cmt}
	}
	// Distinct commitments => distinct structural headers => the parent-hash
	// links actually discriminate order. IDs n-1<n-2<n-3 are the total order.
	a := mk("n-1", "cmt-a")
	b := mk("n-2", "cmt-b")
	c := mk("n-3", "cmt-c")
	ha, _ := hashStructuralHeader(a)
	hb, _ := hashStructuralHeader(b)
	b.ParentHash = ha
	c.ParentHash = hb

	// Valid chain: written in a shuffled order to prove verification depends
	// on the (CreatedAt, ID) total order, not on write/enumeration order.
	dir := t.TempDir()
	s, _ := NewStore(dir)
	for _, n := range []Node{c, a, b} {
		if err := s.WriteNode(n); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 20; i++ { // determinism: same verdict every run
		if err := s.VerifyChain(context.Background()); err != nil {
			t.Fatalf("valid equal-timestamp chain rejected (iter %d): %v", i, err)
		}
	}

	// Reordered chain: c links straight to a, skipping b. Under the total
	// order [a,b,c], c's predecessor is b, so this MUST be rejected.
	dir2 := t.TempDir()
	s2, _ := NewStore(dir2)
	cBad := mk("n-3", "cmt-c")
	cBad.ParentHash = ha // wrong: should be hb
	for _, n := range []Node{a, b, cBad} {
		if err := s2.WriteNode(n); err != nil {
			t.Fatal(err)
		}
	}
	if err := s2.VerifyChain(context.Background()); err == nil {
		t.Fatal("reordered equal-timestamp chain accepted; expected rejection")
	}
}

func TestLinkage_EqualTimestamp_PicksIDMaxParent(t *testing.T) {
	// AddNode must choose the predecessor by the SAME (CreatedAt, ID) total
	// order verification uses. With equal timestamps that means the ID-max of
	// the existing nodes — deterministically, not whatever the index's
	// unspecified equal-timestamp row order happened to yield.
	dir := t.TempDir()
	l, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ctx := context.Background()
	ts := time.Date(2026, 4, 4, 0, 0, 0, 0, time.UTC)
	add := func(tag string) NodeID {
		id, err := l.AddNode(ctx, Node{
			Type: "n", SchemaVersion: 1, CreatedAt: ts, MissionID: "m",
			Content: json.RawMessage(`{"tag":"` + tag + `"}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	id0 := add("zero")
	id1 := add("one")
	id2 := add("two") // its parent should be max(id0, id1) by ID

	wantParent := id0
	if id1 > id0 {
		wantParent = id1
	}
	pNode, err := l.ReadNode(wantParent)
	if err != nil {
		t.Fatal(err)
	}
	wantHash, _ := hashStructuralHeader(pNode)
	n2, err := l.ReadNode(id2)
	if err != nil {
		t.Fatal(err)
	}
	if n2.ParentHash != wantHash {
		t.Fatalf("linkage did not pick ID-max parent: n2.ParentHash=%s want hash(%s)=%s", n2.ParentHash, wantParent, wantHash)
	}
	// NOTE: we deliberately do NOT assert the whole AddNode-built chain
	// verifies here. For EXACTLY-equal timestamps an append-only chain cannot
	// in general form a linear chain in (CreatedAt, ID) order (a later,
	// smaller-ID node forks it). The tiebreak's guarantee is a DETERMINISTIC
	// total order shared by linkage/verify/index — not that arbitrary
	// out-of-order equal-timestamp appends reconcile. Production CreatedAt is
	// monotonic wall-clock, so real chains are strictly ordered; see
	// TestLinkage_MonotonicTimestamps_Verifies for that path.
}

func TestLinkage_MonotonicTimestamps_Verifies(t *testing.T) {
	// The normal path: distinct (monotonic) timestamps. The chain AddNode
	// builds must verify, and the tiebreak must not disturb it.
	dir := t.TempDir()
	l, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ctx := context.Background()
	base := time.Date(2026, 6, 6, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 8; i++ {
		if _, err := l.AddNode(ctx, Node{
			Type: "n", SchemaVersion: 1, CreatedAt: base.Add(time.Duration(i) * time.Millisecond),
			MissionID: "m", Content: json.RawMessage(`{"i":` + string(rune('0'+i)) + `}`),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.store.VerifyChain(ctx); err != nil {
		t.Fatalf("monotonic-timestamp chain failed verify: %v", err)
	}
}

func TestQueryNodes_EqualTimestamp_DeterministicIDOrder(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ctx := context.Background()
	ts := time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC)
	for _, tag := range []string{"a", "b", "c", "d"} {
		if _, err := l.AddNode(ctx, Node{
			Type: "n", SchemaVersion: 1, CreatedAt: ts, MissionID: "m",
			Content: json.RawMessage(`{"t":"` + tag + `"}`),
		}); err != nil {
			t.Fatal(err)
		}
	}
	var first []NodeID
	for i := 0; i < 5; i++ {
		ids, err := l.QueryNodes(QueryFilter{MissionID: "m"})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = ids
			// Must be sorted ascending by ID (the equal-timestamp tiebreak).
			for j := 1; j < len(ids); j++ {
				if ids[j-1] > ids[j] {
					t.Fatalf("QueryNodes not ID-sorted on equal timestamps: %v", ids)
				}
			}
			continue
		}
		if strings.Join(ids, ",") != strings.Join(first, ",") {
			t.Fatalf("QueryNodes order not deterministic: %v vs %v", first, ids)
		}
	}
}
