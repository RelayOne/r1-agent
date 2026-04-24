package crossteam

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/ledger"
)

func TestModificationRequiresCTO_Evaluate_CrossBranchFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	l, err := ledger.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	// Add SDM cross-branch flag.
	flagJSON, _ := json.Marshal(sdmFlagContent{
		FilePath:       "shared/api.go",
		IsCrossBranch:  true,
		AffectedBranch: "branch-2",
		LeadEngineer:   "lead-eng-2",
	})
	_, err = l.AddNode(ctx, ledger.Node{
		Type:          "sdm.cross_branch_flag",
		SchemaVersion: 1,
		CreatedBy:     "sdm-1",
		Content:       flagJSON,
	})
	if err != nil {
		t.Fatal(err)
	}

	rule := NewModificationRequiresCTO()

	payload, _ := json.Marshal(crossTeamActionPayload{
		WorkerID:   "worker-1",
		ActionType: "edit",
		FilePaths:  []string{"shared/api.go"},
	})

	evt := bus.Event{
		ID:        "evt-1",
		Type:      "worker.action.proposed",
		Timestamp: time.Now(),
		EmitterID: "worker-1",
		Scope:     bus.Scope{MissionID: "m1", BranchID: "branch-1"},
		Payload:   payload,
	}

	fired, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !fired {
		t.Fatal("expected rule to fire for cross-branch file")
	}
}

func TestModificationRequiresCTO_Evaluate_NoCrossBranch(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	l, err := ledger.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	rule := NewModificationRequiresCTO()

	payload, _ := json.Marshal(crossTeamActionPayload{
		WorkerID:   "worker-1",
		ActionType: "edit",
		FilePaths:  []string{"internal/foo.go"},
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
		t.Fatal("expected rule NOT to fire when no cross-branch flag exists")
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

	payload, _ := json.Marshal(crossTeamActionPayload{
		WorkerID:   "worker-1",
		ActionType: "edit",
		FilePaths:  []string{"shared/api.go"},
	})

	evt := bus.Event{
		ID:        "evt-1",
		Type:      "worker.action.proposed",
		Timestamp: time.Now(),
		EmitterID: "worker-1",
		Scope:     bus.Scope{MissionID: "m1", BranchID: "branch-1"},
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
