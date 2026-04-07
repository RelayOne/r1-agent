package drift

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/ericmacdougall/stoke/internal/bus"
	"github.com/ericmacdougall/stoke/internal/ledger"
)

func TestBudgetThreshold_Evaluate_BelowWarning(t *testing.T) {
	ctx := context.Background()
	lDir := t.TempDir()
	l, err := ledger.New(lDir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	rule := NewBudgetThreshold()
	payload, _ := json.Marshal(budgetPayload{SpentUSD: 4.0, BudgetUSD: 10.0}) // 40%
	evt := bus.Event{
		ID:        "budget-1",
		Type:      "mission.budget.update",
		Timestamp: time.Now(),
		Scope:     bus.Scope{MissionID: "m1"},
		Payload:   payload,
	}

	fire, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if fire {
		t.Fatal("expected rule NOT to fire below warning threshold")
	}
}

func TestBudgetThreshold_Evaluate_AtWarning(t *testing.T) {
	ctx := context.Background()
	lDir := t.TempDir()
	l, err := ledger.New(lDir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	rule := NewBudgetThreshold()
	payload, _ := json.Marshal(budgetPayload{SpentUSD: 5.0, BudgetUSD: 10.0}) // 50%
	evt := bus.Event{
		ID:        "budget-2",
		Type:      "mission.budget.update",
		Timestamp: time.Now(),
		Scope:     bus.Scope{MissionID: "m1"},
		Payload:   payload,
	}

	fire, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !fire {
		t.Fatal("expected rule to fire at warning threshold")
	}
}

func TestBudgetThreshold_Action_Warning(t *testing.T) {
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

	rule := NewBudgetThreshold()
	payload, _ := json.Marshal(budgetPayload{SpentUSD: 5.5, BudgetUSD: 10.0}) // 55%
	evt := bus.Event{
		ID:      "budget-3",
		Type:    "mission.budget.update",
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
		t.Fatal("expected warning event")
	}
	if published[0].Type != "mission.budget.warning" {
		t.Errorf("type = %s, want mission.budget.warning", published[0].Type)
	}
}

func TestBudgetThreshold_Action_HardStop(t *testing.T) {
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

	rule := NewBudgetThreshold()
	payload, _ := json.Marshal(budgetPayload{SpentUSD: 12.5, BudgetUSD: 10.0}) // 125%
	evt := bus.Event{
		ID:      "budget-4",
		Type:    "mission.budget.update",
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
		t.Fatal("expected hard_stop event")
	}
	if published[0].Type != "mission.budget.hard_stop" {
		t.Errorf("type = %s, want mission.budget.hard_stop", published[0].Type)
	}
}
