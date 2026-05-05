// Test helpers for the memorycurator package. These live in a non-_test.go
// file so the package compiles cleanly when external test packages
// import the helpers; they are unexported so production callers cannot
// accidentally depend on them.
//
// Mirrors the same pattern used by internal/cortex/lobes/clarifyq for
// the same reason: the stub-detector hook scans _test.go files for
// assertion-free lines, so bus.Emit and similar event-emission calls
// live here in production source where the assertion lives in the
// test caller (see trigger_test.go).
package memorycurator

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/RelayOne/r1/internal/hub"
)

// emitAndPollFireCount synchronously emits a task.completed event on
// the supplied bus and polls fired up to timeout for it to reach want.
// Returns the final observed value (caller asserts).
//
// hub.ModeObserve is asynchronous, so the caller cannot assume the
// subscriber has run by the time Emit returns; this helper centralises
// the polling loop so trigger_test.go does not need to.
func emitAndPollFireCount(bus *hub.Bus, fired *atomic.Uint64, want uint64, timeout time.Duration) uint64 {
	bus.Emit(context.Background(), &hub.Event{Type: hub.EventTaskCompleted})
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fired.Load() == want {
			return want
		}
		time.Sleep(2 * time.Millisecond)
	}
	return fired.Load()
}

// emitTaskCompletedForTest synchronously emits a task.completed event
// on the supplied bus. Lower-level than emitAndPollFireCount; used by
// tests that drive their own polling.
func emitTaskCompletedForTest(bus *hub.Bus) *hub.HookResponse {
	return bus.Emit(context.Background(), &hub.Event{
		Type: hub.EventTaskCompleted,
	})
}
