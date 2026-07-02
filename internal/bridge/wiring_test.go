package bridge

// Tests for the A037 wiring surfaces: VerifyBridge.PublishStarted /
// PublishCompleted (externally executed pipelines announce on the
// governance bus) and NewWisdomBridgeWithStore (bridge wraps the run's
// existing store instead of forking learning state).

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/verify"
	"github.com/RelayOne/r1/internal/wisdom"
)

func waitFor(t *testing.T, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

func TestVerifyBridgePublishOutcomesExternally(t *testing.T) {
	b, l := setup(t)
	vb := NewVerifyBridge(b, l, "true", "", "")

	events, cancel := collectEvents(t, b, "verify.")
	defer cancel()

	outcomes := []verify.Outcome{{Name: "build", Success: true, Output: "ok"}}
	vb.PublishStarted("/repo", "task-1", "mission-1")
	vb.PublishCompleted(context.Background(), "task-1", "mission-1", outcomes, true)

	if !waitFor(t, func() bool { return len(events()) >= 2 }) {
		t.Fatalf("expected verify.started + verify.completed, saw %d events", len(events()))
	}
	var sawStarted, sawCompleted bool
	for _, e := range events() {
		switch e.Type {
		case EvtVerifyStarted:
			sawStarted = true
			if e.Scope.MissionID != "mission-1" {
				t.Errorf("started scope mission = %q, want mission-1", e.Scope.MissionID)
			}
		case EvtVerifyCompleted:
			sawCompleted = true
			var payload struct {
				Outcomes []verify.Outcome `json:"outcomes"`
				Success  bool             `json:"success"`
			}
			if err := json.Unmarshal(e.Payload, &payload); err != nil {
				t.Fatalf("unmarshal completed payload: %v", err)
			}
			if !payload.Success || len(payload.Outcomes) != 1 || payload.Outcomes[0].Name != "build" {
				t.Errorf("completed payload = %+v", payload)
			}
		}
	}
	if !sawStarted || !sawCompleted {
		t.Fatalf("sawStarted=%v sawCompleted=%v", sawStarted, sawCompleted)
	}

	// Ledger node written.
	nodes, err := l.Query(context.Background(), ledger.QueryFilter{Type: "verification", MissionID: "mission-1"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("verification ledger nodes = %d, want 1", len(nodes))
	}
}

func TestWisdomBridgeWithStoreSharesState(t *testing.T) {
	b, l := setup(t)
	store := wisdom.NewStore()
	wb := NewWisdomBridgeWithStore(b, l, store)

	events, cancel := collectEvents(t, b, "wisdom.")
	defer cancel()

	wb.Record("task-9", wisdom.Learning{Category: wisdom.Gotcha, Description: "shared state"})

	// The learning must land in the caller's store, not a forked one.
	if store.ForPrompt() == "" {
		t.Fatal("learning did not reach the wrapped store")
	}
	if !waitFor(t, func() bool { return len(events()) >= 1 }) {
		t.Fatal("wisdom.learning.recorded event never published")
	}
	if events()[0].Type != EvtLearningRecorded {
		t.Errorf("event type = %s, want %s", events()[0].Type, EvtLearningRecorded)
	}
	nodes, err := l.Query(context.Background(), ledger.QueryFilter{Type: "wisdom_learning"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("wisdom_learning ledger nodes = %d, want 1", len(nodes))
	}
}
