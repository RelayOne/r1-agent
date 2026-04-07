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

func TestScheduleRiskCriticalPath_Evaluate_AtRisk(t *testing.T) {
	ctx := context.Background()
	lDir := t.TempDir()
	l, err := ledger.New(lDir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	rule := NewScheduleRiskCriticalPath()
	payload, _ := json.Marshal(timingUpdatePayload{
		TaskID:       "task-1",
		BranchID:     "branch-A",
		ProgressPct:  20,
		BlockedCount: 3,
	})
	evt := bus.Event{
		ID:        "timing-1",
		Type:      "task.timing.update",
		Timestamp: time.Now(),
		Scope:     bus.Scope{MissionID: "m1"},
		Payload:   payload,
	}

	fire, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !fire {
		t.Fatal("expected rule to fire on critical path risk")
	}
}

func TestScheduleRiskCriticalPath_Evaluate_OnTrack(t *testing.T) {
	ctx := context.Background()
	lDir := t.TempDir()
	l, err := ledger.New(lDir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	rule := NewScheduleRiskCriticalPath()
	payload, _ := json.Marshal(timingUpdatePayload{
		TaskID:       "task-1",
		BranchID:     "branch-A",
		ProgressPct:  80,
		BlockedCount: 3,
	})
	evt := bus.Event{
		ID:      "timing-2",
		Type:    "task.timing.update",
		Scope:   bus.Scope{MissionID: "m1"},
		Payload: payload,
	}

	fire, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if fire {
		t.Fatal("expected rule NOT to fire when task is on track")
	}
}

func TestScheduleRiskCriticalPath_Action(t *testing.T) {
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

	rule := NewScheduleRiskCriticalPath()
	payload, _ := json.Marshal(timingUpdatePayload{
		TaskID:       "task-1",
		BlockedCount: 3,
		ProgressPct:  20,
	})
	evt := bus.Event{
		ID:      "timing-3",
		Type:    "task.timing.update",
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
	if published[0].Type != "sdm.schedule_risk.detected" {
		t.Errorf("type = %s, want sdm.schedule_risk.detected", published[0].Type)
	}
}
