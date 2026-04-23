package ledger

import (
	"sync"
	"testing"
	"time"
)

func TestLedgerAppendHook_FiresOnWriteNode(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	var mu sync.Mutex
	var got []LedgerAppendEvent
	SetLedgerAppendHook(func(ev LedgerAppendEvent) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, ev)
	})
	t.Cleanup(func() { SetLedgerAppendHook(nil) })

	n := Node{
		ID:         "node-abc",
		Type:       "task",
		CreatedAt:  time.Now().UTC(),
		ParentHash: "sha256:deadbeef",
	}
	if err := store.WriteNode(n); err != nil {
		t.Fatalf("WriteNode: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("expected 1 hook event, got %d: %+v", len(got), got)
	}
	if got[0].NodeID != "node-abc" || got[0].Type != "task" || got[0].ParentHash != "sha256:deadbeef" {
		t.Errorf("unexpected event payload: %+v", got[0])
	}
}

func TestLedgerAppendHook_NilNoOp(t *testing.T) {
	// No hook set — WriteNode must not panic.
	SetLedgerAppendHook(nil)
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	n := Node{ID: "n1", Type: "task", CreatedAt: time.Now().UTC()}
	if err := store.WriteNode(n); err != nil {
		t.Fatalf("WriteNode: %v", err)
	}
}

func TestLedgerAppendHook_NotFiredOnDedup(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)

	calls := 0
	SetLedgerAppendHook(func(LedgerAppendEvent) { calls++ })
	t.Cleanup(func() { SetLedgerAppendHook(nil) })

	n := Node{ID: "dedup-n", Type: "task", CreatedAt: time.Now().UTC()}
	_ = store.WriteNode(n)
	_ = store.WriteNode(n) // second call should be a no-op (chain already exists)
	if calls != 1 {
		t.Errorf("hook fired %d times; want exactly 1 (dedup should skip)", calls)
	}
}
