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

func TestReportUnblocksRequester_Evaluate_SingleResearcher(t *testing.T) {
	ctx := context.Background()
	lDir := t.TempDir()
	l, err := ledger.New(lDir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	rule := NewReportUnblocksRequester()
	payload, _ := json.Marshal(researchCompletedPayload{
		RequesterID:      "worker-1",
		ResearcherIndex:  0,
		TotalResearchers: 1,
		Report:           "The API rate limit is 100 req/s.",
	})
	evt := bus.Event{
		ID:        "done-1",
		Type:      "worker.research.completed",
		Timestamp: time.Now(),
		Scope:     bus.Scope{MissionID: "m1", TaskID: "task-1"},
		Payload:   payload,
	}

	fire, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !fire {
		t.Fatal("expected rule to fire for single researcher completion")
	}
}

func TestReportUnblocksRequester_Evaluate_EmptyReport(t *testing.T) {
	ctx := context.Background()
	lDir := t.TempDir()
	l, err := ledger.New(lDir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	rule := NewReportUnblocksRequester()
	payload, _ := json.Marshal(researchCompletedPayload{
		RequesterID:      "worker-1",
		TotalResearchers: 1,
		Report:           "",
	})
	evt := bus.Event{
		ID:      "done-2",
		Type:    "worker.research.completed",
		Scope:   bus.Scope{MissionID: "m1"},
		Payload: payload,
	}

	fire, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if fire {
		t.Fatal("expected rule NOT to fire with empty report")
	}
}

func TestReportUnblocksRequester_Action(t *testing.T) {
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

	rule := NewReportUnblocksRequester()
	payload, _ := json.Marshal(researchCompletedPayload{
		RequesterID: "worker-1",
		Report:      "Found the answer.",
	})
	evt := bus.Event{
		ID:      "done-3",
		Type:    "worker.research.completed",
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
		if n >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()

	if len(published) < 1 {
		t.Fatal("expected resume event")
	}
	if published[0].Type != bus.EvtWorkerResumed {
		t.Errorf("type = %s, want %s", published[0].Type, bus.EvtWorkerResumed)
	}
}
