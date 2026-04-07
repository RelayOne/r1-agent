package skill

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ericmacdougall/stoke/internal/bus"
	"github.com/ericmacdougall/stoke/internal/ledger"
)

func TestContradictsOutcome_Evaluate_NegativeWithSkills(t *testing.T) {
	ctx := context.Background()
	lDir := t.TempDir()
	l, err := ledger.New(lDir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	rule := NewContradictsOutcome()
	payload, _ := json.Marshal(loopTerminalPayload{
		Outcome:    "failed",
		SkillsUsed: []string{"sk-1", "sk-2"},
		IsNegative: true,
	})
	evt := bus.Event{
		ID:        "esc-1",
		Type:      "loop.escalated",
		Timestamp: time.Now(),
		Scope:     bus.Scope{MissionID: "m1", LoopID: "loop-1"},
		Payload:   payload,
	}

	fire, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !fire {
		t.Fatal("expected rule to fire on negative outcome with skills")
	}
}

func TestContradictsOutcome_Evaluate_PositiveOutcome(t *testing.T) {
	ctx := context.Background()
	lDir := t.TempDir()
	l, err := ledger.New(lDir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	rule := NewContradictsOutcome()
	payload, _ := json.Marshal(loopTerminalPayload{
		Outcome:    "success",
		SkillsUsed: []string{"sk-1"},
		IsNegative: false,
	})
	evt := bus.Event{
		ID:      "esc-2",
		Type:    "loop.escalated",
		Scope:   bus.Scope{MissionID: "m1"},
		Payload: payload,
	}

	fire, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if fire {
		t.Fatal("expected rule NOT to fire on positive outcome")
	}
}

func TestContradictsOutcome_Action(t *testing.T) {
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

	rule := NewContradictsOutcome()
	payload, _ := json.Marshal(loopTerminalPayload{
		IsNegative: true,
		SkillsUsed: []string{"sk-1"},
	})
	evt := bus.Event{
		ID:      "esc-3",
		Type:    "loop.escalated",
		Scope:   bus.Scope{MissionID: "m1", LoopID: "loop-1"},
		Payload: payload,
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
