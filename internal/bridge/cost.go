package bridge

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"time"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/costtrack"
	"github.com/RelayOne/r1/internal/ledger"
)

// CostBridge wraps a costtrack.Tracker and emits bus events on cost changes.
//
// Event-loss observability: every emit failure (json.Marshal,
// bus.Publish, ledger.AddNode) increments emitErrors and updates
// lastEmitError. Operators can poll EmitErrorCount / LastEmitError
// to detect V2 governance event loss without instrumenting the
// callers. Callers that want push-style notification can wire
// OnEmitError via SetOnEmitError. Closes audit/scan-go-stubs.md
// item "internal/bridge/cost.go error swallow".
type CostBridge struct {
	tracker *costtrack.Tracker
	bus     *bus.Bus
	ledger  *ledger.Ledger

	emitErrors    atomic.Uint64
	lastEmitError atomic.Pointer[bridgeEmitError]
	onEmitError   atomic.Pointer[func(stage string, err error)]
}

// bridgeEmitError pairs an emit-stage label with the underlying
// error so LastEmitError can answer both "what failed" and
// "where". Stored via atomic.Pointer so concurrent reads see a
// stable snapshot without locking.
type bridgeEmitError struct {
	Stage string
	Err   error
	When  time.Time
}

// NewCostBridge creates a CostBridge with the given budget. Budget <= 0 means unlimited.
func NewCostBridge(b *bus.Bus, l *ledger.Ledger, budget float64) *CostBridge {
	cb := &CostBridge{
		bus:    b,
		ledger: l,
	}
	cb.tracker = costtrack.NewTracker(budget, func(alert costtrack.Alert) {
		payload, err := json.Marshal(alert)
		if err != nil {
			cb.recordEmitError("alert.marshal", err)
			return
		}
		if err := b.Publish(bus.Event{
			Type:      EvtBudgetAlert,
			Timestamp: time.Now(),
			EmitterID: "bridge.cost",
			Payload:   payload,
		}); err != nil {
			cb.recordEmitError("alert.publish", err)
		}
	})
	return cb
}

// SetOnEmitError installs a callback fired every time an emit
// stage (marshal, bus.Publish, ledger.AddNode) fails. Pass nil
// to clear. The callback runs on the goroutine of the offending
// Record call (or the costtrack alert dispatcher) so it MUST NOT
// block — wrap in a goroutine if your handler does I/O.
func (cb *CostBridge) SetOnEmitError(fn func(stage string, err error)) {
	if fn == nil {
		cb.onEmitError.Store(nil)
		return
	}
	cb.onEmitError.Store(&fn)
}

// EmitErrorCount returns the cumulative number of emit failures
// since this CostBridge was created. Includes marshal failures,
// bus.Publish failures, and ledger.AddNode failures across all
// Record calls and budget-alert dispatches.
func (cb *CostBridge) EmitErrorCount() uint64 {
	return cb.emitErrors.Load()
}

// LastEmitError returns the most recent emit failure (with stage
// + timestamp) or nil if there have been none. Operators tail
// this for governance observability.
func (cb *CostBridge) LastEmitError() (stage string, err error, when time.Time, ok bool) {
	last := cb.lastEmitError.Load()
	if last == nil {
		return "", nil, time.Time{}, false
	}
	return last.Stage, last.Err, last.When, true
}

func (cb *CostBridge) recordEmitError(stage string, err error) {
	cb.emitErrors.Add(1)
	cb.lastEmitError.Store(&bridgeEmitError{Stage: stage, Err: err, When: time.Now()})
	if cbk := cb.onEmitError.Load(); cbk != nil {
		(*cbk)(stage, err)
	}
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
	payload, err := json.Marshal(usage)
	if err != nil {
		// Marshaling a concrete struct with no func/chan fields
		// shouldn't fail in practice, but record + return early
		// rather than emit empty payloads downstream.
		cb.recordEmitError("usage.marshal", err)
		return cost
	}

	if err := cb.bus.Publish(bus.Event{
		Type:      EvtCostRecorded,
		Timestamp: time.Now(),
		EmitterID: "bridge.cost",
		Scope:     bus.Scope{TaskID: taskID},
		Payload:   payload,
	}); err != nil {
		cb.recordEmitError("usage.publish", err)
	}

	if _, err := cb.ledger.AddNode(context.Background(), ledger.Node{
		Type:          "cost_record",
		SchemaVersion: 1,
		CreatedBy:     "bridge.cost",
		Content:       payload,
	}); err != nil {
		cb.recordEmitError("ledger.add_node", err)
	}

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
