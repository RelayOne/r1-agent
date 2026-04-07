package consensus

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/ericmacdougall/stoke/internal/bus"
	"github.com/ericmacdougall/stoke/internal/ledger"
)

func TestDraftRequiresReview_Evaluate_DraftNode(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	l, err := ledger.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	rule := NewDraftRequiresReview()

	payload, _ := json.Marshal(nodeAddedPayload{
		NodeID:   "node-1",
		NodeType: "prd.draft",
		LoopID:   "loop-1",
		Concern:  "performance",
	})

	evt := bus.Event{
		ID:        "evt-1",
		Type:      bus.EvtLedgerNodeAdded,
		Timestamp: time.Now(),
		EmitterID: "worker-1",
		Scope:     bus.Scope{MissionID: "m1", LoopID: "loop-1"},
		Payload:   payload,
	}

	fired, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !fired {
		t.Fatal("expected rule to fire for draft node")
	}
}

func TestDraftRequiresReview_Evaluate_NonDraft(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	l, err := ledger.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	rule := NewDraftRequiresReview()

	payload, _ := json.Marshal(nodeAddedPayload{
		NodeID:   "node-1",
		NodeType: "review.agree",
	})

	evt := bus.Event{
		ID:        "evt-1",
		Type:      bus.EvtLedgerNodeAdded,
		Timestamp: time.Now(),
		EmitterID: "worker-1",
		Scope:     bus.Scope{MissionID: "m1"},
		Payload:   payload,
	}

	fired, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if fired {
		t.Fatal("expected rule NOT to fire for non-draft node")
	}
}

func TestDraftRequiresReview_Action(t *testing.T) {
	dir := t.TempDir()
	b, err := bus.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	rule := NewDraftRequiresReview()

	payload, _ := json.Marshal(nodeAddedPayload{
		NodeID:   "node-1",
		NodeType: "prd.draft",
		LoopID:   "loop-1",
		Concern:  "performance",
	})

	evt := bus.Event{
		ID:        "evt-1",
		Type:      bus.EvtLedgerNodeAdded,
		Timestamp: time.Now(),
		EmitterID: "worker-1",
		Scope:     bus.Scope{MissionID: "m1", LoopID: "loop-1"},
		Payload:   payload,
	}

	var published []bus.Event
	var mu sync.Mutex
	b.Subscribe(bus.Pattern{}, func(e bus.Event) {
		mu.Lock()
		published = append(published, e)
		mu.Unlock()
	})

	err = rule.Action(context.Background(), evt, b)
	if err != nil {
		t.Fatalf("Action: %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(published)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()

	// Expect spawns for Reviewer and LeadEngineer.
	if len(published) < 2 {
		t.Fatalf("expected at least 2 published events, got %d", len(published))
	}

	for _, e := range published {
		if e.Type != "supervisor.spawn.requested" {
			t.Errorf("event type = %s, want supervisor.spawn.requested", e.Type)
		}
	}
}
