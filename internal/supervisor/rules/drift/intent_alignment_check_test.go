package drift

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ericmacdougall/stoke/internal/bus"
	"github.com/ericmacdougall/stoke/internal/ledger"
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
	b.Subscribe(bus.Pattern{}, func(e bus.Event) {
		published = append(published, e)
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
	if len(published) < 1 {
		t.Fatal("expected spawn event")
	}
	if published[0].Type != "supervisor.spawn.requested" {
		t.Errorf("type = %s, want supervisor.spawn.requested", published[0].Type)
	}
}
