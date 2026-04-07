package hierarchy

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/ericmacdougall/stoke/internal/bus"
	"github.com/ericmacdougall/stoke/internal/ledger"
)

func TestUserEscalation_Evaluate_NoResolution(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	l, err := ledger.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	rule := NewUserEscalation()

	payload, _ := json.Marshal(forwardedEscalationPayload{
		WorkerID:       "worker-1",
		TaskID:         "task-42",
		EscalationType: "task.blocked",
		Reason:         "dependency missing",
		SourceBranch:   "branch-1",
		Level:          "mission",
	})

	evt := bus.Event{
		ID:        "evt-1",
		Type:      "supervisor.escalation.forwarded",
		Timestamp: time.Now(),
		EmitterID: "branch-supervisor-1",
		Scope:     bus.Scope{MissionID: "m1"},
		Payload:   payload,
	}

	fired, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !fired {
		t.Fatal("expected rule to fire when no mission resolution exists")
	}
}

func TestUserEscalation_Evaluate_AlreadyResolved(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	l, err := ledger.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	// Add mission resolution.
	resJSON, _ := json.Marshal(map[string]any{
		"task_id":  "task-42",
		"resolved": true,
	})
	_, err = l.AddNode(ctx, ledger.Node{
		Type:          "escalation.mission_resolution",
		SchemaVersion: 1,
		CreatedBy:     "mission-supervisor",
		Content:       resJSON,
	})
	if err != nil {
		t.Fatal(err)
	}

	rule := NewUserEscalation()

	payload, _ := json.Marshal(forwardedEscalationPayload{
		WorkerID:       "worker-1",
		TaskID:         "task-42",
		EscalationType: "task.blocked",
		Reason:         "dependency missing",
	})

	evt := bus.Event{
		ID:        "evt-1",
		Type:      "supervisor.escalation.forwarded",
		Timestamp: time.Now(),
		EmitterID: "branch-supervisor-1",
		Scope:     bus.Scope{MissionID: "m1"},
		Payload:   payload,
	}

	fired, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if fired {
		t.Fatal("expected rule NOT to fire when mission resolution exists")
	}
}

func TestUserEscalation_Action_Interactive(t *testing.T) {
	dir := t.TempDir()
	b, err := bus.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	rule := NewUserEscalation()
	rule.Mode = ModeInteractive

	payload, _ := json.Marshal(forwardedEscalationPayload{
		WorkerID:       "worker-1",
		TaskID:         "task-42",
		EscalationType: "task.blocked",
		Reason:         "dependency missing",
		SourceBranch:   "branch-1",
	})

	evt := bus.Event{
		ID:        "evt-1",
		Type:      "supervisor.escalation.forwarded",
		Timestamp: time.Now(),
		EmitterID: "branch-supervisor-1",
		Scope:     bus.Scope{MissionID: "m1"},
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
	if published[0].Type != "supervisor.user.message" {
		t.Errorf("first event type = %s, want supervisor.user.message", published[0].Type)
	}
	if published[1].Type != bus.EvtWorkerPaused {
		t.Errorf("second event type = %s, want %s", published[1].Type, bus.EvtWorkerPaused)
	}
}

func TestUserEscalation_Action_FullAuto(t *testing.T) {
	dir := t.TempDir()
	b, err := bus.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	rule := NewUserEscalation()
	rule.Mode = ModeFullAuto

	payload, _ := json.Marshal(forwardedEscalationPayload{
		WorkerID:       "worker-1",
		TaskID:         "task-42",
		EscalationType: "task.blocked",
		Reason:         "dependency missing",
		SourceBranch:   "branch-1",
	})

	evt := bus.Event{
		ID:        "evt-1",
		Type:      "supervisor.escalation.forwarded",
		Timestamp: time.Now(),
		EmitterID: "branch-supervisor-1",
		Scope:     bus.Scope{MissionID: "m1"},
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
	if published[0].Type != "supervisor.spawn.requested" {
		t.Errorf("event type = %s, want supervisor.spawn.requested", published[0].Type)
	}

	var spawnData map[string]any
	if err := json.Unmarshal(published[0].Payload, &spawnData); err != nil {
		t.Fatal(err)
	}
	if spawnData["role"] != "Stakeholder" {
		t.Errorf("role = %s, want Stakeholder", spawnData["role"])
	}
}
