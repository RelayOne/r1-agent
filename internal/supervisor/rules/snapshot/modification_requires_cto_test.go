package snapshot

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1-agent/internal/bus"
	"github.com/RelayOne/r1-agent/internal/ledger"
)

func TestModificationRequiresCTO_Evaluate_SnapshotFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	l, err := ledger.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	rule := NewModificationRequiresCTO()

	payload, _ := json.Marshal(actionPayload{
		WorkerID:   "worker-1",
		ActionType: "edit",
		FilePaths:  []string{"snapshot/main.go"},
		IsSnapshot: true,
	})

	evt := bus.Event{
		ID:        "evt-1",
		Type:      "worker.action.proposed",
		Timestamp: time.Now(),
		EmitterID: "worker-1",
		Scope:     bus.Scope{MissionID: "m1"},
		Payload:   payload,
	}

	fired, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !fired {
		t.Fatal("expected rule to fire for snapshot file modification")
	}
}

func TestModificationRequiresCTO_Evaluate_NonSnapshot(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	l, err := ledger.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	rule := NewModificationRequiresCTO()

	payload, _ := json.Marshal(actionPayload{
		WorkerID:   "worker-1",
		ActionType: "edit",
		FilePaths:  []string{"src/main.go"},
		IsSnapshot: false,
	})

	evt := bus.Event{
		ID:        "evt-1",
		Type:      "worker.action.proposed",
		Timestamp: time.Now(),
		EmitterID: "worker-1",
		Scope:     bus.Scope{MissionID: "m1"},
		Payload:   payload,
	}

	fired, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if fired {
		t.Fatal("expected rule NOT to fire for non-snapshot file")
	}
}

func TestModificationRequiresCTO_Evaluate_WithApproval(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	l, err := ledger.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	// Add CTO approval.
	approvalJSON, _ := json.Marshal(ctoApprovalContent{
		FilePaths: []string{"snapshot/main.go"},
		Approved:  true,
	})
	_, err = l.AddNode(ctx, ledger.Node{
		Type:          "cto.approval",
		SchemaVersion: 1,
		CreatedBy:     "cto-1",
		Content:       approvalJSON,
	})
	if err != nil {
		t.Fatal(err)
	}

	rule := NewModificationRequiresCTO()

	payload, _ := json.Marshal(actionPayload{
		WorkerID:   "worker-1",
		ActionType: "edit",
		FilePaths:  []string{"snapshot/main.go"},
		IsSnapshot: true,
	})

	evt := bus.Event{
		ID:        "evt-1",
		Type:      "worker.action.proposed",
		Timestamp: time.Now(),
		EmitterID: "worker-1",
		Scope:     bus.Scope{MissionID: "m1"},
		Payload:   payload,
	}

	fired, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if fired {
		t.Fatal("expected rule NOT to fire when CTO approval exists")
	}
}

func TestModificationRequiresCTO_Action(t *testing.T) {
	dir := t.TempDir()
	b, err := bus.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	rule := NewModificationRequiresCTO()

	payload, _ := json.Marshal(actionPayload{
		WorkerID:   "worker-1",
		ActionType: "edit",
		FilePaths:  []string{"snapshot/main.go"},
		IsSnapshot: true,
	})

	evt := bus.Event{
		ID:        "evt-1",
		Type:      "worker.action.proposed",
		Timestamp: time.Now(),
		EmitterID: "worker-1",
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

	if published[0].Type != bus.EvtWorkerPaused {
		t.Errorf("first event type = %s, want %s", published[0].Type, bus.EvtWorkerPaused)
	}
	if published[1].Type != "supervisor.spawn.requested" {
		t.Errorf("second event type = %s, want supervisor.spawn.requested", published[1].Type)
	}
}
