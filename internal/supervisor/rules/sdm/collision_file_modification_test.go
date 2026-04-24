package sdm

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/ledger"
)

func TestCollisionFileModification_Evaluate_Collision(t *testing.T) {
	ctx := context.Background()
	lDir := t.TempDir()
	l, err := ledger.New(lDir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	// Record a file modification in branch-A.
	modContent, _ := json.Marshal(map[string]any{
		"branch_id": "branch-A",
		"files":     []string{"pkg/foo.go", "pkg/bar.go"},
	})
	_, err = l.AddNode(ctx, ledger.Node{
		Type:          "file.modification",
		SchemaVersion: 1,
		CreatedBy:     "worker-1",
		MissionID:     "m1",
		Content:       modContent,
	})
	if err != nil {
		t.Fatal(err)
	}

	rule := NewCollisionFileModification()
	payload, _ := json.Marshal(actionProposedPayload{
		WorkerID:  "worker-2",
		BranchID:  "branch-B",
		FilePaths: []string{"pkg/foo.go"},
		Action:    "modify",
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
		t.Fatal("expected rule to fire on file collision across branches")
	}
}

func TestCollisionFileModification_Evaluate_SameBranch(t *testing.T) {
	ctx := context.Background()
	lDir := t.TempDir()
	l, err := ledger.New(lDir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	modContent, _ := json.Marshal(map[string]any{
		"branch_id": "branch-A",
		"files":     []string{"pkg/foo.go"},
	})
	_, err = l.AddNode(ctx, ledger.Node{
		Type:          "file.modification",
		SchemaVersion: 1,
		CreatedBy:     "worker-1",
		MissionID:     "m1",
		Content:       modContent,
	})
	if err != nil {
		t.Fatal(err)
	}

	rule := NewCollisionFileModification()
	payload, _ := json.Marshal(actionProposedPayload{
		WorkerID:  "worker-1",
		BranchID:  "branch-A",
		FilePaths: []string{"pkg/foo.go"},
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
		t.Fatal("expected rule NOT to fire for same branch")
	}
}

func TestCollisionFileModification_Action(t *testing.T) {
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

	rule := NewCollisionFileModification()
	payload, _ := json.Marshal(actionProposedPayload{
		BranchID:  "branch-B",
		FilePaths: []string{"pkg/foo.go"},
	})
	evt := bus.Event{
		ID:      "prop-3",
		Type:    "worker.action.proposed",
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
		t.Fatal("expected advisory event")
	}
	if published[0].Type != "sdm.collision.detected" {
		t.Errorf("type = %s, want sdm.collision.detected", published[0].Type)
	}
}
