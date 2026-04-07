package trust

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/ericmacdougall/stoke/internal/bus"
	"github.com/ericmacdougall/stoke/internal/ledger"
)

func TestProblemRequiresSecondOpinion_Evaluate_Infeasible(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	l, err := ledger.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	rule := NewProblemRequiresSecondOpinion()

	payload, _ := json.Marshal(escalationPayload{
		WorkerID:       "worker-1",
		TaskID:         "task-42",
		EscalationType: "task.infeasible",
		Reason:         "cannot implement without API access",
	})

	evt := bus.Event{
		ID:        "evt-1",
		Type:      "worker.escalation.requested",
		Timestamp: time.Now(),
		EmitterID: "worker-1",
		Scope:     bus.Scope{MissionID: "m1", TaskID: "task-42"},
		Payload:   payload,
	}

	fired, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !fired {
		t.Fatal("expected rule to fire for task.infeasible escalation")
	}
}

func TestProblemRequiresSecondOpinion_Evaluate_IrrelevantType(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	l, err := ledger.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	rule := NewProblemRequiresSecondOpinion()

	payload, _ := json.Marshal(escalationPayload{
		WorkerID:       "worker-1",
		TaskID:         "task-42",
		EscalationType: "other.type",
	})

	evt := bus.Event{
		ID:        "evt-1",
		Type:      "worker.escalation.requested",
		Timestamp: time.Now(),
		EmitterID: "worker-1",
		Scope:     bus.Scope{MissionID: "m1", TaskID: "task-42"},
		Payload:   payload,
	}

	fired, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if fired {
		t.Fatal("expected rule NOT to fire for non-infeasible/blocked escalation")
	}
}

func TestProblemRequiresSecondOpinion_Action(t *testing.T) {
	dir := t.TempDir()
	b, err := bus.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	rule := NewProblemRequiresSecondOpinion()

	payload, _ := json.Marshal(escalationPayload{
		WorkerID:       "worker-1",
		TaskID:         "task-42",
		EscalationType: "task.blocked",
		Reason:         "upstream dependency missing",
	})

	evt := bus.Event{
		ID:        "evt-1",
		Type:      "worker.escalation.requested",
		Timestamp: time.Now(),
		EmitterID: "worker-1",
		Scope:     bus.Scope{MissionID: "m1", TaskID: "task-42"},
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

	if published[0].Type != bus.EvtWorkerPaused {
		t.Errorf("first event type = %s, want %s", published[0].Type, bus.EvtWorkerPaused)
	}
	if published[1].Type != "supervisor.spawn.requested" {
		t.Errorf("second event type = %s, want supervisor.spawn.requested", published[1].Type)
	}
}
