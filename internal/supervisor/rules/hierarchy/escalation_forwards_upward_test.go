package hierarchy

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/ledger"
)

func TestEscalationForwardsUpward_Evaluate_FailedResolution(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	l, err := ledger.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	// Add a failed resolution attempt.
	resJSON, _ := json.Marshal(map[string]any{
		"task_id": "task-42",
		"success": false,
	})
	_, err = l.AddNode(ctx, ledger.Node{
		Type:          "escalation.resolution_attempt",
		SchemaVersion: 1,
		CreatedBy:     "branch-supervisor-1",
		Content:       resJSON,
	})
	if err != nil {
		t.Fatal(err)
	}

	rule := NewEscalationForwardsUpward()

	payload, _ := json.Marshal(escalationForwardPayload{
		WorkerID:       "worker-1",
		TaskID:         "task-42",
		EscalationType: "task.blocked",
		Reason:         "dependency missing",
	})

	evt := bus.Event{
		ID:        "evt-1",
		Type:      "worker.escalation.requested",
		Timestamp: time.Now(),
		EmitterID: "worker-1",
		Scope:     bus.Scope{MissionID: "m1", BranchID: "branch-1", TaskID: "task-42"},
		Payload:   payload,
	}

	fired, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !fired {
		t.Fatal("expected rule to fire when branch resolution failed")
	}
}

func TestEscalationForwardsUpward_Evaluate_NoResolutionAttempt(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	l, err := ledger.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	rule := NewEscalationForwardsUpward()

	payload, _ := json.Marshal(escalationForwardPayload{
		WorkerID:       "worker-1",
		TaskID:         "task-42",
		EscalationType: "task.blocked",
		Reason:         "dependency missing",
	})

	evt := bus.Event{
		ID:        "evt-1",
		Type:      "worker.escalation.requested",
		Timestamp: time.Now(),
		EmitterID: "worker-1",
		Scope:     bus.Scope{MissionID: "m1", BranchID: "branch-1", TaskID: "task-42"},
		Payload:   payload,
	}

	fired, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if fired {
		t.Fatal("expected rule NOT to fire when no failed resolution attempt exists")
	}
}

func TestEscalationForwardsUpward_Action(t *testing.T) {
	dir := t.TempDir()
	b, err := bus.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	rule := NewEscalationForwardsUpward()

	payload, _ := json.Marshal(escalationForwardPayload{
		WorkerID:       "worker-1",
		TaskID:         "task-42",
		EscalationType: "task.blocked",
		Reason:         "dependency missing",
	})

	evt := bus.Event{
		ID:        "evt-1",
		Type:      "worker.escalation.requested",
		Timestamp: time.Now(),
		EmitterID: "worker-1",
		Scope:     bus.Scope{MissionID: "m1", BranchID: "branch-1", TaskID: "task-42"},
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

	if published[0].Type != "supervisor.escalation.forwarded" {
		t.Errorf("event type = %s, want supervisor.escalation.forwarded", published[0].Type)
	}
	// Should be scoped to mission only.
	if published[0].Scope.BranchID != "" {
		t.Errorf("expected empty BranchID for mission-level escalation, got %s", published[0].Scope.BranchID)
	}
}
