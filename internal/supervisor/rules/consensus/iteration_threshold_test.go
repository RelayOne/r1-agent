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

func TestIterationThreshold_Evaluate_BelowThreshold(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	l, err := ledger.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	// Create a draft node with no supersedes predecessors.
	draftJSON, _ := json.Marshal(map[string]string{"title": "draft v1"})
	nodeID, err := l.AddNode(ctx, ledger.Node{
		Type:          "pr.draft",
		SchemaVersion: 1,
		CreatedBy:     "worker-1",
		Content:       draftJSON,
	})
	if err != nil {
		t.Fatal(err)
	}

	rule := NewIterationThreshold()

	payload, _ := json.Marshal(nodeAddedPayload{
		NodeID:   nodeID,
		NodeType: "pr.draft",
		LoopID:   "loop-1",
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
	if fired {
		t.Fatal("expected rule NOT to fire below threshold")
	}
}

func TestIterationThreshold_Evaluate_NonDraft(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	l, err := ledger.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	rule := NewIterationThreshold()

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

func TestIterationThreshold_Action(t *testing.T) {
	dir := t.TempDir()
	b, err := bus.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	rule := NewIterationThreshold()

	payload, _ := json.Marshal(nodeAddedPayload{
		NodeID:   "node-1",
		NodeType: "pr.draft",
		LoopID:   "loop-1",
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
		if n >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()

	if len(published) != 1 {
		t.Fatalf("expected 1 published event, got %d", len(published))
	}
	if published[0].Type != "supervisor.spawn.requested" {
		t.Errorf("event type = %s, want supervisor.spawn.requested", published[0].Type)
	}
}
