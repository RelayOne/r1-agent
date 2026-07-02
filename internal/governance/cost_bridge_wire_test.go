package governance

// Test for the CostBridge wiring (audit A055): a hub model.post_call
// event through the Governor's handler must publish a cost.recorded bus
// event and write a cost_record ledger node — previously no production
// path constructed any bridge, so neither ever happened.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/costtrack"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/ledger"
)

func TestGovernorRoutesCostThroughBridge(t *testing.T) {
	// Zero budget: cost.recorded must fire even when the budget rule
	// path is disabled.
	g := newTestGovernor(t, 0)

	events, cancel := collect(t, g.Bus(), "cost.recorded")
	defer cancel()

	sub := g.HubSubscriber()
	sub.Handler(context.Background(), &hub.Event{
		Type:   hub.EventModelPostCall,
		TaskID: "task-7",
		Model: &hub.ModelEvent{
			Provider:     "anthropic",
			Model:        "claude",
			InputTokens:  100,
			OutputTokens: 50,
			CostUSD:      0.42,
		},
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(events()) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	got := events()
	if len(got) == 0 {
		t.Fatal("no cost.recorded event published for model.post_call")
	}
	var usage costtrack.Usage
	if err := json.Unmarshal(got[0].Payload, &usage); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if usage.Cost != 0.42 || usage.Model != "claude" || usage.TaskID != "task-7" {
		t.Errorf("usage payload = %+v, want cost=0.42 model=claude task=task-7", usage)
	}
	if usage.InputTokens != 100 || usage.OutputTokens != 50 {
		t.Errorf("token counts = %d/%d, want 100/50", usage.InputTokens, usage.OutputTokens)
	}
	if got[0].Scope.MissionID != "mission-test" {
		t.Errorf("event mission scope = %q, want mission-test", got[0].Scope.MissionID)
	}

	nodes, err := g.Ledger().Query(context.Background(), ledger.QueryFilter{Type: "cost_record", MissionID: "mission-test"})
	if err != nil {
		t.Fatalf("ledger query: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("cost_record ledger nodes = %d, want 1", len(nodes))
	}
}

func TestGovernorCostBridgeSkipsZeroCost(t *testing.T) {
	g := newTestGovernor(t, 0)
	events, cancel := collect(t, g.Bus(), "cost.recorded")
	defer cancel()

	sub := g.HubSubscriber()
	sub.Handler(context.Background(), &hub.Event{
		Type:  hub.EventModelPostCall,
		Model: &hub.ModelEvent{Provider: "anthropic", Model: "claude", CostUSD: 0},
	})

	time.Sleep(150 * time.Millisecond)
	if n := len(events()); n != 0 {
		t.Errorf("zero-cost post_call published %d cost.recorded events, want 0", n)
	}
}
