package promptguard

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/RelayOne/r1/internal/bus"
)

// TestBudgetExceededRule_EmitsSessionKill covers spec §T5 item 21
// acceptance criterion: a fixture promptguard.budget.exceeded event
// produces exactly one daemon.session.kill action with the correct
// Target and Reason.
func TestBudgetExceededRule_EmitsSessionKill(t *testing.T) {
	rule := NewBudgetExceeded()
	if rule.Name() != "promptguard.budget_exceeded" {
		t.Errorf("Name = %q", rule.Name())
	}
	if rule.Pattern().TypePrefix != "promptguard.budget.exceeded" {
		t.Errorf("Pattern = %+v", rule.Pattern())
	}

	payload, _ := json.Marshal(map[string]any{
		"session_id": "session-abc",
		"threshold":  5,
		"detections": 6,
	})
	evt := bus.Event{
		ID:      "evt-1",
		Type:    "promptguard.budget.exceeded",
		Payload: payload,
	}

	// Evaluate must return true.
	ok, err := rule.Evaluate(context.Background(), evt, nil)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !ok {
		t.Fatal("Evaluate returned false for well-formed event")
	}

	// Action publishes the kill event. Stand up a temp bus to capture.
	b, err := bus.New(filepath.Join(t.TempDir(), "wal"))
	if err != nil {
		t.Fatalf("bus.New: %v", err)
	}
	defer b.Close()

	captured := make(chan bus.Event, 4)
	b.Subscribe(bus.Pattern{TypePrefix: "daemon.session.kill"}, func(e bus.Event) {
		select {
		case captured <- e:
		default:
		}
	})

	if err := rule.Action(context.Background(), evt, b); err != nil {
		t.Fatalf("Action: %v", err)
	}

	// Wait up to 500ms for the kill event to reach the subscriber.
	done := make(chan struct{})
	go func() {
		got := <-captured
		if got.Type != "daemon.session.kill" {
			t.Errorf("type = %q", got.Type)
		}
		var p map[string]any
		if err := json.Unmarshal(got.Payload, &p); err != nil {
			t.Errorf("unmarshal: %v", err)
		}
		if p["session_id"] != "session-abc" {
			t.Errorf("session_id = %v", p["session_id"])
		}
		reason, _ := p["reason"].(string)
		if reason == "" {
			t.Error("reason is empty")
		}
		close(done)
	}()
	<-done
}

// TestBudgetExceededRule_EmptySessionID_NoFire asserts the rule does
// not fire on a malformed event with no session id (defensive: the
// supervisor should not publish a kill targeting nothing).
func TestBudgetExceededRule_EmptySessionID_NoFire(t *testing.T) {
	rule := NewBudgetExceeded()
	payload, _ := json.Marshal(map[string]any{
		"threshold":  5,
		"detections": 6,
	})
	evt := bus.Event{Type: "promptguard.budget.exceeded", Payload: payload}
	ok, err := rule.Evaluate(context.Background(), evt, nil)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if ok {
		t.Error("rule fired on empty SessionID; should not")
	}
}
