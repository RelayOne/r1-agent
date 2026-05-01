package main

import "testing"

// TestGapCountProgressTracker_NoPlateauBelowWindow — fewer than
// `window+1` observations cannot plateau; tracker should return false.
func TestGapCountProgressTracker_NoPlateauBelowWindow(t *testing.T) {
	g := &gapCountProgressTracker{window: 3, minDelta: 1}
	if g.Observe(100) {
		t.Error("first observation should not plateau")
	}
	if g.Observe(80) {
		t.Error("second observation (still under window) should not plateau")
	}
	if g.Observe(60) {
		t.Error("third observation (still under window) should not plateau")
	}
}

// TestGapCountProgressTracker_ClearProgressNoPlateau — a healthy run
// where each round reduces gap count by >= minDelta should never
// trigger plateau even after many observations.
func TestGapCountProgressTracker_ClearProgressNoPlateau(t *testing.T) {
	g := &gapCountProgressTracker{window: 3, minDelta: 1}
	for _, n := range []int{100, 90, 80, 70, 60, 50} {
		if g.Observe(n) {
			t.Errorf("monotonic progress should not plateau; tripped at %d", n)
		}
	}
}

// TestGapCountProgressTracker_FlatPlateauTriggers — 3 rounds with
// identical gap count must trigger plateau on the 4th observation.
func TestGapCountProgressTracker_FlatPlateauTriggers(t *testing.T) {
	g := &gapCountProgressTracker{window: 3, minDelta: 1}
	_ = g.Observe(50) // first, no plateau check possible
	_ = g.Observe(50)
	_ = g.Observe(50)
	// 4th observation: window+1=4 entries; check last 4 deltas.
	if !g.Observe(50) {
		t.Error("4 identical gap counts must trigger plateau")
	}
}

// TestGapCountProgressTracker_IncreasingPlateauTriggers — gap count
// going backward (gaps growing) is the worst shape; plateau check
// must fire.
func TestGapCountProgressTracker_IncreasingPlateauTriggers(t *testing.T) {
	g := &gapCountProgressTracker{window: 3, minDelta: 1}
	_ = g.Observe(36)
	_ = g.Observe(42)
	_ = g.Observe(48)
	if !g.Observe(54) {
		t.Error("growing gap count must trigger plateau")
	}
}

// TestGapCountProgressTracker_RecentProgressResetsPlateau — even
// after stalling for a few rounds, a single successful drop within
// the tail window should reset the plateau detector.
func TestGapCountProgressTracker_RecentProgressResetsPlateau(t *testing.T) {
	g := &gapCountProgressTracker{window: 3, minDelta: 1}
	_ = g.Observe(50)
	_ = g.Observe(50)
	_ = g.Observe(50)
	// One good round drops the gap by 2 — still inside the tail window.
	if g.Observe(48) {
		t.Error("recent progress within tail window should not plateau")
	}
}

// TestGapCountProgressTracker_ZeroGapsResets — a clean round
// (gapCount=0) means the loop succeeded; the tracker should wipe
// history so a subsequent non-zero count starts fresh (defensive —
// production always exits before reaching this path after 0 gaps).
func TestGapCountProgressTracker_ZeroGapsResets(t *testing.T) {
	g := &gapCountProgressTracker{window: 3, minDelta: 1}
	_ = g.Observe(50)
	_ = g.Observe(50)
	if triggered := g.Observe(0); triggered {
		t.Error("gapCount=0 must not trigger plateau")
	}
	if len(g.History()) != 0 {
		t.Errorf("history should reset on clean round; got %v", g.History())
	}
}

// TestGapCountProgressTracker_Best — Best() returns the lowest count
// seen, used in the PARTIAL-SUCCESS banner to show progress high-
// water mark.
func TestGapCountProgressTracker_Best(t *testing.T) {
	g := &gapCountProgressTracker{window: 3, minDelta: 1}
	_ = g.Observe(100)
	_ = g.Observe(80)
	_ = g.Observe(60) // best
	_ = g.Observe(70)
	if got := g.Best(); got != 60 {
		t.Errorf("Best = %d, want 60", got)
	}
}

// TestGapCountProgressTracker_MinDeltaTunable — a tracker tuned to
// require a larger drop treats small drops as plateau. Guards
// against callers that want to require meaningful progress.
func TestGapCountProgressTracker_MinDeltaTunable(t *testing.T) {
	// Require a drop of at least 5 per round.
	g := &gapCountProgressTracker{window: 3, minDelta: 5}
	_ = g.Observe(100)
	_ = g.Observe(98) // only -2 → plateau contribution
	_ = g.Observe(97) // only -1 → plateau contribution
	_ = g.Observe(96) // only -1 → plateau triggers
	if !g.Observe(95) {
		// On the 5th observation the tail-of-4 has deltas 2,1,1,1 — all
		// < minDelta=5 → plateau. (Observe returns true on the tail
		// that covers window+1 entries; here that's rounds 2..5.)
		t.Error("sub-threshold drops must accumulate to plateau when minDelta is strict")
	}
}
