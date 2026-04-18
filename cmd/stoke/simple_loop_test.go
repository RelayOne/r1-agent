package main

import (
	"context"
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

// TestStep8RegressionTracker_AuditDidNotRun covers the H-6 fix:
// ObserveAuditResult(auditRan=false, ...) MUST NOT increment the
// counter and MUST NOT trip the cap, no matter what complianceClean
// or gaps say. This is the "claude rate-limited, audit couldn't
// produce a verdict" case observed in H1-sonnet / H2-opus-full.
func TestStep8RegressionTracker_AuditDidNotRun(t *testing.T) {
	tr := &step8RegressionTracker{cap: 2}

	for i := 0; i < 5; i++ {
		if tr.ObserveAuditResult(false, false, nil) {
			t.Fatalf("errored audit %d must not trip cap", i+1)
		}
	}
	if tr.Cycles() != 0 {
		t.Fatalf("errored audits must not bump cycles; got %d", tr.Cycles())
	}
}

// TestStep8RegressionTracker_AuditRanFoundGaps covers the normal
// "real regression" case: auditRan=true + complianceClean=false
// must increment the counter just like the old Observe() did.
func TestStep8RegressionTracker_AuditRanFoundGaps(t *testing.T) {
	tr := &step8RegressionTracker{cap: 3}

	if tr.ObserveAuditResult(true, false, []string{"gap-a"}) {
		t.Fatalf("first real regression must not trip cap=3")
	}
	if tr.Cycles() != 1 {
		t.Fatalf("cycles=1 expected after one real regression, got %d", tr.Cycles())
	}
	if tr.ObserveAuditResult(true, false, []string{"gap-b"}) {
		t.Fatalf("second real regression must not trip cap=3")
	}
	if tr.Cycles() != 2 {
		t.Fatalf("cycles=2 expected, got %d", tr.Cycles())
	}
}

// TestStep8RegressionTracker_AuditRanClean covers the reset path:
// auditRan=true + complianceClean=true must zero the counter.
func TestStep8RegressionTracker_AuditRanClean(t *testing.T) {
	tr := &step8RegressionTracker{cap: 2}

	tr.ObserveAuditResult(true, false, []string{"gap-a"}) // cycles=1
	if tripped := tr.ObserveAuditResult(true, true, nil); tripped {
		t.Fatalf("clean pass must never trip cap")
	}
	if tr.Cycles() != 0 {
		t.Fatalf("clean pass must reset cycles, got %d", tr.Cycles())
	}
	if len(tr.LastGaps()) != 0 {
		t.Fatalf("clean pass must reset lastGaps, got %v", tr.LastGaps())
	}
}

// TestStep8RegressionTracker_ErroredAuditsInterspersedWithRealRegressions
// is the headline scenario from the H-6 bug report: 3 errored audits
// (audit didn't run) followed by 2 real regressions must leave the
// counter at exactly 2 — the errored audits don't pollute the count.
// With cap=3 this means we're still NOT tripped; the old Observe()
// would have tripped after the 3rd errored audit.
func TestStep8RegressionTracker_ErroredAuditsInterspersedWithRealRegressions(t *testing.T) {
	tr := &step8RegressionTracker{cap: 3}

	// 3 errored audits — counter stays at 0.
	for i := 0; i < 3; i++ {
		if tr.ObserveAuditResult(false, false, nil) {
			t.Fatalf("errored audit %d must not trip", i+1)
		}
	}
	if tr.Cycles() != 0 {
		t.Fatalf("cycles must stay 0 after 3 errored audits, got %d", tr.Cycles())
	}
	// 2 real regressions — counter becomes 2, still under cap=3.
	if tr.ObserveAuditResult(true, false, []string{"real-a"}) {
		t.Fatalf("first real regression tripped unexpectedly")
	}
	if tripped := tr.ObserveAuditResult(true, false, []string{"real-b"}); tripped {
		t.Fatalf("second real regression tripped unexpectedly (cap=3)")
	}
	if tr.Cycles() != 2 {
		t.Fatalf("cycles=2 expected (errored audits must NOT contribute), got %d", tr.Cycles())
	}
}

// TestStep8RegressionTracker_ObserveStillWorks makes sure the legacy
// Observe() wrapper still behaves exactly like before — it delegates
// to ObserveAuditResult(true, ...).
func TestStep8RegressionTracker_ObserveStillWorks(t *testing.T) {
	tr := &step8RegressionTracker{cap: 2}

	if tr.Observe(false, []string{"x"}) {
		t.Fatalf("one failure must not trip cap=2")
	}
	if !tr.Observe(false, []string{"y"}) {
		t.Fatalf("two failures must trip cap=2")
	}
}

// TestTierFilterRescue_TierFilterDisabled verifies the rescue helper
// returns (false, false) when the operator disabled the filter
// (--tier-filter-after=0). That's a pass-through: H-6 cap must fire
// normally if the filter isn't active.
func TestTierFilterRescue_TierFilterDisabled(t *testing.T) {
	tracker := &step8RegressionTracker{cap: 2, cycles: 2, lastGaps: []string{"gap-a"}}
	review := func(_ context.Context, _ string) string {
		t.Fatalf("reviewer should NOT be called when tier-filter-after <= 0")
		return ""
	}
	rescued, complete := tierFilterRescueBeforeH6CapWithReview(
		review, []string{"gap-a"}, 0 /* disabled */, 0.7, tracker)
	if rescued || complete {
		t.Fatalf("rescued=%v complete=%v; want (false,false) when filter disabled",
			rescued, complete)
	}
	// Tracker state must be untouched.
	if tracker.cycles != 2 {
		t.Fatalf("tracker cycles mutated: %d (want 2 preserved)", tracker.cycles)
	}
}

// TestTierFilterRescue_DropAllCompleteExits covers the scenario where
// the filter declares every remaining gap to be TIER-3 noise. The
// rescue must return complete=true so the outer loop exits as SIMPLE
// LOOP COMPLETE rather than firing the H-6 abort banner.
func TestTierFilterRescue_DropAllCompleteExits(t *testing.T) {
	tracker := &step8RegressionTracker{cap: 2, cycles: 2, lastGaps: []string{"naming", "naming-2"}}
	reviewBody := `{
  "tier1": [],
  "tier2": [],
  "tier3": ["style preference on naming", "style preference on naming-2"],
  "recurring": true,
  "decision": "drop-tier3-complete",
  "rationale": "all noise"
}`
	review := func(_ context.Context, _ string) string { return reviewBody }
	rescued, complete := tierFilterRescueBeforeH6CapWithReview(
		review,
		[]string{"style preference on naming", "style preference on naming-2"},
		5, 0.7, tracker)
	if !rescued || !complete {
		t.Fatalf("rescued=%v complete=%v; want (true,true) for drop-tier3-complete",
			rescued, complete)
	}
	// Tracker must be reset so a subsequent call doesn't fire the cap.
	if tracker.Cycles() != 0 {
		t.Fatalf("tracker cycles not reset on complete: %d", tracker.Cycles())
	}
}

// TestTierFilterRescue_DropContinueResetsCounter covers the H-20
// headline case from the task spec: the outer H-6 cap was about to
// fire, the filter says drop-tier3-continue leaving TIER-1 gaps. The
// rescue must reset the regression counter (so the loop keeps going)
// and leave the filtered gap list in lastGaps.
func TestTierFilterRescue_DropContinueResetsCounter(t *testing.T) {
	tracker := &step8RegressionTracker{
		cap: 2, cycles: 2,
		lastGaps: []string{
			"MISSING: auth bypass on /admin",
			"missing docstring on foo",
			"missing docstring on bar",
			"missing docstring on baz",
			"missing docstring on qux",
		},
	}
	gaps := []string{
		"MISSING: auth bypass on /admin",
		"missing docstring on foo",
		"missing docstring on bar",
		"missing docstring on baz",
		"missing docstring on qux",
	}
	// Reviewer JSON: 1 TIER-1 (real defect), 4 TIER-3 (noise),
	// dominance share = 4/5 = 80% (above 0.7). Recurrence passes
	// because the synthetic priorRounds from the rescue helper is the
	// current gaps themselves.
	reviewBody := `{
  "tier1": ["MISSING: auth bypass on /admin"],
  "tier2": [],
  "tier3": [
    "missing docstring on foo",
    "missing docstring on bar",
    "missing docstring on baz",
    "missing docstring on qux"
  ],
  "recurring": true,
  "decision": "drop-tier3-continue",
  "rationale": "4/5 are docstring noise"
}`
	review := func(_ context.Context, _ string) string { return reviewBody }
	rescued, complete := tierFilterRescueBeforeH6CapWithReview(
		review, gaps, 5, 0.7, tracker)
	if !rescued || complete {
		t.Fatalf("rescued=%v complete=%v; want (true,false) for drop-tier3-continue",
			rescued, complete)
	}
	if tracker.Cycles() != 0 {
		t.Fatalf("tracker cycles not reset on drop-tier3-continue: %d", tracker.Cycles())
	}
	got := tracker.LastGaps()
	if len(got) != 1 || !strings.Contains(got[0], "auth bypass") {
		t.Fatalf("tracker.LastGaps not filtered to TIER-1 only: %v", got)
	}
}

// TestTierFilterRescue_ContinueDecisionFallsThrough is the H-20
// safety contract: if the filter's decision is "continue" (real
// defects dominate), the rescue MUST return (false, false) so the
// original H-6 termination fires. We must NEVER silently skip a real
// regression just because the filter engaged.
func TestTierFilterRescue_ContinueDecisionFallsThrough(t *testing.T) {
	tracker := &step8RegressionTracker{cap: 2, cycles: 2, lastGaps: []string{"real-defect"}}
	reviewBody := `{
  "tier1": ["real-defect"],
  "tier2": [],
  "tier3": [],
  "recurring": false,
  "decision": "continue",
  "rationale": "no noise"
}`
	review := func(_ context.Context, _ string) string { return reviewBody }
	rescued, complete := tierFilterRescueBeforeH6CapWithReview(
		review, []string{"real-defect"}, 5, 0.7, tracker)
	if rescued || complete {
		t.Fatalf("rescued=%v complete=%v; want (false,false) for decision=continue",
			rescued, complete)
	}
	// Counter MUST be unchanged — we're about to fire the abort.
	if tracker.Cycles() != 2 {
		t.Fatalf("counter mutated on fall-through: %d (want 2)", tracker.Cycles())
	}
}

// TestTierFilterRescue_ReviewerErrorFailsOpen covers the spec's
// "fail-open on H-20" clause: a reviewer that returns empty / errored
// output must NOT rescue, so the H-6 abort runs. Dropping real gaps
// on a filter failure would silently swallow a real regression.
func TestTierFilterRescue_ReviewerErrorFailsOpen(t *testing.T) {
	tracker := &step8RegressionTracker{cap: 2, cycles: 2, lastGaps: []string{"gap-a"}}
	review := func(_ context.Context, _ string) string { return "" } // empty = error
	rescued, complete := tierFilterRescueBeforeH6CapWithReview(
		review, []string{"gap-a"}, 5, 0.7, tracker)
	if rescued || complete {
		t.Fatalf("rescued=%v complete=%v; want (false,false) on reviewer error (fail-open)",
			rescued, complete)
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
