package ctxpack

import (
	"testing"
)

func TestDefaultEngineDefaults(t *testing.T) {
	e := NewDefaultEngine()
	if e.PrimaryPct != 50 || e.SafetyPct != 85 {
		t.Fatalf("bad defaults: primary=%d safety=%d", e.PrimaryPct, e.SafetyPct)
	}
	if e.Compactor == nil {
		t.Fatal("expected default compactor")
	}
}

func TestShouldCompactInLoop(t *testing.T) {
	e := NewDefaultEngine()
	// 40% — below primary, no fire
	ok, _ := e.ShouldCompact(40, 100, "in_loop")
	if ok {
		t.Fatal("should not fire below 50% in_loop")
	}
	// 50% — hits primary, fire
	ok, label := e.ShouldCompact(50, 100, "in_loop")
	if !ok || label != "in_loop_primary" {
		t.Fatalf("expected in_loop_primary; got ok=%v label=%q", ok, label)
	}
	// 90% — in_loop only fires on primary, not safety
	ok, label = e.ShouldCompact(90, 100, "in_loop")
	if !ok {
		t.Fatal("90% must still fire in_loop (>= primary)")
	}
	if label != "in_loop_primary" {
		t.Fatalf("in_loop phase must not report safety label; got %q", label)
	}
}

func TestShouldCompactBetweenTurn(t *testing.T) {
	e := NewDefaultEngine()
	// 50% — below safety-net, no fire (safety net is 85%)
	ok, _ := e.ShouldCompact(50, 100, "between_turn")
	if ok {
		t.Fatal("50% below 85% safety must not fire between_turn")
	}
	// 85% — hits safety
	ok, label := e.ShouldCompact(85, 100, "between_turn")
	if !ok || label != "between_turn_safety" {
		t.Fatalf("expected between_turn_safety; got ok=%v label=%q", ok, label)
	}
}

func TestShouldCompactUnknownPhaseFiresOnEither(t *testing.T) {
	e := NewDefaultEngine()
	// Unknown phase + 55% → fires on primary
	ok, label := e.ShouldCompact(55, 100, "mystery")
	if !ok {
		t.Fatal("unknown phase at 55% should fire")
	}
	if label != "unknown_phase_primary" {
		t.Fatalf("label should flag unknown phase primary; got %q", label)
	}
}

func TestShouldCompactDefensiveInputs(t *testing.T) {
	e := NewDefaultEngine()
	// Zero budget, zero used, nil engine: never fire.
	cases := []struct {
		used, budget int
		phase        string
	}{
		{0, 100, "in_loop"},
		{50, 0, "in_loop"},
		{-5, 100, "in_loop"},
	}
	for _, c := range cases {
		if ok, _ := e.ShouldCompact(c.used, c.budget, c.phase); ok {
			t.Fatalf("defensive case {used=%d budget=%d} unexpectedly fired", c.used, c.budget)
		}
	}
	var nilE *DefaultEngine
	if ok, _ := nilE.ShouldCompact(50, 100, "in_loop"); ok {
		t.Fatal("nil engine must not fire")
	}
}

func TestNewEngineWithThresholdsInvertedFallsBack(t *testing.T) {
	// Primary=80, Safety=50 is nonsense (safety should be >= primary).
	e := NewEngineWithThresholds(80, 50)
	if e.PrimaryPct != 80 {
		t.Fatalf("primary should be accepted: %d", e.PrimaryPct)
	}
	if e.SafetyPct != 85 {
		t.Fatalf("inverted safety should fall back to 85; got %d", e.SafetyPct)
	}
}

func TestCompactEmptySectionsReturnsEmptyResult(t *testing.T) {
	e := NewDefaultEngine()
	r := e.Compact(nil)
	if len(r.Sections) != 0 {
		t.Fatalf("expected empty output on nil input; got %+v", r)
	}
}
