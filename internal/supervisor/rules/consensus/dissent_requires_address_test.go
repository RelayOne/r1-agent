package consensus

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/ledger"
)

func TestDissentRequiresAddress_Evaluate_DissentNode(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	l, err := ledger.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	rule := NewDissentRequiresAddress()

	payload, _ := json.Marshal(nodeAddedPayload{
		NodeID:   "node-1",
		NodeType: "review.dissent",
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
		t.Fatal("expected rule to fire for dissent node")
	}
}

func TestDissentRequiresAddress_Evaluate_NonDissent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	l, err := ledger.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	rule := NewDissentRequiresAddress()

	payload, _ := json.Marshal(nodeAddedPayload{
		NodeID:   "node-1",
		NodeType: "review.agree",
	})

	evt := bus.Event{
		ID:        "evt-1",
		Type:      bus.EvtLedgerNodeAdded,
		Timestamp: time.Now(),
		EmitterID: "reviewer-1",
		Scope:     bus.Scope{MissionID: "m1"},
		Payload:   payload,
	}

	fired, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if fired {
		t.Fatal("expected rule NOT to fire for agree node")
	}
}

func TestDissentRequiresAddress_Action(t *testing.T) {
	dir := t.TempDir()
	b, err := bus.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	rule := NewDissentRequiresAddress()

	payload, _ := json.Marshal(nodeAddedPayload{
		NodeID:   "node-1",
		NodeType: "review.dissent",
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
		if n >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()

	if len(published) < 2 {
		t.Fatalf("expected at least 2 published events, got %d", len(published))
	}

	if published[0].Type != "consensus.loop.state.changed" {
		t.Errorf("first event type = %s, want consensus.loop.state.changed", published[0].Type)
	}
	if published[1].Type != "consensus.dissent.notification" {
		t.Errorf("second event type = %s, want consensus.dissent.notification", published[1].Type)
	}
}
