package bridge

import (
	"context"
	"encoding/json"
	"time"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/costtrack"
	"github.com/RelayOne/r1/internal/ledger"
)

// CostBridge wraps a costtrack.Tracker and emits bus events on cost changes.
type CostBridge struct {
	tracker *costtrack.Tracker
	bus     *bus.Bus
	ledger  *ledger.Ledger
}

// NewCostBridge creates a CostBridge with the given budget. Budget <= 0 means unlimited.
func NewCostBridge(b *bus.Bus, l *ledger.Ledger, budget float64) *CostBridge {
	cb := &CostBridge{
		bus:    b,
		ledger: l,
	}
	cb.tracker = costtrack.NewTracker(budget, func(alert costtrack.Alert) {
		payload, _ := json.Marshal(alert)
		_ = b.Publish(bus.Event{
			Type:      EvtBudgetAlert,
			Timestamp: time.Now(),
			EmitterID: "bridge.cost",
			Payload:   payload,
		})
	})
	return cb
}

// Record logs a usage event, publishes a bus event, writes a ledger node, and returns the cost.
func (cb *CostBridge) Record(model, taskID string, inputTokens, outputTokens, cacheRead, cacheWrite int) float64 {
	cost := cb.tracker.Record(model, taskID, inputTokens, outputTokens, cacheRead, cacheWrite)

	usage := costtrack.Usage{
		Model:        model,
		TaskID:       taskID,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		CacheRead:    cacheRead,
		CacheWrite:   cacheWrite,
		Cost:         cost,
		Timestamp:    time.Now(),
	}
	payload, _ := json.Marshal(usage)

	_ = cb.bus.Publish(bus.Event{
		Type:      EvtCostRecorded,
		Timestamp: time.Now(),
		EmitterID: "bridge.cost",
		Scope:     bus.Scope{TaskID: taskID},
		Payload:   payload,
	})

	_, _ = cb.ledger.AddNode(context.Background(), ledger.Node{
		Type:          "cost_record",
		SchemaVersion: 1,
		CreatedBy:     "bridge.cost",
		Content:       payload,
	})

	return cost
}

// Total returns total cost so far.
func (cb *CostBridge) Total() float64 {
	return cb.tracker.Total()
}

// ByModel returns cost breakdown by model.
func (cb *CostBridge) ByModel() map[string]float64 {
	return cb.tracker.ByModel()
}

// ByTask returns cost breakdown by task.
func (cb *CostBridge) ByTask() map[string]float64 {
	return cb.tracker.ByTask()
}

// OverBudget returns true if spending exceeds the budget.
func (cb *CostBridge) OverBudget() bool {
	return cb.tracker.OverBudget()
}
