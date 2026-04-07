package trust

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ericmacdougall/stoke/internal/bus"
	"github.com/ericmacdougall/stoke/internal/ledger"
)

func TestFixRequiresSecondOpinion_Evaluate_NoReviewer(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	l, err := ledger.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	rule := NewFixRequiresSecondOpinion()

	payload, _ := json.Marshal(fixPayload{
		WorkerID:   "worker-1",
		TaskID:     "task-42",
		ArtifactID: "artifact-1",
		DissentID:  "dissent-1",
	})

	evt := bus.Event{
		ID:        "evt-1",
		Type:      "worker.fix.completed",
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
		t.Fatal("expected rule to fire when no reviewer agree exists")
	}
}

func TestFixRequiresSecondOpinion_Action(t *testing.T) {
	dir := t.TempDir()
	b, err := bus.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	rule := NewFixRequiresSecondOpinion()

	payload, _ := json.Marshal(fixPayload{
		WorkerID:   "worker-1",
		TaskID:     "task-42",
		ArtifactID: "artifact-1",
		DissentID:  "dissent-1",
	})

	evt := bus.Event{
		ID:        "evt-1",
		Type:      "worker.fix.completed",
		Timestamp: time.Now(),
		EmitterID: "worker-1",
		Scope:     bus.Scope{MissionID: "m1", TaskID: "task-42"},
		Payload:   payload,
	}

	var published []bus.Event
	b.Subscribe(bus.Pattern{}, func(e bus.Event) {
		published = append(published, e)
	})

	err = rule.Action(context.Background(), evt, b)
	if err != nil {
		t.Fatalf("Action: %v", err)
	}

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
