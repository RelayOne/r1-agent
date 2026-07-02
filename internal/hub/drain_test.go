package hub

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestDrainWaitsForObserveHandlers asserts Drain blocks until every Phase-3
// ModeObserve goroutine has run its handler to completion. This is the
// guarantee R3 relies on: the governance bridge is a wildcard observe
// subscriber whose terminal ledger/bus writes run on these goroutines, and
// they must land before the governor closes the bus+ledger.
func TestDrainWaitsForObserveHandlers(t *testing.T) {
	b := New()
	const n = 8
	var completed atomic.Int32
	b.Register(Subscriber{
		ID:     "slow-observer",
		Events: []EventType{"*"},
		Mode:   ModeObserve,
		Handler: func(ctx context.Context, ev *Event) *HookResponse {
			// Sleep long enough that a no-op Drain would return with
			// most handlers still in flight.
			time.Sleep(30 * time.Millisecond)
			completed.Add(1)
			return nil
		},
	})

	for i := 0; i < n; i++ {
		b.Emit(context.Background(), &Event{Type: EventTaskStarted})
	}

	if err := b.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	// If Drain returned before the observe goroutines finished this would
	// be < n (a broken/no-op Drain returns immediately with ~0 completed).
	if got := completed.Load(); got != n {
		t.Fatalf("after Drain, completed = %d, want %d (Drain did not wait for observe handlers)", got, n)
	}
}

// TestDrainWaitsForEmitAsyncChain asserts Drain also waits for the EmitAsync
// dispatch goroutine AND the ModeObserve goroutine it transitively spawns —
// the exact path workflow.emitEventAsync(EventTaskCompleted) takes into the
// governance bridge. If only the outer EmitAsync goroutine were tracked (and
// not its nested observe goroutine) the counter could hit zero mid-chain and
// Drain would return early.
func TestDrainWaitsForEmitAsyncChain(t *testing.T) {
	b := New()
	const n = 6
	var completed atomic.Int32
	b.Register(Subscriber{
		ID:     "async-observer",
		Events: []EventType{"*"},
		Mode:   ModeObserve,
		Handler: func(ctx context.Context, ev *Event) *HookResponse {
			time.Sleep(20 * time.Millisecond)
			completed.Add(1)
			return nil
		},
	})

	for i := 0; i < n; i++ {
		b.EmitAsync(&Event{Type: EventTaskCompleted})
	}

	if err := b.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if got := completed.Load(); got != n {
		t.Fatalf("after Drain, completed = %d, want %d (EmitAsync->observe chain not fully drained)", got, n)
	}
}

// TestDrainTimeout asserts Drain returns ctx.Err() when a handler outlives the
// deadline, so a wedged observe handler cannot hang teardown forever.
func TestDrainTimeout(t *testing.T) {
	b := New()
	release := make(chan struct{})
	b.Register(Subscriber{
		ID:     "blocker",
		Events: []EventType{"*"},
		Mode:   ModeObserve,
		Handler: func(ctx context.Context, ev *Event) *HookResponse {
			<-release
			return nil
		},
	})
	b.Emit(context.Background(), &Event{Type: EventTaskStarted})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := b.Drain(ctx); err == nil {
		t.Fatal("Drain returned nil on a blocked handler, want context deadline error")
	}

	// Unblock the handler so its goroutine exits cleanly and the internal
	// wait goroutine terminates before the test ends.
	close(release)
	if err := b.Drain(context.Background()); err != nil {
		t.Fatalf("Drain after release: %v", err)
	}
}

// TestDrainNoGoroutinesReturnsImmediately asserts Drain is a fast no-op when
// nothing is in flight — the common case on a run with no observe subscribers.
func TestDrainNoGoroutinesReturnsImmediately(t *testing.T) {
	b := New()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := b.Drain(ctx); err != nil {
		t.Fatalf("Drain on idle bus: %v", err)
	}
}
