package plan

import (
	"sync"
	"testing"
)

// TestStatusChangeHook_FiresOnSetState asserts that a SetState call
// that passes the transition table triggers the registered hook with
// the correct payload (node_id, new status, title).
func TestStatusChangeHook_FiresOnSetState(t *testing.T) {
	var mu sync.Mutex
	var got []StatusChangeEvent
	SetStatusChangeHook(func(ev StatusChangeEvent) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, ev)
	})
	t.Cleanup(func() { SetStatusChangeHook(nil) })

	n := &Node{
		ID:     "node-xyz",
		Title:  "Draft the SOW",
		Status: StateDraft,
	}
	if err := n.SetState(StateReady); err != nil {
		t.Fatalf("SetState: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("expected 1 hook event, got %d: %+v", len(got), got)
	}
	if got[0].NodeID != "node-xyz" {
		t.Errorf("NodeID=%q want %q", got[0].NodeID, "node-xyz")
	}
	if got[0].Status != string(StateReady) {
		t.Errorf("Status=%q want %q", got[0].Status, StateReady)
	}
	if got[0].Title != "Draft the SOW" {
		t.Errorf("Title=%q want %q", got[0].Title, "Draft the SOW")
	}
}

// TestStatusChangeHook_NilNoOp asserts a nil hook does not panic on
// SetState. Covers the standalone-run / test-default path.
func TestStatusChangeHook_NilNoOp(t *testing.T) {
	SetStatusChangeHook(nil)
	n := &Node{ID: "n1", Title: "t", Status: StateDraft}
	if err := n.SetState(StateReady); err != nil {
		t.Fatalf("SetState: %v", err)
	}
}

// TestStatusChangeHook_NotFiredOnInvalidTransition asserts a rejected
// transition (one not in the table) does NOT fire the hook — we must
// not emit phantom node_updated events for transitions that never
// happened.
func TestStatusChangeHook_NotFiredOnInvalidTransition(t *testing.T) {
	calls := 0
	SetStatusChangeHook(func(StatusChangeEvent) { calls++ })
	t.Cleanup(func() { SetStatusChangeHook(nil) })

	n := &Node{ID: "n-bad", Title: "t", Status: StateVerified}
	// VERIFIED is terminal — no out-edges. SetState must reject.
	if err := n.SetState(StateReady); err == nil {
		t.Fatalf("expected ErrInvalidTransition, got nil")
	}
	if calls != 0 {
		t.Errorf("hook fired %d times on invalid transition; want 0", calls)
	}
}

// TestStatusChangeHook_FiresPerTransition asserts multiple valid
// transitions each produce one event, in order.
func TestStatusChangeHook_FiresPerTransition(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	SetStatusChangeHook(func(ev StatusChangeEvent) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, ev.Status)
	})
	t.Cleanup(func() { SetStatusChangeHook(nil) })

	n := &Node{ID: "n-seq", Title: "t", Status: StateDraft}
	for _, next := range []State{StateReady, StateActive, StateCompleted, StateVerified} {
		if err := n.SetState(next); err != nil {
			t.Fatalf("SetState(%s): %v", next, err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{"READY", "ACTIVE", "COMPLETED", "VERIFIED"}
	if len(seen) != len(want) {
		t.Fatalf("saw %d events, want %d: %v", len(seen), len(want), seen)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Errorf("event[%d]=%q want %q", i, seen[i], want[i])
		}
	}
}
