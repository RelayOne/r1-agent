package main

import (
	"testing"
	"time"
)

// newTestBackoff builds a codexErrorBackoff whose clock is fully
// controllable via the returned *time.Time pointer. Tests mutate
// *clock to advance simulated time without real sleeps.
func newTestBackoff() (*codexErrorBackoff, *time.Time) {
	now := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	clock := &now
	b := newCodexErrorBackoff()
	b.now = func() time.Time { return *clock }
	return b, clock
}

// TestEmptyTrackerNoDelay — fresh tracker returns base delay (0),
// multiplier 1, no errors in window.
func TestEmptyTrackerNoDelay(t *testing.T) {
	b, _ := newTestBackoff()
	if got := b.Multiplier(); got != 1 {
		t.Fatalf("empty multiplier = %d, want 1", got)
	}
	if got := b.NextDelay(0); got != 0 {
		t.Fatalf("empty NextDelay(0) = %v, want 0", got)
	}
	if got := b.NextDelay(500 * time.Millisecond); got != 500*time.Millisecond {
		t.Fatalf("empty NextDelay(500ms) = %v, want 500ms", got)
	}
	if got := b.ErrorCount(); got != 0 {
		t.Fatalf("empty ErrorCount = %d, want 0", got)
	}
}

// TestBelowThresholdStaysAtOne — 4 errors within the window must
// NOT activate backoff. Threshold is 5.
func TestBelowThresholdStaysAtOne(t *testing.T) {
	b, _ := newTestBackoff()
	for i := 0; i < 4; i++ {
		tripped := b.RecordError()
		if tripped {
			t.Fatalf("error %d unexpectedly tripped backoff", i+1)
		}
	}
	if got := b.Multiplier(); got != 1 {
		t.Fatalf("multiplier after 4 errors = %d, want 1", got)
	}
	if got := b.NextDelay(0); got != 0 {
		t.Fatalf("NextDelay after 4 errors = %v, want 0", got)
	}
	if got := b.ErrorCount(); got != 4 {
		t.Fatalf("ErrorCount = %d, want 4", got)
	}
}

// TestThresholdCrossedTripsToTwoX — 5 errors crosses the threshold;
// next call should see multiplier 2.
func TestThresholdCrossedTripsToTwoX(t *testing.T) {
	b, _ := newTestBackoff()
	for i := 0; i < 4; i++ {
		b.RecordError()
	}
	tripped := b.RecordError() // 5th error
	if !tripped {
		t.Fatalf("5th error did not trip backoff")
	}
	if got := b.Multiplier(); got != 2 {
		t.Fatalf("multiplier after 5 errors = %d, want 2", got)
	}
	// NextDelay(0) uses the 30-s floor when backoff is active.
	if got := b.NextDelay(0); got != 60*time.Second {
		t.Fatalf("NextDelay(0) at 2x = %v, want 60s", got)
	}
	if got := b.NextDelay(500 * time.Millisecond); got != time.Second {
		t.Fatalf("NextDelay(500ms) at 2x = %v, want 1s", got)
	}
}

// TestConsecutiveTripEscalates — once backoff is active, the NEXT
// error escalates 2x → 4x. A second consecutive escalation goes
// 4x → 8x and then caps.
func TestConsecutiveTripEscalates(t *testing.T) {
	b, _ := newTestBackoff()
	for i := 0; i < 5; i++ {
		b.RecordError()
	}
	if got := b.Multiplier(); got != 2 {
		t.Fatalf("multiplier after 5 = %d, want 2", got)
	}
	// Consecutive error (without a success in between) → 4x.
	b.RecordError()
	if got := b.Multiplier(); got != 4 {
		t.Fatalf("multiplier after consecutive trip = %d, want 4", got)
	}
	// Another consecutive trip → 8x.
	b.RecordError()
	if got := b.Multiplier(); got != 8 {
		t.Fatalf("multiplier after 2nd consecutive trip = %d, want 8", got)
	}
	// Further consecutive trips must NOT exceed the cap.
	b.RecordError()
	if got := b.Multiplier(); got != 8 {
		t.Fatalf("multiplier cap breached: got %d, want 8", got)
	}
	b.RecordError()
	if got := b.Multiplier(); got != 8 {
		t.Fatalf("multiplier cap breached on 2nd overflow: got %d, want 8", got)
	}
}

// TestSuccessResetsToOne — RecordSuccess brings multiplier back
// to 1 and clears the "last trip" flag so the NEXT threshold
// crossing starts over at 2x rather than continuing to escalate.
func TestSuccessResetsToOne(t *testing.T) {
	b, _ := newTestBackoff()
	for i := 0; i < 7; i++ {
		b.RecordError()
	}
	// Should be at 4x (5 errors → 2x, 6th → 4x, 7th → 8x).
	if got := b.Multiplier(); got != 8 {
		t.Fatalf("pre-reset multiplier = %d, want 8", got)
	}
	b.RecordSuccess()
	if got := b.Multiplier(); got != 1 {
		t.Fatalf("post-success multiplier = %d, want 1", got)
	}
	if got := b.NextDelay(0); got != 0 {
		t.Fatalf("post-success NextDelay = %v, want 0", got)
	}
	// NOTE: RecordSuccess does NOT clear the error-time window;
	// the errors are still in the 5-min window. But because the
	// lastCallTrippedBackoff flag is cleared, the NEXT error that
	// crosses the threshold will register as a fresh trip (2x,
	// not continuing the escalation).
	//
	// We already have 7 errors in the window; the next error
	// makes 8. That's still over threshold (5). Since the prior
	// call (the success) did NOT trip backoff, this is a "first
	// trip" and should jump to 2x — not continue the 8x escalation.
	b.RecordError()
	if got := b.Multiplier(); got != 2 {
		t.Fatalf("post-reset trip multiplier = %d, want 2 (fresh trip after reset)", got)
	}
}

// TestErrorsOutsideWindowDropOff — an error older than 5 min must
// no longer count. Use the mock clock to place 4 errors in the
// distant past, then advance past the window and record 1 more
// error. The window should contain only 1 error, below threshold.
func TestErrorsOutsideWindowDropOff(t *testing.T) {
	b, clock := newTestBackoff()
	// 4 errors at t=0.
	for i := 0; i < 4; i++ {
		b.RecordError()
	}
	if got := b.ErrorCount(); got != 4 {
		t.Fatalf("ErrorCount after 4 recent = %d, want 4", got)
	}
	// Advance clock past the 5-min window.
	*clock = clock.Add(6 * time.Minute)
	// Now only fresh errors should count.
	if got := b.ErrorCount(); got != 0 {
		t.Fatalf("ErrorCount after advancing past window = %d, want 0", got)
	}
	// Record 4 more — still below threshold.
	for i := 0; i < 4; i++ {
		tripped := b.RecordError()
		if tripped {
			t.Fatalf("post-window error %d unexpectedly tripped backoff", i+1)
		}
	}
	if got := b.Multiplier(); got != 1 {
		t.Fatalf("multiplier after 4 post-window errors = %d, want 1", got)
	}
	// A 5th post-window error → trip.
	tripped := b.RecordError()
	if !tripped {
		t.Fatalf("5th post-window error did not trip backoff")
	}
	if got := b.Multiplier(); got != 2 {
		t.Fatalf("multiplier after 5 post-window errors = %d, want 2", got)
	}
}

// TestWindowPartialDropoff — half the errors age out, the rest
// stay. Verifies pruning is incremental, not all-or-nothing.
func TestWindowPartialDropoff(t *testing.T) {
	b, clock := newTestBackoff()
	// 3 errors at t=0.
	for i := 0; i < 3; i++ {
		b.RecordError()
	}
	// Advance 4 min (still within window).
	*clock = clock.Add(4 * time.Minute)
	// 2 more errors — 5 total in the window → trip.
	b.RecordError()
	tripped := b.RecordError()
	if !tripped {
		t.Fatalf("5th in-window error did not trip")
	}
	if got := b.ErrorCount(); got != 5 {
		t.Fatalf("ErrorCount pre-prune = %d, want 5", got)
	}
	// Advance another 2 min — the first 3 errors fall off.
	*clock = clock.Add(2 * time.Minute)
	if got := b.ErrorCount(); got != 2 {
		t.Fatalf("ErrorCount post-partial-prune = %d, want 2", got)
	}
}
