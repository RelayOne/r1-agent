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

func TestPartnerTimeout_Evaluate_NoResponse(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	l, err := ledger.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	rule := NewPartnerTimeout()

	payload, _ := json.Marshal(timeoutPayload{
		PartnerID: "reviewer-1",
		LoopID:    "loop-1",
		Role:      "Reviewer",
	})

	evt := bus.Event{
		ID:        "evt-1",
		Type:      "consensus.partner.timeout",
		Timestamp: time.Now(),
		EmitterID: "supervisor",
		Scope:     bus.Scope{MissionID: "m1", LoopID: "loop-1"},
		Payload:   payload,
	}

	fired, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !fired {
		t.Fatal("expected rule to fire when partner has not responded")
	}
}

func TestPartnerTimeout_Evaluate_HasResponse(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	l, err := ledger.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	// Partner has responded with an agree node.
	agreeJSON, _ := json.Marshal(map[string]string{"loop_id": "loop-1"})
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

	rule := NewPartnerTimeout()

	payload, _ := json.Marshal(timeoutPayload{
		PartnerID: "reviewer-1",
		LoopID:    "loop-1",
		Role:      "Reviewer",
	})

	evt := bus.Event{
		ID:        "evt-1",
		Type:      "consensus.partner.timeout",
		Timestamp: time.Now(),
		EmitterID: "supervisor",
		Scope:     bus.Scope{MissionID: "m1", LoopID: "loop-1"},
		Payload:   payload,
	}

	fired, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if fired {
		t.Fatal("expected rule NOT to fire when partner has responded")
	}
}

func TestPartnerTimeout_Action_SpawnReplacement(t *testing.T) {
	dir := t.TempDir()
	b, err := bus.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	rule := NewPartnerTimeout()

	payload, _ := json.Marshal(timeoutPayload{
		PartnerID:     "reviewer-1",
		LoopID:        "loop-1",
		Role:          "Reviewer",
		IsReplacement: false,
	})

	evt := bus.Event{
		ID:        "evt-1",
		Type:      "consensus.partner.timeout",
		Timestamp: time.Now(),
		EmitterID: "supervisor",
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

	if published[0].Type != "consensus.partner.timed_out" {
		t.Errorf("first event type = %s, want consensus.partner.timed_out", published[0].Type)
	}
	if published[1].Type != "supervisor.spawn.requested" {
		t.Errorf("second event type = %s, want supervisor.spawn.requested", published[1].Type)
	}
}

func TestPartnerTimeout_Action_Escalate(t *testing.T) {
	dir := t.TempDir()
	b, err := bus.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	rule := NewPartnerTimeout()

	payload, _ := json.Marshal(timeoutPayload{
		PartnerID:     "reviewer-2",
		LoopID:        "loop-1",
		Role:          "Reviewer",
		IsReplacement: true,
	})

	evt := bus.Event{
		ID:        "evt-1",
		Type:      "consensus.partner.timeout",
		Timestamp: time.Now(),
		EmitterID: "supervisor",
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

	if published[1].Type != "supervisor.escalation.forwarded" {
		t.Errorf("second event type = %s, want supervisor.escalation.forwarded", published[1].Type)
	}
}
