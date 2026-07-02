package loops

// Tests for SubscribeStateChanges (audit A066): consensus.loop.state.changed
// bus events must persist to the ledger via TransitionState — previously
// nothing consumed those events and TransitionState had no callers, so
// loop nodes could never change state.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/bus"
)

func newTestBus(t *testing.T) *bus.Bus {
	t.Helper()
	b, err := bus.New(t.TempDir())
	if err != nil {
		t.Fatalf("bus.New: %v", err)
	}
	t.Cleanup(func() { b.Close() })
	return b
}

func publishStateChanged(t *testing.T, b *bus.Bus, loopID, state, reason string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]string{
		"loop_id": loopID,
		"state":   state,
		"reason":  reason,
	})
	if err := b.Publish(bus.Event{
		Type:    StateChangedEventType,
		Scope:   bus.Scope{MissionID: "mission-1", LoopID: loopID},
		Payload: payload,
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
}

func waitForState(t *testing.T, tr *Tracker, loopID string, want LoopState) bool {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		info, err := tr.Get(context.Background(), loopID)
		if err == nil && info.State == want {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func TestSubscribeStateChangesPersistsTransition(t *testing.T) {
	l := newTestLedger(t)
	b := newTestBus(t)
	tr := NewTracker(l)
	tr.SubscribeStateChanges(b)

	draftID := createDraft(t, l, "")
	loopID := createLoop(t, l, "mission-1", LoopTypePRD, StateReviewing, draftID, []string{"r1"}, "")

	publishStateChanged(t, b, loopID, string(StateConverged), "all partners agreed")

	if !waitForState(t, tr, loopID, StateConverged) {
		info, err := tr.Get(context.Background(), loopID)
		t.Fatalf("loop never transitioned to converged; info=%+v err=%v", info, err)
	}

	// The transition preserves reason as terminal_reason.
	resolved, err := l.Resolve(context.Background(), loopID)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	var m struct {
		TerminalReason string `json:"terminal_reason"`
	}
	if err := json.Unmarshal(resolved.Content, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m.TerminalReason != "all partners agreed" {
		t.Errorf("terminal_reason = %q, want 'all partners agreed'", m.TerminalReason)
	}
}

func TestSubscribeStateChangesIgnoresUnknownState(t *testing.T) {
	l := newTestLedger(t)
	b := newTestBus(t)
	tr := NewTracker(l)
	tr.SubscribeStateChanges(b)

	draftID := createDraft(t, l, "")
	loopID := createLoop(t, l, "mission-1", LoopTypePRD, StateReviewing, draftID, []string{"r1"}, "")

	publishStateChanged(t, b, loopID, "bogus-state", "nope")
	// Also a valid one afterwards so we have a deterministic wait point.
	publishStateChanged(t, b, loopID, string(StateEscalated), "budget")

	if !waitForState(t, tr, loopID, StateEscalated) {
		t.Fatal("valid transition after bogus one never applied")
	}
	// The bogus state must never have been written: iteration count is
	// exactly 2 (original + one transition).
	count, err := tr.IterationCount(context.Background(), loopID)
	if err != nil {
		t.Fatalf("IterationCount: %v", err)
	}
	if count != 2 {
		t.Errorf("iteration count = %d, want 2 (bogus state must be dropped)", count)
	}
}
