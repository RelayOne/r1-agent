package skill

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/ledger"
)

func TestExtractionTrigger_Evaluate_Converged(t *testing.T) {
	ctx := context.Background()
	lDir := t.TempDir()
	l, err := ledger.New(lDir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	rule := NewExtractionTrigger()
	evt := bus.Event{
		ID:        "conv-1",
		Type:      "loop.converged",
		Timestamp: time.Now(),
		Scope:     bus.Scope{MissionID: "m1", LoopID: "loop-1"},
		Payload:   json.RawMessage(`{}`),
	}

	fire, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !fire {
		t.Fatal("expected rule to fire on loop.converged")
	}
}

func TestExtractionTrigger_Evaluate_Escalated_DifferentApproach(t *testing.T) {
	ctx := context.Background()
	lDir := t.TempDir()
	l, err := ledger.New(lDir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	rule := NewExtractionTrigger()
	payload, _ := json.Marshal(escalationPayload{Outcome: "try_different_approach"})
	evt := bus.Event{
		ID:      "esc-1",
		Type:    "loop.escalated",
		Scope:   bus.Scope{MissionID: "m1"},
		Payload: payload,
	}

	fire, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !fire {
		t.Fatal("expected rule to fire on escalation with try_different_approach")
	}
}

func TestExtractionTrigger_Evaluate_Escalated_Other(t *testing.T) {
	ctx := context.Background()
	lDir := t.TempDir()
	l, err := ledger.New(lDir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	rule := NewExtractionTrigger()
	payload, _ := json.Marshal(escalationPayload{Outcome: "need_more_info"})
	evt := bus.Event{
		ID:      "esc-2",
		Type:    "loop.escalated",
		Scope:   bus.Scope{MissionID: "m1"},
		Payload: payload,
	}

	fire, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if fire {
		t.Fatal("expected rule NOT to fire on escalation with need_more_info")
	}
}

func TestExtractionTrigger_Action(t *testing.T) {
	bDir := t.TempDir()
	b, err := bus.New(bDir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	var published []bus.Event
	var mu sync.Mutex
	b.Subscribe(bus.Pattern{}, func(e bus.Event) {
		mu.Lock()
		published = append(published, e)
		mu.Unlock()
	})

	rule := NewExtractionTrigger()
	evt := bus.Event{
		ID:    "conv-2",
		Type:  "loop.converged",
		Scope: bus.Scope{MissionID: "m1"},
	}

	if err := rule.Action(context.Background(), evt, b); err != nil {
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

	if len(published) < 1 {
		t.Fatal("expected extraction event")
	}
	if published[0].Type != bus.EvtSkillExtraction {
		t.Errorf("type = %s, want %s", published[0].Type, bus.EvtSkillExtraction)
	}
}
