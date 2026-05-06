package ledger

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newSessionTestLedger(t *testing.T) (*Ledger, *Store) {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "ledger")
	led, err := New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	store, err := NewStore(root)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return led, store
}

func seedNode(t *testing.T, led *Ledger, mission string, body string) {
	t.Helper()
	_, err := led.AddNode(context.Background(), Node{
		Type:          "agent_io",
		SchemaVersion: 1,
		CreatedAt:     time.Now().UTC(),
		CreatedBy:     "test",
		MissionID:     mission,
		Content:       []byte(body),
	})
	if err != nil {
		t.Fatalf("AddNode(%s): %v", mission, err)
	}
}

func TestListNodesForSession_FiltersByMissionID(t *testing.T) {
	led, store := newSessionTestLedger(t)
	seedNode(t, led, "sess-A", `{"step":"a1"}`)
	seedNode(t, led, "sess-A", `{"step":"a2"}`)
	seedNode(t, led, "sess-B", `{"step":"b1"}`)

	got, err := store.ListNodesForSession("sess-A")
	if err != nil {
		t.Fatalf("ListNodesForSession A: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("session A: got %d nodes, want 2", len(got))
	}
	for _, n := range got {
		if n.MissionID != "sess-A" {
			t.Errorf("got mission %q, want sess-A", n.MissionID)
		}
	}
}

func TestListNodesForSession_EmptyReturnsAll(t *testing.T) {
	led, store := newSessionTestLedger(t)
	seedNode(t, led, "sess-A", `{"step":"a"}`)
	seedNode(t, led, "sess-B", `{"step":"b"}`)

	got, err := store.ListNodesForSession("")
	if err != nil {
		t.Fatalf("ListNodesForSession empty: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("empty session: got %d, want 2 (all)", len(got))
	}
}

func TestListNodesForSession_NonMatchingReturnsEmpty(t *testing.T) {
	led, store := newSessionTestLedger(t)
	seedNode(t, led, "sess-A", `{"step":"a"}`)

	got, err := store.ListNodesForSession("sess-NEVER")
	if err != nil {
		t.Fatalf("ListNodesForSession non-match: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("non-matching session: got %d, want 0", len(got))
	}
}

func TestChainRootHashForSession_StableForSameInput(t *testing.T) {
	led, store := newSessionTestLedger(t)
	seedNode(t, led, "sess-X", `{"step":"a"}`)
	seedNode(t, led, "sess-X", `{"step":"b"}`)

	h1, err := store.ChainRootHashForSession("sess-X")
	if err != nil || h1 == "" {
		t.Fatalf("hash 1: err=%v hash=%q", err, h1)
	}
	h2, err := store.ChainRootHashForSession("sess-X")
	if err != nil {
		t.Fatalf("hash 2: %v", err)
	}
	if h1 != h2 {
		t.Errorf("hash unstable: %s vs %s", h1, h2)
	}
}

func TestChainRootHashForSession_EmptySessionReturnsEmpty(t *testing.T) {
	_, store := newSessionTestLedger(t)
	h, err := store.ChainRootHashForSession("never-existed")
	if err != nil {
		t.Errorf("expected nil err for missing session, got %v", err)
	}
	if h != "" {
		t.Errorf("expected empty hash for missing session, got %s", h)
	}
}

func TestChainRootHashForSession_DifferentSessionsDifferentHashes(t *testing.T) {
	led, store := newSessionTestLedger(t)
	seedNode(t, led, "sess-A", `{"step":"a"}`)
	seedNode(t, led, "sess-B", `{"step":"b"}`)

	hA, _ := store.ChainRootHashForSession("sess-A")
	hB, _ := store.ChainRootHashForSession("sess-B")
	if hA == "" || hB == "" {
		t.Fatalf("expected non-empty hashes; got A=%q B=%q", hA, hB)
	}
	if hA == hB {
		t.Errorf("two distinct sessions should have distinct hashes; both = %s", hA)
	}
}

func TestCanonicalManifestSignBody_DeterministicForSameInput(t *testing.T) {
	a := CanonicalManifestSignBody("tracebundle", 2, "sess-1", "abc123", "2026-05-05T00:00:00Z", "fp-1")
	b := CanonicalManifestSignBody("tracebundle", 2, "sess-1", "abc123", "2026-05-05T00:00:00Z", "fp-1")
	if string(a) != string(b) {
		t.Errorf("CanonicalManifestSignBody not deterministic: %q vs %q", a, b)
	}
	c := CanonicalManifestSignBody("tracebundle", 2, "sess-2", "abc123", "2026-05-05T00:00:00Z", "fp-1")
	if string(a) == string(c) {
		t.Error("session_id swap should produce different canonical body")
	}
	if !strings.Contains(string(a), `"version":2`) {
		t.Errorf("canonical body should include version=2, got %s", a)
	}
}
