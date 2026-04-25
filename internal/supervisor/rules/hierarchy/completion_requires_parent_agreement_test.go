package hierarchy

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1-agent/internal/bus"
	"github.com/RelayOne/r1-agent/internal/ledger"
)

func TestCompletionRequiresParentAgreement_Evaluate_AlwaysFires(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	l, err := ledger.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	rule := NewCompletionRequiresParentAgreement()

	payload, _ := json.Marshal(branchCompletionPayload{
		BranchID:   "branch-1",
		ProposerID: "branch-supervisor-1",
	})

	evt := bus.Event{
		ID:        "evt-1",
		Type:      "supervisor.branch.completion.proposed",
		Timestamp: time.Now(),
		EmitterID: "branch-supervisor-1",
		Scope:     bus.Scope{MissionID: "m1", BranchID: "branch-1"},
		Payload:   payload,
	}

	fired, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !fired {
		t.Fatal("expected rule to always fire")
	}
}

func TestCompletionRequiresParentAgreement_Action_Agree(t *testing.T) {
	dir := t.TempDir()
	b, err := bus.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	rule := NewCompletionRequiresParentAgreement()

	payload, _ := json.Marshal(branchCompletionPayload{
		BranchID:   "branch-1",
		ProposerID: "branch-supervisor-1",
		TasksTotal: 5,
		TasksDone:  5,
		VerifyPass: true,
	})

	evt := bus.Event{
		ID:        "evt-1",
		Type:      "supervisor.branch.completion.proposed",
		Timestamp: time.Now(),
		EmitterID: "branch-supervisor-1",
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

	var decision map[string]any
	if err := json.Unmarshal(published[0].Payload, &decision); err != nil {
		t.Fatal(err)
	}
	if decision["decision"] != "agree" {
		t.Errorf("decision = %s, want agree", decision["decision"])
	}
}

func TestCompletionRequiresParentAgreement_Action_Dissent_IncompleteTasks(t *testing.T) {
	dir := t.TempDir()
	b, err := bus.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	rule := NewCompletionRequiresParentAgreement()

	payload, _ := json.Marshal(branchCompletionPayload{
		BranchID:   "branch-1",
		ProposerID: "branch-supervisor-1",
		TasksTotal: 5,
		TasksDone:  3,
		VerifyPass: true,
	})

	evt := bus.Event{
		ID:        "evt-1",
		Type:      "supervisor.branch.completion.proposed",
		Timestamp: time.Now(),
		EmitterID: "branch-supervisor-1",
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

	var decision map[string]any
	if err := json.Unmarshal(published[0].Payload, &decision); err != nil {
		t.Fatal(err)
	}
	if decision["decision"] != "dissent" {
		t.Errorf("decision = %s, want dissent", decision["decision"])
	}
}
