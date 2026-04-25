package consensus

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1-agent/internal/bus"
	"github.com/RelayOne/r1-agent/internal/ledger"
)

func TestConvergenceDetected_Evaluate_AllAgreed(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	l, err := ledger.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	// Add an agree node for the loop.
	agreeJSON, _ := json.Marshal(convergenceContent{LoopID: "loop-1"})
	_, err = l.AddNode(ctx, ledger.Node{
		Type:          "review.agree",
		SchemaVersion: 1,
		CreatedBy:     "reviewer-1",
		MissionID:     "m1",
		Content:       agreeJSON,
	})
	if err != nil {
		t.Fatal(err)
	}

	rule := NewConvergenceDetected()

	payload, _ := json.Marshal(nodeAddedPayload{
		NodeID:   "node-1",
		NodeType: "review.agree",
		LoopID:   "loop-1",
	})

	evt := bus.Event{
		ID:        "evt-1",
		Type:      bus.EvtLedgerNodeAdded,
		Timestamp: time.Now(),
		EmitterID: "reviewer-1",
		Scope:     bus.Scope{MissionID: "m1", LoopID: "loop-1"},
		Payload:   payload,
	}

	fired, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !fired {
		t.Fatal("expected rule to fire when all partners agreed")
	}
}

func TestConvergenceDetected_Evaluate_OutstandingDissent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	l, err := ledger.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	// Add an unresolved dissent node.
	dissentJSON, _ := json.Marshal(convergenceContent{LoopID: "loop-1", Resolved: false})
	_, err = l.AddNode(ctx, ledger.Node{
		Type:          "review.dissent",
		SchemaVersion: 1,
		CreatedBy:     "reviewer-1",
		MissionID:     "m1",
		Content:       dissentJSON,
	})
	if err != nil {
		t.Fatal(err)
	}

	rule := NewConvergenceDetected()

	payload, _ := json.Marshal(nodeAddedPayload{
		NodeID:   "node-1",
		NodeType: "review.agree",
		LoopID:   "loop-1",
	})

	evt := bus.Event{
		ID:        "evt-1",
		Type:      bus.EvtLedgerNodeAdded,
		Timestamp: time.Now(),
		EmitterID: "reviewer-2",
		Scope:     bus.Scope{MissionID: "m1", LoopID: "loop-1"},
		Payload:   payload,
	}

	fired, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if fired {
		t.Fatal("expected rule NOT to fire with outstanding dissent")
	}
}

func TestConvergenceDetected_Action(t *testing.T) {
	dir := t.TempDir()
	b, err := bus.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	rule := NewConvergenceDetected()

	payload, _ := json.Marshal(nodeAddedPayload{
		NodeID:   "node-1",
		NodeType: "review.agree",
		LoopID:   "loop-1",
	})

	evt := bus.Event{
		ID:        "evt-1",
		Type:      bus.EvtLedgerNodeAdded,
		Timestamp: time.Now(),
		EmitterID: "reviewer-1",
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
	if published[0].Type != "consensus.loop.state.changed" {
		t.Errorf("event type = %s, want consensus.loop.state.changed", published[0].Type)
	}
}
