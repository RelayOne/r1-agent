package research

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1-agent/internal/bus"
	"github.com/RelayOne/r1-agent/internal/ledger"
)

func TestRequestDispatchesResearchers_Evaluate(t *testing.T) {
	ctx := context.Background()
	lDir := t.TempDir()
	l, err := ledger.New(lDir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	rule := NewRequestDispatchesResearchers()
	evt := bus.Event{
		ID:        "req-1",
		Type:      "worker.research.requested",
		Timestamp: time.Now(),
		Scope:     bus.Scope{MissionID: "m1"},
		Payload:   json.RawMessage(`{"question":"how does X work?"}`),
	}

	fire, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !fire {
		t.Fatal("expected rule to always fire on research requests")
	}
}

func TestRequestDispatchesResearchers_Action_LowStakes(t *testing.T) {
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

	rule := NewRequestDispatchesResearchers()
	payload, _ := json.Marshal(researchRequestPayload{
		WorkerID:    "worker-1",
		Question:    "what is the API rate limit?",
		StakesLevel: 0,
	})
	evt := bus.Event{
		ID:      "req-2",
		Type:    "worker.research.requested",
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

	// Expect: 1 pause + 1 spawn = 2 events
	if len(published) < 2 {
		t.Fatalf("expected at least 2 events, got %d", len(published))
	}
	if published[0].Type != bus.EvtWorkerPaused {
		t.Errorf("first event = %s, want %s", published[0].Type, bus.EvtWorkerPaused)
	}
	if published[1].Type != "supervisor.spawn.requested" {
		t.Errorf("second event = %s, want supervisor.spawn.requested", published[1].Type)
	}
}

func TestRequestDispatchesResearchers_Action_HighStakes(t *testing.T) {
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

	rule := NewRequestDispatchesResearchers()
	payload, _ := json.Marshal(researchRequestPayload{
		WorkerID:    "worker-1",
		Question:    "is this library licensed for commercial use?",
		StakesLevel: 3,
	})
	evt := bus.Event{
		ID:      "req-3",
		Type:    "worker.research.requested",
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
		if n >= 4 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()

	// Expect: 1 pause + 3 spawns = 4 events
	if len(published) < 4 {
		t.Fatalf("expected at least 4 events, got %d", len(published))
	}
	if published[0].Type != bus.EvtWorkerPaused {
		t.Errorf("first event = %s, want %s", published[0].Type, bus.EvtWorkerPaused)
	}
	for i := 1; i <= 3; i++ {
		if published[i].Type != "supervisor.spawn.requested" {
			t.Errorf("event[%d] = %s, want supervisor.spawn.requested", i, published[i].Type)
		}
	}
}
