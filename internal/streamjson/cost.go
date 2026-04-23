// Package streamjson — cost.go
//
// Spec-2 cloudswarm-protocol item 11: periodic cost events. Emits one
// stoke.cost system event every tick carrying the current total USD
// spend + optional token counters. Observability lane — drop-oldest
// safe so a back-pressured pipe doesn't stall the session.
//
// Usage pattern:
//
//	ct := costtrack.NewTracker(budget, alertFn)
//	stopCost := streamjson.StartCostReporter(tl, ct.Total, 5*time.Second)
//	defer stopCost()
//
// The reporter does NOT require a concrete Tracker type — any
// func() float64 that returns the running cost total works. This
// keeps cost.go free of an import cycle with internal/costtrack.
package streamjson

import (
	"sync"
	"time"
)

// CostProvider is the minimal interface the periodic cost reporter
// needs. Satisfied by costtrack.Tracker.Total, but kept as a function
// type so alternate aggregators (per-session caps, sub-tracker fans
// etc.) can be swapped in without touching this package.
type CostProvider func() float64

// StartCostReporter spawns a goroutine that emits stoke.cost every
// `interval` on the emitter's observability lane. Returns a stop
// function that blocks until the reporter exits. interval <= 0
// disables the reporter (returns a no-op stop).
//
// When emitter is nil or disabled, StartCostReporter still returns a
// valid stop function (idempotent) so callers can invoke it under
// defer regardless of emitter state.
func StartCostReporter(emitter *TwoLane, provider CostProvider, interval time.Duration) func() {
	if emitter == nil || !emitter.Enabled() || provider == nil || interval <= 0 {
		return func() {}
	}
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	var once sync.Once
	go func() {
		defer close(doneCh)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		var last float64 = -1
		for {
			select {
			case <-stopCh:
				// Exit without emitting a terminal stoke.cost line.
				// Callers that need a terminal cost figure should
				// tail the last stoke.cost tick before invoking
				// stop(); letting stop() emit would race against
				// the caller's own "complete" event, which needs
				// to be the last line on the wire.
				return
			case <-ticker.C:
				total := provider()
				// Skip emitting when nothing changed — saves a line
				// per tick on idle sessions.
				if total == last {
					continue
				}
				last = total
				emitter.EmitSystem("stoke.cost", map[string]any{
					"_stoke.dev/total_usd": total,
				})
			}
		}
	}()
	return func() {
		once.Do(func() {
			close(stopCh)
			<-doneCh
		})
	}
}
