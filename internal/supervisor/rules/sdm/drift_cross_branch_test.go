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

func TestDriftCrossBranch_Evaluate_BoundaryFile(t *testing.T) {
	ctx := context.Background()
	lDir := t.TempDir()
	l, err := ledger.New(lDir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	// Record a boundary file modification in branch-A.
	bfContent, _ := json.Marshal(map[string]string{
		"branch_id": "branch-A",
		"file_path": "api/types.go",
	})
	_, err = l.AddNode(ctx, ledger.Node{
		Type:          "boundary_file",
		SchemaVersion: 1,
		CreatedBy:     "worker-1",
		MissionID:     "m1",
		Content:       bfContent,
	})
	if err != nil {
		t.Fatal(err)
	}

	rule := NewDriftCrossBranch()
	payload, _ := json.Marshal(actionProposedPayload{
		WorkerID:  "worker-2",
		BranchID:  "branch-B",
		FilePaths: []string{"api/types.go"},
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
		t.Fatal("expected rule to fire on shared boundary file modification")
	}
}

func TestDriftCrossBranch_Evaluate_NoBoundary(t *testing.T) {
	ctx := context.Background()
	lDir := t.TempDir()
	l, err := ledger.New(lDir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	rule := NewDriftCrossBranch()
	payload, _ := json.Marshal(actionProposedPayload{
		WorkerID:  "worker-1",
		BranchID:  "branch-A",
		FilePaths: []string{"internal/foo.go"},
	})
	evt := bus.Event{
		ID:      "prop-2",
		Type:    "worker.action.proposed",
		Scope:   bus.Scope{MissionID: "m1", BranchID: "branch-A"},
		Payload: payload,
	}

	fire, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if fire {
		t.Fatal("expected rule NOT to fire when no boundary files are involved")
	}
}

func TestDriftCrossBranch_Action(t *testing.T) {
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

	rule := NewDriftCrossBranch()
	evt := bus.Event{
		ID:    "prop-3",
		Type:  "worker.action.proposed",
		Scope: bus.Scope{MissionID: "m1", BranchID: "branch-B"},
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
	if published[0].Type != "sdm.cross_branch_drift.detected" {
		t.Errorf("type = %s, want sdm.cross_branch_drift.detected", published[0].Type)
	}
}
