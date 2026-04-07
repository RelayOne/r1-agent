package skill

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ericmacdougall/stoke/internal/bus"
	"github.com/ericmacdougall/stoke/internal/ledger"
)

func TestApplicationRequiresReview_Evaluate_Tentative(t *testing.T) {
	ctx := context.Background()
	lDir := t.TempDir()
	l, err := ledger.New(lDir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	rule := NewApplicationRequiresReview()
	payload, _ := json.Marshal(skillAppliedPayload{SkillID: "sk-1", Confidence: "tentative"})
	evt := bus.Event{
		ID:        "applied-1",
		Type:      bus.EvtSkillApplied,
		Timestamp: time.Now(),
		Scope:     bus.Scope{MissionID: "m1"},
		Payload:   payload,
	}

	fire, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !fire {
		t.Fatal("expected rule to fire for tentative confidence")
	}
}

func TestApplicationRequiresReview_Evaluate_Proven(t *testing.T) {
	ctx := context.Background()
	lDir := t.TempDir()
	l, err := ledger.New(lDir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	rule := NewApplicationRequiresReview()
	payload, _ := json.Marshal(skillAppliedPayload{SkillID: "sk-2", Confidence: "proven"})
	evt := bus.Event{
		ID:      "applied-2",
		Type:    bus.EvtSkillApplied,
		Scope:   bus.Scope{MissionID: "m1"},
		Payload: payload,
	}

	fire, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if fire {
		t.Fatal("expected rule NOT to fire for proven confidence")
	}
}

func TestApplicationRequiresReview_Action(t *testing.T) {
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

	rule := NewApplicationRequiresReview()
	payload, _ := json.Marshal(skillAppliedPayload{SkillID: "sk-1", Confidence: "tentative"})
	evt := bus.Event{
		ID:      "applied-3",
		Type:    bus.EvtSkillApplied,
		Scope:   bus.Scope{MissionID: "m1"},
		Payload: payload,
	}

	if err := rule.Action(context.Background(), evt, b); err != nil {
		t.Fatalf("Action: %v", err)
	}
	if len(published) < 1 {
		t.Fatal("expected review queued event")
	}
	if published[0].Type != "skill.review.queued" {
		t.Errorf("type = %s, want skill.review.queued", published[0].Type)
	}
}
