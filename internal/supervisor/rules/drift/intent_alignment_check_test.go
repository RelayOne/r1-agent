package drift

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1-agent/internal/bus"
	"github.com/RelayOne/r1-agent/internal/ledger"
)

func TestIntentAlignmentCheck_Evaluate_AlwaysFires(t *testing.T) {
	ctx := context.Background()
	lDir := t.TempDir()
	l, err := ledger.New(lDir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	rule := NewIntentAlignmentCheck()

	evt := bus.Event{
		ID:        "milestone-1",
		Type:      "task.milestone.reached",
		Timestamp: time.Now(),
		Scope:     bus.Scope{MissionID: "m1", TaskID: "task-1"},
		Payload:   json.RawMessage(`{"milestone":"tests_passing"}`),
	}

	fire, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !fire {
		t.Fatal("expected rule to always fire on milestone events")
	}
}

func TestIntentAlignmentCheck_Action(t *testing.T) {
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

	rule := NewIntentAlignmentCheck()
	evt := bus.Event{
		ID:    "milestone-2",
		Type:  "task.milestone.reached",
		Scope: bus.Scope{MissionID: "m1", TaskID: "task-1"},
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
		t.Fatal("expected spawn event")
	}
	if published[0].Type != "supervisor.spawn.requested" {
		t.Errorf("type = %s, want supervisor.spawn.requested", published[0].Type)
	}
}
