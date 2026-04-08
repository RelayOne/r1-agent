package sdm

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/ericmacdougall/stoke/internal/bus"
	"github.com/ericmacdougall/stoke/internal/ledger"
)

func TestDuplicateWorkDetected_Evaluate_ActionOverlap(t *testing.T) {
	ctx := context.Background()
	lDir := t.TempDir()
	l, err := ledger.New(lDir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	// Record active work in branch-A.
	workContent, _ := json.Marshal(workDescriptor{
		BranchID:  "branch-A",
		FilePaths: []string{"pkg/handler.go", "pkg/model.go"},
	})
	_, err = l.AddNode(ctx, ledger.Node{
		Type:          "active_work",
		SchemaVersion: 1,
		CreatedBy:     "worker-1",
		MissionID:     "m1",
		Content:       workContent,
	})
	if err != nil {
		t.Fatal(err)
	}

	rule := NewDuplicateWorkDetected()
	payload, _ := json.Marshal(actionProposedPayload{
		WorkerID:  "worker-2",
		BranchID:  "branch-B",
		FilePaths: []string{"pkg/handler.go"},
	})
	evt := bus.Event{
		ID:        "prop-1",
		Type:      "worker.action.proposed",
		Timestamp: time.Now(),
		Scope:     bus.Scope{MissionID: "m1", BranchID: "branch-B"},
		Payload:   payload,
	}

	fire, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !fire {
		t.Fatal("expected rule to fire on duplicate work across branches")
	}
}

func TestDuplicateWorkDetected_Evaluate_NoOverlap(t *testing.T) {
	ctx := context.Background()
	lDir := t.TempDir()
	l, err := ledger.New(lDir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	workContent, _ := json.Marshal(workDescriptor{
		BranchID:  "branch-A",
		FilePaths: []string{"pkg/handler.go"},
	})
	_, err = l.AddNode(ctx, ledger.Node{
		Type:          "active_work",
		SchemaVersion: 1,
		CreatedBy:     "worker-1",
		MissionID:     "m1",
		Content:       workContent,
	})
	if err != nil {
		t.Fatal(err)
	}

	rule := NewDuplicateWorkDetected()
	payload, _ := json.Marshal(actionProposedPayload{
		WorkerID:  "worker-2",
		BranchID:  "branch-B",
		FilePaths: []string{"pkg/other.go"},
	})
	evt := bus.Event{
		ID:      "prop-2",
		Type:    "worker.action.proposed",
		Scope:   bus.Scope{MissionID: "m1", BranchID: "branch-B"},
		Payload: payload,
	}

	fire, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if fire {
		t.Fatal("expected rule NOT to fire when files don't overlap")
	}
}

func TestDuplicateWorkDetected_Action(t *testing.T) {
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

	rule := NewDuplicateWorkDetected()
	evt := bus.Event{
		ID:    "prop-3",
		Type:  "worker.action.proposed",
		Scope: bus.Scope{MissionID: "m1"},
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
		t.Fatal("expected advisory event")
	}
	if published[0].Type != "sdm.duplicate_work.detected" {
		t.Errorf("type = %s, want sdm.duplicate_work.detected", published[0].Type)
	}
}
