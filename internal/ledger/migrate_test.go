package ledger

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newMigrateLedger uses the same root-dir pattern as
// newTestLedger in ledger_test.go but named differently to
// avoid the same-file redeclare conflict.
func newMigrateLedger(t *testing.T) *Ledger {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "ledger")
	l, err := New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		_ = l.Close()
		_ = os.RemoveAll(dir)
	})
	return l
}

func TestAddNode_SetsParentHash_SecondInMission(t *testing.T) {
	l := newMigrateLedger(t)
	ctx := context.Background()
	// First node in mission — no predecessor, ParentHash
	// stays empty.
	firstID, err := l.AddNode(ctx, Node{
		Type: "decision_internal", SchemaVersion: 1,
		MissionID: "m-1",
		Content:   json.RawMessage(`{"x":1}`),
		CreatedAt: time.Date(2026, 4, 16, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("AddNode 1: %v", err)
	}
	first, _ := l.Get(ctx, firstID)
	if first.ParentHash != "" {
		t.Errorf("first-in-mission ParentHash should be empty, got %q", first.ParentHash)
	}

	// Second node — ParentHash should be set to SHA256 of
	// first node's canonical JSON.
	secondID, err := l.AddNode(ctx, Node{
		Type: "decision_internal", SchemaVersion: 1,
		MissionID: "m-1",
		Content:   json.RawMessage(`{"x":2}`),
		CreatedAt: time.Date(2026, 4, 16, 10, 1, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("AddNode 2: %v", err)
	}
	second, _ := l.Get(ctx, secondID)
	if second.ParentHash == "" {
		t.Fatal("second-in-mission ParentHash must be populated")
	}
	expected, err := hashNode(*first)
	if err != nil {
		t.Fatalf("hashNode: %v", err)
	}
	if second.ParentHash != expected {
		t.Errorf("ParentHash=%s want %s", second.ParentHash, expected)
	}
}

func TestAddNode_MissionsIndependent(t *testing.T) {
	l := newMigrateLedger(t)
	ctx := context.Background()
	// First node in m-1.
	_, _ = l.AddNode(ctx, Node{
		Type: "decision_internal", SchemaVersion: 1,
		MissionID: "m-1", Content: json.RawMessage(`{"a":1}`),
		CreatedAt: time.Now(),
	})
	// First node in m-2 — separate mission, no predecessor.
	id2, _ := l.AddNode(ctx, Node{
		Type: "decision_internal", SchemaVersion: 1,
		MissionID: "m-2", Content: json.RawMessage(`{"b":1}`),
		CreatedAt: time.Now().Add(time.Second),
	})
	n2, _ := l.Get(ctx, id2)
	if n2.ParentHash != "" {
		t.Errorf("cross-mission: first-in-m2 should have empty ParentHash, got %q", n2.ParentHash)
	}
}

func TestAddNode_CallerProvidedParentHashPreserved(t *testing.T) {
	l := newMigrateLedger(t)
	ctx := context.Background()
	caller := "caller-supplied-hash"
	id, err := l.AddNode(ctx, Node{
		Type: "decision_internal", SchemaVersion: 1,
		MissionID:  "m-1",
		Content:    json.RawMessage(`{"x":1}`),
		CreatedAt:  time.Now(),
		ParentHash: caller,
	})
	if err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	n, _ := l.Get(ctx, id)
	if n.ParentHash != caller {
		t.Errorf("caller-supplied ParentHash overwritten: %q", n.ParentHash)
	}
}

func TestMigrateParentHash_ReportsLegacyNodes(t *testing.T) {
	l := newMigrateLedger(t)
	ctx := context.Background()
	// Add 3 nodes in m-1 with auto-populated ParentHash.
	for i := 0; i < 3; i++ {
		_, _ = l.AddNode(ctx, Node{
			Type: "decision_internal", SchemaVersion: 1,
			MissionID: "m-1",
			Content:   json.RawMessage(`{"i":` + string(rune('0'+i)) + `}`),
			CreatedAt: time.Date(2026, 4, 16, 10, i, 0, 0, time.UTC),
		})
	}
	report, err := MigrateParentHash(ctx, l, true)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if report.MissionsScanned != 1 {
		t.Errorf("MissionsScanned=%d want 1", report.MissionsScanned)
	}
	if report.NodesVisited != 3 {
		t.Errorf("NodesVisited=%d want 3", report.NodesVisited)
	}
	// 2nd + 3rd should be counted as skipped (already have
	// ParentHash); 1st is first-in-mission which is counted
	// as "no predecessor" (not skipped, not updated).
	if report.NodesSkipped != 2 {
		t.Errorf("NodesSkipped=%d want 2 (2nd and 3rd already linked)", report.NodesSkipped)
	}
}

func TestVerifyChain_HappyPath(t *testing.T) {
	l := newMigrateLedger(t)
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		_, _ = l.AddNode(ctx, Node{
			Type: "decision_internal", SchemaVersion: 1,
			MissionID: "m-1",
			Content:   json.RawMessage(`{"n":` + string(rune('0'+i)) + `}`),
			CreatedAt: time.Date(2026, 4, 16, 10, i, 0, 0, time.UTC),
		})
	}
	breaks, err := VerifyChain(ctx, l)
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if len(breaks) != 0 {
		t.Errorf("expected 0 chain breaks on freshly-built ledger, got %+v", breaks)
	}
}

func TestHashNode_Deterministic(t *testing.T) {
	n := Node{
		ID: "abc", Type: "x", SchemaVersion: 1,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Content:   json.RawMessage(`{"a":1}`),
	}
	h1, err := hashNode(n)
	if err != nil {
		t.Fatalf("hashNode: %v", err)
	}
	h2, _ := hashNode(n)
	if h1 != h2 {
		t.Errorf("hash non-deterministic: %s vs %s", h1, h2)
	}
	// ParentHash should NOT affect the hash (chicken-and-egg
	// guard).
	n.ParentHash = "anything"
	h3, _ := hashNode(n)
	if h3 != h1 {
		t.Errorf("ParentHash should not affect hashNode; got %s vs %s", h3, h1)
	}
}

func TestMigrateParentHash_NilLedgerErrors(t *testing.T) {
	_, err := MigrateParentHash(context.Background(), nil, false)
	if err == nil {
		t.Error("nil ledger should error")
	}
}

// TestMigrate_PreexistingNodes seeds 3 old-format node files under nodes/
// and asserts that opening a Store triggers the one-shot T6 migration:
// chain/ and content/ get populated, and nodes/ is renamed to nodes.bak/.
func TestMigrate_PreexistingNodes(t *testing.T) {
	root := t.TempDir()

	// Stage a pre-T6 layout directly on disk: three node files under nodes/
	// and nothing under chain/ or content/ yet.
	nodesDir := filepath.Join(root, "nodes")
	if err := os.MkdirAll(nodesDir, 0o755); err != nil {
		t.Fatalf("mkdir nodes: %v", err)
	}
	seed := []Node{
		{
			ID:            "legacy-aaa11111",
			Type:          "decision",
			SchemaVersion: 1,
			CreatedAt:     time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
			CreatedBy:     "stance-a",
			MissionID:     "m-1",
			Content:       json.RawMessage(`{"text":"one"}`),
		},
		{
			ID:            "legacy-bbb22222",
			Type:          "decision",
			SchemaVersion: 1,
			CreatedAt:     time.Date(2025, 1, 1, 0, 1, 0, 0, time.UTC),
			CreatedBy:     "stance-a",
			MissionID:     "m-1",
			Content:       json.RawMessage(`{"text":"two"}`),
		},
		{
			ID:            "legacy-ccc33333",
			Type:          "task",
			SchemaVersion: 1,
			CreatedAt:     time.Date(2025, 1, 1, 0, 2, 0, 0, time.UTC),
			CreatedBy:     "stance-b",
			MissionID:     "m-2",
			Content:       json.RawMessage(`{"text":"three"}`),
		},
	}
	for _, n := range seed {
		raw, err := json.MarshalIndent(n, "", "  ")
		if err != nil {
			t.Fatalf("marshal seed %s: %v", n.ID, err)
		}
		if err := os.WriteFile(filepath.Join(nodesDir, n.ID+".json"), raw, 0o644); err != nil {
			t.Fatalf("write seed %s: %v", n.ID, err)
		}
	}

	// Opening the store triggers migrateNodesToChainContent exactly once.
	store, err := NewStore(root)
	if err != nil {
		t.Fatalf("NewStore triggered migration error: %v", err)
	}
	_ = store

	// After migration: nodes.bak/ exists and contains all three legacy files.
	backupDir := filepath.Join(root, "nodes.bak")
	backupEntries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatalf("nodes.bak not created: %v", err)
	}
	if len(backupEntries) != len(seed) {
		t.Errorf("nodes.bak has %d entries, want %d", len(backupEntries), len(seed))
	}

	// nodes/ itself must be gone (renamed).
	if _, err := os.Stat(nodesDir); !os.IsNotExist(err) {
		t.Errorf("nodes/ still present after migration (err=%v)", err)
	}

	// chain/ and content/ each hold one file per seed.
	chainEntries, err := os.ReadDir(filepath.Join(root, "chain"))
	if err != nil {
		t.Fatalf("read chain dir: %v", err)
	}
	if len(chainEntries) != len(seed) {
		t.Errorf("chain/ has %d entries, want %d", len(chainEntries), len(seed))
	}
	contentEntries, err := os.ReadDir(filepath.Join(root, "content"))
	if err != nil {
		t.Fatalf("read content dir: %v", err)
	}
	if len(contentEntries) != len(seed) {
		t.Errorf("content/ has %d entries, want %d", len(contentEntries), len(seed))
	}

	// ReadNode returns the migrated payload for each original ID.
	for _, n := range seed {
		got, err := store.ReadNode(n.ID)
		if err != nil {
			t.Fatalf("ReadNode %s after migration: %v", n.ID, err)
		}
		if got.ID != n.ID {
			t.Errorf("ID changed during migration: %q -> %q", n.ID, got.ID)
		}
		if got.Type != n.Type {
			t.Errorf("Type for %s: got %q want %q", n.ID, got.Type, n.Type)
		}
		// Content round-trips through JSON indent during migration write,
		// so compare by parsed value rather than raw bytes.
		var wantVal, gotVal map[string]any
		if err := json.Unmarshal(n.Content, &wantVal); err != nil {
			t.Fatalf("unmarshal want content: %v", err)
		}
		if err := json.Unmarshal(got.Content, &gotVal); err != nil {
			t.Fatalf("unmarshal got content: %v", err)
		}
		wantBytes, _ := json.Marshal(wantVal)
		gotBytes, _ := json.Marshal(gotVal)
		if string(wantBytes) != string(gotBytes) {
			t.Errorf("Content for %s: got %s want %s", n.ID, gotBytes, wantBytes)
		}
		if got.ContentCommitment == "" {
			t.Errorf("ContentCommitment not synthesized during migration for %s", n.ID)
		}
		if got.Salt == "" {
			t.Errorf("Salt not synthesized during migration for %s", n.ID)
		}
	}

	// Re-opening the store is a no-op: no panic, no error, nodes.bak left
	// alone. (Second-open safety matters because operators may restart the
	// process while leaving nodes.bak in place as inspection evidence.)
	store2, err := NewStore(root)
	if err != nil {
		t.Fatalf("second NewStore after migration: %v", err)
	}
	_ = store2
	if _, err := os.Stat(backupDir); err != nil {
		t.Errorf("nodes.bak disappeared on second open: %v", err)
	}
}
