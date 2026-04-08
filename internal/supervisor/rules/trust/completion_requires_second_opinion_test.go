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

func TestCompletionRequiresSecondOpinion_Evaluate_NoReviewer(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	l, err := ledger.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	rule := NewCompletionRequiresSecondOpinion()

	payload, _ := json.Marshal(declarationPayload{
		WorkerID:   "worker-1",
		TaskID:     "task-42",
		ArtifactID: "artifact-1",
	})

	evt := bus.Event{
		ID:        "evt-1",
		Type:      bus.EvtWorkerDeclarationDone,
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

func TestCompletionRequiresSecondOpinion_Evaluate_HasReviewer(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	l, err := ledger.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	// Add an agree node from a different worker.
	agreeJSON, _ := json.Marshal(agreeContent{TaskID: "task-42"})
	_, err = l.AddNode(ctx, ledger.Node{
		Type:          "review.agree",
		SchemaVersion: 1,
		CreatedBy:     "reviewer-1",
		Content:       agreeJSON,
	})
	if err != nil {
		t.Fatal(err)
	}

	rule := NewCompletionRequiresSecondOpinion()

	payload, _ := json.Marshal(declarationPayload{
		WorkerID:   "worker-1",
		TaskID:     "task-42",
		ArtifactID: "artifact-1",
	})

	evt := bus.Event{
		ID:        "evt-2",
		Type:      bus.EvtWorkerDeclarationDone,
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
		t.Fatal("expected rule NOT to fire when reviewer agree exists")
	}
}

func TestCompletionRequiresSecondOpinion_Action(t *testing.T) {
	dir := t.TempDir()
	b, err := bus.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	rule := NewCompletionRequiresSecondOpinion()

	payload, _ := json.Marshal(declarationPayload{
		WorkerID:   "worker-1",
		TaskID:     "task-42",
		ArtifactID: "artifact-1",
	})

	evt := bus.Event{
		ID:        "evt-3",
		Type:      bus.EvtWorkerDeclarationDone,
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

	// First should be pause.
	if published[0].Type != bus.EvtWorkerPaused {
		t.Errorf("first event type = %s, want %s", published[0].Type, bus.EvtWorkerPaused)
	}
	// Second should be spawn.
	if published[1].Type != "supervisor.spawn.requested" {
		t.Errorf("second event type = %s, want supervisor.spawn.requested", published[1].Type)
	}
}
