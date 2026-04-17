package main

import (
	"strings"
	"testing"
)

// TestStep8RegressionTracker_CleanPassResets verifies a clean
// compliance pass zeros the counter and doesn't trip the cap.
func TestStep8RegressionTracker_CleanPassResets(t *testing.T) {
	tr := &step8RegressionTracker{cap: 2}

	// Round 1: compliance not clean.
	if tr.Observe(false, []string{"MISSING: auth"}) {
		t.Fatalf("cycle 1 should not trip cap=2")
	}
	if tr.Cycles() != 1 {
		t.Fatalf("cycles=1 expected, got %d", tr.Cycles())
	}

	// Round 2: compliance clean — counter must reset.
	if tr.Observe(true, nil) {
		t.Fatalf("clean pass must never trip cap")
	}
	if tr.Cycles() != 0 {
		t.Fatalf("cycles must reset to 0 after clean, got %d", tr.Cycles())
	}
	if len(tr.LastGaps()) != 0 {
		t.Fatalf("lastGaps must reset on clean, got %v", tr.LastGaps())
	}
}

// TestStep8RegressionTracker_TripsAtCap verifies the cap-th
// consecutive failure trips the guard and that the first (cap-1)
// failures do not.
func TestStep8RegressionTracker_TripsAtCap(t *testing.T) {
	tr := &step8RegressionTracker{cap: 2}

	if tr.Observe(false, []string{"gap1"}) {
		t.Fatalf("first failure must not trip cap=2")
	}
	if got := tr.Observe(false, []string{"gap1", "gap2"}); !got {
		t.Fatalf("second consecutive failure must trip cap=2")
	}
	if tr.Cycles() != 2 {
		t.Fatalf("expected cycles=2 after trip, got %d", tr.Cycles())
	}
	if gaps := tr.LastGaps(); len(gaps) != 2 || gaps[0] != "gap1" {
		t.Fatalf("lastGaps not captured correctly: %v", gaps)
	}
}

// TestStep8RegressionTracker_CapDefaultMatchesConst ensures the
// package-level const wired by simpleLoopCmd matches the intended
// default (N=2). If someone changes the const, they must update this
// test and the banner wording.
func TestStep8RegressionTracker_CapDefaultMatchesConst(t *testing.T) {
	if step8RegressionCap != 2 {
		t.Fatalf("default cap changed from 2 to %d — update banner wording and docs",
			step8RegressionCap)
	}
}

// TestStep8RegressionTracker_ResetAllowsAdditionalCycles verifies
// that after a clean reset, the tracker tolerates cap-1 more
// failures without tripping — i.e. the counter is truly consecutive,
// not cumulative.
func TestStep8RegressionTracker_ResetAllowsAdditionalCycles(t *testing.T) {
	tr := &step8RegressionTracker{cap: 2}

	tr.Observe(false, []string{"a"}) // cycles=1
	tr.Observe(true, nil)            // reset to 0
	if tr.Observe(false, []string{"b"}) {
		t.Fatalf("post-reset first failure must not trip cap")
	}
	if tr.Cycles() != 1 {
		t.Fatalf("expected cycles=1 after reset+1fail, got %d", tr.Cycles())
	}
}

// TestFormatGapList covers the gap-list pretty-printer used in the
// banner and final summary.
func TestFormatGapList(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want string
	}{
		{"empty", nil, "(none recorded)"},
		{"one", []string{"MISSING: auth"}, "MISSING: auth"},
		{"few", []string{"a", "b", "c"}, "a; b; c"},
		{"exactly-five", []string{"1", "2", "3", "4", "5"}, "1; 2; 3; 4; 5"},
		{"overflow", []string{"1", "2", "3", "4", "5", "6", "7"}, "1; 2; 3; 4; 5 (+2 more)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := formatGapList(c.in)
			if got != c.want {
				t.Fatalf("formatGapList(%v)=%q want %q", c.in, got, c.want)
			}
			// Sanity: every non-empty input must include the first gap.
			if len(c.in) > 0 && !strings.Contains(got, c.in[0]) {
				t.Fatalf("first gap missing from output: %q", got)
			}
		})
	}
}
