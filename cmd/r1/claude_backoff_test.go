package main

import (
	"sync"
	"testing"
	"time"
)

// newTestClaudeDetector builds a claudeRateLimitDetector whose clock
// and sleep are fully controllable. The returned *time.Time is the
// simulated wall clock — mutate it to advance time without real
// sleeps. The returned *[]time.Duration records every value WaitIfPaused
// attempted to sleep for (in call order), so tests can assert on the
// backoff ladder without blocking.
func newTestClaudeDetector() (*claudeRateLimitDetector, *time.Time, *[]time.Duration) {
	now := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	clock := &now
	var slept []time.Duration
	var slepMu sync.Mutex
	d := newClaudeRateLimitDetector()
	d.now = func() time.Time { return *clock }
	d.sleep = func(x time.Duration) {
		slepMu.Lock()
		slept = append(slept, x)
		slepMu.Unlock()
		// advance the mock clock by the sleep duration so the rolling
		// window behaves realistically without real time passing.
		*clock = clock.Add(x)
	}
	return d, clock, &slept
}

// TestClaudeDetector_EmptyNoPause — a fresh detector is Normal,
// WaitIfPaused is a no-op, SignatureCount==0.
func TestClaudeDetector_EmptyNoPause(t *testing.T) {
	d, _, slept := newTestClaudeDetector()
	if d.State() != claudeBackoffNormal {
		t.Fatalf("fresh state = %v, want Normal", d.State())
	}
	if d.SignatureCount() != 0 {
		t.Fatalf("fresh SignatureCount = %d, want 0", d.SignatureCount())
	}
	d.WaitIfPaused()
	if len(*slept) != 0 {
		t.Fatalf("empty detector must not sleep; slept = %v", *slept)
	}
}

// TestClaudeDetector_OneSignatureStaysSuspected — 1 rate-limit
// signature must move us to Suspected, NOT Active. WaitIfPaused
// is still a no-op.
func TestClaudeDetector_OneSignatureStaysSuspected(t *testing.T) {
	d, _, slept := newTestClaudeDetector()
	d.RecordResult("claude error: exit status 1", 1, 0)
	if d.State() != claudeBackoffSuspected {
		t.Fatalf("after 1 signature state = %v, want Suspected", d.State())
	}
	if d.SignatureCount() != 1 {
		t.Fatalf("SignatureCount = %d, want 1", d.SignatureCount())
	}
	d.WaitIfPaused()
	if len(*slept) != 0 {
		t.Fatalf("Suspected must not sleep; slept = %v", *slept)
	}
}

// TestClaudeDetector_TwoSignaturesWithinWindowActivate — 2 signatures
// within the 5-min window must promote us to Active. WaitIfPaused
// then sleeps the first backoff rung (1 min).
func TestClaudeDetector_TwoSignaturesWithinWindowActivate(t *testing.T) {
	d, clock, slept := newTestClaudeDetector()
	d.RecordResult("claude error: exit status 1", 1, 0)
	// Advance 2 min — still inside the window.
	*clock = clock.Add(2 * time.Minute)
	d.RecordResult("claude error: exit status 1", 1, 0)
	if d.State() != claudeBackoffActive {
		t.Fatalf("after 2 signatures state = %v, want Active", d.State())
	}
	if got := d.CurrentBackoff(); got != 1*time.Minute {
		t.Fatalf("first-rung backoff = %v, want 1m", got)
	}
	d.WaitIfPaused()
	if len(*slept) != 1 || (*slept)[0] != 1*time.Minute {
		t.Fatalf("WaitIfPaused slept = %v, want [1m]", *slept)
	}
}

// TestClaudeDetector_TwoSignaturesOutsideWindowDoNotActivate — the
// first signature ages out before the second arrives. Tracker should
// stay in Suspected (count = 1 after pruning), NOT Active.
func TestClaudeDetector_TwoSignaturesOutsideWindowDoNotActivate(t *testing.T) {
	d, clock, slept := newTestClaudeDetector()
	d.RecordResult("claude error: exit status 1", 1, 0)
	// Advance 6 min — the first signature ages out.
	*clock = clock.Add(6 * time.Minute)
	d.RecordResult("claude error: exit status 1", 1, 0)
	if d.State() == claudeBackoffActive {
		t.Fatalf("old-signature + new-signature must NOT activate; state = %v", d.State())
	}
	if d.SignatureCount() != 1 {
		t.Fatalf("SignatureCount after prune = %d, want 1", d.SignatureCount())
	}
	d.WaitIfPaused()
	if len(*slept) != 0 {
		t.Fatalf("non-Active must not sleep; slept = %v", *slept)
	}
}

// TestClaudeDetector_BackoffLadderEscalates — repeated rate-limit
// results while Active walk the ladder 1 → 2 → 4 → 8 → 15 → 30 min
// and cap at 30 min.
func TestClaudeDetector_BackoffLadderEscalates(t *testing.T) {
	d, clock, _ := newTestClaudeDetector()
	// Enter Active with 2 signatures inside the window.
	d.RecordResult("claude error: exit status 1", 1, 0)
	*clock = clock.Add(1 * time.Minute)
	d.RecordResult("claude error: exit status 1", 1, 0)

	want := []time.Duration{
		1 * time.Minute,
		2 * time.Minute,
		4 * time.Minute,
		8 * time.Minute,
		15 * time.Minute,
		30 * time.Minute,
		30 * time.Minute, // cap sustained
		30 * time.Minute, // cap sustained
	}
	for i, expected := range want {
		got := d.CurrentBackoff()
		if got != expected {
			t.Fatalf("step %d backoff = %v, want %v", i, got, expected)
		}
		// Simulate the retry: another rate-limit result → escalate.
		d.RecordResult("claude error: exit status 1", 1, 0)
	}
}

// TestClaudeDetector_SuccessResetsToNormal — once Active, a successful
// call (turns > 1 OR cost > 0) must reset us to Normal, clear the
// signature window, and zero the backoff step.
func TestClaudeDetector_SuccessResetsToNormal(t *testing.T) {
	d, clock, _ := newTestClaudeDetector()
	d.RecordResult("claude error: exit status 1", 1, 0)
	*clock = clock.Add(1 * time.Minute)
	d.RecordResult("claude error: exit status 1", 1, 0)
	if d.State() != claudeBackoffActive {
		t.Fatalf("state = %v, want Active", d.State())
	}
	// Successful call: turns > 1.
	d.RecordResult("built the thing", 42, 0.0)
	if d.State() != claudeBackoffNormal {
		t.Fatalf("post-success state = %v, want Normal", d.State())
	}
	if d.SignatureCount() != 0 {
		t.Fatalf("post-success SignatureCount = %d, want 0 (window cleared)", d.SignatureCount())
	}
	if d.CurrentBackoff() != 0 {
		t.Fatalf("post-success backoff = %v, want 0", d.CurrentBackoff())
	}
}

// TestClaudeDetector_SuccessByCostOnly — turns==1 but cost>0 still
// counts as success. (A one-turn response that actually made a
// priced API call is NOT a rate-limit signature.)
func TestClaudeDetector_SuccessByCostOnly(t *testing.T) {
	d, clock, _ := newTestClaudeDetector()
	d.RecordResult("claude error: exit status 1", 1, 0)
	*clock = clock.Add(1 * time.Minute)
	d.RecordResult("claude error: exit status 1", 1, 0)
	if d.State() != claudeBackoffActive {
		t.Fatalf("state pre-success = %v, want Active", d.State())
	}
	// Cost > 0 is enough to count as success even if turns == 1.
	d.RecordResult("some output", 1, 0.01)
	if d.State() != claudeBackoffNormal {
		t.Fatalf("state post-success-by-cost = %v, want Normal", d.State())
	}
}

// TestClaudeDetector_AmbiguousOutputDoesNothing — an empty output
// with no "claude error" marker should not escalate or reset. Useful
// because some claude CLI paths return empty bodies without an error
// code (e.g. watchdog kill where the cmd was signalled).
func TestClaudeDetector_AmbiguousOutputDoesNothing(t *testing.T) {
	d, _, _ := newTestClaudeDetector()
	// Put us in Suspected first so we have a non-default state to
	// observe.
	d.RecordResult("claude error: exit status 1", 1, 0)
	if d.State() != claudeBackoffSuspected {
		t.Fatalf("precondition failed: state = %v, want Suspected", d.State())
	}
	// Ambiguous: empty output, no error marker, no success markers.
	d.RecordResult("", 0, 0)
	if d.State() != claudeBackoffSuspected {
		t.Fatalf("ambiguous result changed state: %v, want still Suspected", d.State())
	}
	if d.SignatureCount() != 1 {
		t.Fatalf("ambiguous result touched window: %d, want 1", d.SignatureCount())
	}
}

// TestClaudeDetector_RateLimitSignatureClassification — three-prong
// check. Only outputs with all three markers count:
//  1. contains "claude error"
//  2. turns <= 1
//  3. cost == 0
func TestClaudeDetector_RateLimitSignatureClassification(t *testing.T) {
	cases := []struct {
		name     string
		output   string
		turns    int
		cost     float64
		wantSigs int
	}{
		{"textbook", "claude error: exit status 1", 1, 0, 1},
		{"error_but_turns_high", "claude error: exit status 1", 5, 0, 0},
		{"error_but_cost_nonzero", "claude error: exit status 1", 1, 0.001, 0},
		{"no_marker", "some normal short output", 1, 0, 0},
		{"zero_turns_zero_cost_with_marker", "claude error: exit status 1", 0, 0, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, _, _ := newTestClaudeDetector()
			d.RecordResult(c.output, c.turns, c.cost)
			// For success cases (high turns or cost), SignatureCount is
			// 0 because success clears the window. For not-signature,
			// no-op cases, it's also 0. This covers both paths.
			if got := d.SignatureCount(); got != c.wantSigs {
				t.Fatalf("SignatureCount = %d, want %d", got, c.wantSigs)
			}
		})
	}
}

// TestClaudeDetector_WaitIfPausedNormalNoop — WaitIfPaused must never
// sleep while in Normal state, even after recording some successes.
func TestClaudeDetector_WaitIfPausedNormalNoop(t *testing.T) {
	d, _, slept := newTestClaudeDetector()
	for i := 0; i < 5; i++ {
		d.RecordResult("built stuff", 10, 0.05)
		d.WaitIfPaused()
	}
	if len(*slept) != 0 {
		t.Fatalf("Normal state must never sleep; slept = %v", *slept)
	}
}

// TestClaudeDetector_RateLimitThenSuccessCycle — end-to-end: trip,
// sleep, then a success arrives. Next cycle starts fresh (new 2
// signatures needed to re-activate).
func TestClaudeDetector_RateLimitThenSuccessCycle(t *testing.T) {
	d, clock, slept := newTestClaudeDetector()
	// Trip into Active.
	d.RecordResult("claude error: exit status 1", 1, 0)
	*clock = clock.Add(30 * time.Second)
	d.RecordResult("claude error: exit status 1", 1, 0)
	d.WaitIfPaused() // sleeps 1 min
	if len(*slept) != 1 {
		t.Fatalf("expected 1 sleep after first Active, got %d", len(*slept))
	}
	// Retry succeeds.
	d.RecordResult("finished the build", 20, 0.10)
	if d.State() != claudeBackoffNormal {
		t.Fatalf("post-success state = %v, want Normal", d.State())
	}
	// Second cycle: one rate-limit → Suspected only (not Active).
	d.RecordResult("claude error: exit status 1", 1, 0)
	if d.State() != claudeBackoffSuspected {
		t.Fatalf("single signature after reset moved us to %v, want Suspected", d.State())
	}
	d.WaitIfPaused() // still no sleep in Suspected
	if len(*slept) != 1 {
		t.Fatalf("Suspected must not add a sleep; total sleeps = %d, want 1", len(*slept))
	}
}
