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

func TestDependencyCrossed_Evaluate_CrossBranch(t *testing.T) {
	ctx := context.Background()
	lDir := t.TempDir()
	l, err := ledger.New(lDir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	// Create a task_dag node in branch-A.
	depContent, _ := json.Marshal(taskDAGContent{
		TaskID:   "task-1",
		BranchID: "branch-A",
	})
	_, err = l.AddNode(ctx, ledger.Node{
		Type:          "task_dag",
		SchemaVersion: 1,
		CreatedBy:     "planner",
		MissionID:     "m1",
		Content:       depContent,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Create a task_dag node in branch-B that depends on task-1 (branch-A).
	newContent, _ := json.Marshal(taskDAGContent{
		TaskID:    "task-2",
		BranchID:  "branch-B",
		DependsOn: []string{"task-1"},
	})
	nodeID, err := l.AddNode(ctx, ledger.Node{
		Type:          "task_dag",
		SchemaVersion: 1,
		CreatedBy:     "planner",
		MissionID:     "m1",
		Content:       newContent,
	})
	if err != nil {
		t.Fatal(err)
	}

	rule := NewDependencyCrossed()
	payload, _ := json.Marshal(taskDAGPayload{NodeID: nodeID, NodeType: "task_dag"})
	evt := bus.Event{
		ID:        "nodeadd-1",
		Type:      bus.EvtLedgerNodeAdded,
		Timestamp: time.Now(),
		Scope:     bus.Scope{MissionID: "m1", BranchID: "branch-B"},
		Payload:   payload,
	}

	fire, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !fire {
		t.Fatal("expected rule to fire on cross-branch dependency")
	}
}

func TestDependencyCrossed_Evaluate_SameBranch(t *testing.T) {
	ctx := context.Background()
	lDir := t.TempDir()
	l, err := ledger.New(lDir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	depContent, _ := json.Marshal(taskDAGContent{
		TaskID:   "task-1",
		BranchID: "branch-A",
	})
	_, err = l.AddNode(ctx, ledger.Node{
		Type:          "task_dag",
		SchemaVersion: 1,
		CreatedBy:     "planner",
		MissionID:     "m1",
		Content:       depContent,
	})
	if err != nil {
		t.Fatal(err)
	}

	newContent, _ := json.Marshal(taskDAGContent{
		TaskID:    "task-2",
		BranchID:  "branch-A",
		DependsOn: []string{"task-1"},
	})
	nodeID, err := l.AddNode(ctx, ledger.Node{
		Type:          "task_dag",
		SchemaVersion: 1,
		CreatedBy:     "planner",
		MissionID:     "m1",
		Content:       newContent,
	})
	if err != nil {
		t.Fatal(err)
	}

	rule := NewDependencyCrossed()
	payload, _ := json.Marshal(taskDAGPayload{NodeID: nodeID, NodeType: "task_dag"})
	evt := bus.Event{
		ID:      "nodeadd-2",
		Type:    bus.EvtLedgerNodeAdded,
		Scope:   bus.Scope{MissionID: "m1", BranchID: "branch-A"},
		Payload: payload,
	}

	fire, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if fire {
		t.Fatal("expected rule NOT to fire for same-branch dependency")
	}
}

func TestDependencyCrossed_Action(t *testing.T) {
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

	rule := NewDependencyCrossed()
	evt := bus.Event{
		ID:    "nodeadd-3",
		Type:  bus.EvtLedgerNodeAdded,
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
	if published[0].Type != "sdm.dependency.crossed" {
		t.Errorf("type = %s, want sdm.dependency.crossed", published[0].Type)
	}
}
