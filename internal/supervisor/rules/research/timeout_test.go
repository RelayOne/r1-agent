package research

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/ledger"
)

func TestTimeout_Evaluate_NoReport(t *testing.T) {
	ctx := context.Background()
	lDir := t.TempDir()
	l, err := ledger.New(lDir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	rule := NewTimeout()
	payload, _ := json.Marshal(researchTimeoutPayload{
		ResearcherID: "researcher-1",
		RequesterID:  "worker-1",
		Question:     "what is the API limit?",
	})
	evt := bus.Event{
		ID:        "timeout-1",
		Type:      "research.timeout",
		Timestamp: time.Now(),
		Scope:     bus.Scope{MissionID: "m1"},
		Payload:   payload,
	}

	fire, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !fire {
		t.Fatal("expected rule to fire when no report exists")
	}
}

func TestTimeout_Evaluate_HasReport(t *testing.T) {
	ctx := context.Background()
	lDir := t.TempDir()
	l, err := ledger.New(lDir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	// Add a report node from the researcher.
	reportContent, _ := json.Marshal(map[string]string{"answer": "100 req/s"})
	_, err = l.AddNode(ctx, ledger.Node{
		Type:          "research.report",
		SchemaVersion: 1,
		CreatedBy:     "researcher-1",
		MissionID:     "m1",
		Content:       reportContent,
	})
	if err != nil {
		t.Fatal(err)
	}

	rule := NewTimeout()
	payload, _ := json.Marshal(researchTimeoutPayload{
		ResearcherID: "researcher-1",
		RequesterID:  "worker-1",
	})
	evt := bus.Event{
		ID:      "timeout-2",
		Type:    "research.timeout",
		Scope:   bus.Scope{MissionID: "m1"},
		Payload: payload,
	}

	fire, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if fire {
		t.Fatal("expected rule NOT to fire when report already exists")
	}
}

func TestTimeout_Action_WithQuestion(t *testing.T) {
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

	rule := NewTimeout()
	payload, _ := json.Marshal(researchTimeoutPayload{
		ResearcherID: "researcher-1",
		RequesterID:  "worker-1",
		Question:     "how does X work?",
	})
	evt := bus.Event{
		ID:      "timeout-3",
		Type:    "research.timeout",
		Scope:   bus.Scope{MissionID: "m1"},
		Payload: payload,
	}

	if err := rule.Action(context.Background(), evt, b); err != nil {
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

	// Expect termination + spawn replacement = 2 events
	if len(published) < 2 {
		t.Fatalf("expected at least 2 events, got %d", len(published))
	}
	if published[0].Type != bus.EvtWorkerTerminated {
		t.Errorf("first event = %s, want %s", published[0].Type, bus.EvtWorkerTerminated)
	}
	if published[1].Type != "supervisor.spawn.requested" {
		t.Errorf("second event = %s, want supervisor.spawn.requested", published[1].Type)
	}
}
