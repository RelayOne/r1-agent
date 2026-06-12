package workflow

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/hub"
)

// recordReviewEvents registers an observe-mode subscriber on the given bus that
// records every EventVerifyCrossModelReview it sees. It returns a snapshot
// accessor (safe to call after a short poll) for the recorded events.
func recordReviewEvents(t *testing.T, bus *hub.Bus) func() []*hub.Event {
	t.Helper()
	var mu sync.Mutex
	var got []*hub.Event
	bus.Register(hub.Subscriber{
		ID:     "test-review-recorder",
		Events: []hub.EventType{hub.EventVerifyCrossModelReview},
		Mode:   hub.ModeObserve,
		Handler: func(_ context.Context, ev *hub.Event) *hub.HookResponse {
			mu.Lock()
			got = append(got, ev)
			mu.Unlock()
			return nil
		},
	})
	return func() []*hub.Event {
		mu.Lock()
		defer mu.Unlock()
		out := make([]*hub.Event, len(got))
		copy(out, got)
		return out
	}
}

// waitForReviewEvents polls until at least want events are recorded or the
// deadline elapses, then returns whatever was recorded.
func waitForReviewEvents(snapshot func() []*hub.Event, want int) []*hub.Event {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if evs := snapshot(); len(evs) >= want {
			return evs
		}
		time.Sleep(5 * time.Millisecond)
	}
	return snapshot()
}

func TestReviewEmit_Pass(t *testing.T) {
	bus := hub.New()
	snapshot := recordReviewEvents(t, bus)
	e := Engine{EventBus: bus}

	e.emitReviewEvent("task-pass", "codex", true)

	evs := waitForReviewEvents(snapshot, 1)
	if len(evs) != 1 {
		t.Fatalf("expected exactly 1 EventVerifyCrossModelReview, got %d", len(evs))
	}
	ev := evs[0]
	if ev.Type != hub.EventVerifyCrossModelReview {
		t.Errorf("Type = %q, want %q", ev.Type, hub.EventVerifyCrossModelReview)
	}
	if ev.TaskID != "task-pass" {
		t.Errorf("TaskID = %q, want %q", ev.TaskID, "task-pass")
	}
	if ev.Lifecycle == nil {
		t.Fatal("Lifecycle is nil")
	}
	if ev.Lifecycle.State != "agree" {
		t.Errorf("Lifecycle.State = %q, want %q", ev.Lifecycle.State, "agree")
	}
	if ev.Lifecycle.Entity != "review" {
		t.Errorf("Lifecycle.Entity = %q, want %q", ev.Lifecycle.Entity, "review")
	}
	if ev.Phase != "review" {
		t.Errorf("Phase = %q, want %q", ev.Phase, "review")
	}
	if ev.Model == nil {
		t.Fatal("Model is nil")
	}
	if ev.Model.Provider != "codex" {
		t.Errorf("Model.Provider = %q, want %q", ev.Model.Provider, "codex")
	}
}

func TestReviewEmit_Dissent(t *testing.T) {
	bus := hub.New()
	snapshot := recordReviewEvents(t, bus)
	e := Engine{EventBus: bus}

	e.emitReviewEvent("task-dissent", "claude", false)

	evs := waitForReviewEvents(snapshot, 1)
	if len(evs) != 1 {
		t.Fatalf("expected exactly 1 EventVerifyCrossModelReview, got %d", len(evs))
	}
	ev := evs[0]
	if ev.Lifecycle == nil {
		t.Fatal("Lifecycle is nil")
	}
	if ev.Lifecycle.State != "dissent" {
		t.Errorf("Lifecycle.State = %q, want %q", ev.Lifecycle.State, "dissent")
	}
	if ev.TaskID != "task-dissent" {
		t.Errorf("TaskID = %q, want %q", ev.TaskID, "task-dissent")
	}
	if ev.Model == nil || ev.Model.Provider != "claude" {
		t.Errorf("Model.Provider mismatch, got %+v", ev.Model)
	}
}

func TestReviewEmit_NilBus(t *testing.T) {
	e := Engine{EventBus: nil}
	// Must not panic and must not emit (nothing to record; assert no panic).
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("emitReviewEvent panicked with nil bus: %v", r)
		}
	}()
	e.emitReviewEvent("task-nil", "codex", true)
	// Give any (incorrectly spawned) async work a moment; there should be none.
	time.Sleep(20 * time.Millisecond)
}
