package costtrack

import (
	"strings"
	"testing"
)

func TestComputeCost(t *testing.T) {
	// Claude Opus: $15/M input, $75/M output
	cost := ComputeCost("claude-opus-4", 1000, 500, 0, 0)
	expected := 1000*15.0/1_000_000 + 500*75.0/1_000_000
	if cost < expected*0.99 || cost > expected*1.01 {
		t.Errorf("expected ~%f, got %f", expected, cost)
	}
}

func TestComputeCostUnknownModel(t *testing.T) {
	cost := ComputeCost("unknown-model", 1000, 500, 0, 0)
	if cost <= 0 {
		t.Error("unknown model should use default pricing")
	}
}

func TestNormalizeModel(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		matched bool
	}{
		// Exact keys pass through.
		{"claude-opus-4", "claude-opus-4", true},
		{"claude-sonnet-4", "claude-sonnet-4", true},
		{"codex-mini", "codex-mini", true},
		// Dated IDs must resolve to the correct tier, not silently default.
		{"claude-opus-4-20250514", "claude-opus-4", true},
		{"claude-sonnet-4-20250514", "claude-sonnet-4", true},
		{"claude-3-5-haiku-20241022", "claude-haiku-3.5", true},
		{"us.anthropic.claude-opus-4-20250514-v1:0", "claude-opus-4", true},
		// Provider prefixes / casing.
		{"anthropic/claude-opus-4", "claude-opus-4", true},
		{"CLAUDE-SONNET-4", "claude-sonnet-4", true},
		{"gpt-4o-2024-08-06", "gpt-4o", true},
		{"o3-mini-2025-01-31", "o3-mini", true},
		{"codex", "codex-mini", true},
		// Bare runner labels carry no tier -> no confident match.
		{"claude", "", false},
		{"native", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, matched := NormalizeModel(c.in)
		if matched != c.matched || got != c.want {
			t.Errorf("NormalizeModel(%q) = (%q,%v), want (%q,%v)", c.in, got, matched, c.want, c.matched)
		}
	}
}

// TestComputeCostDatedOpusNotUnderpriced proves the core budget-integrity fix:
// a dated Opus ID must be priced as Opus, not silently ~5x-under as Sonnet.
func TestComputeCostDatedOpusNotUnderpriced(t *testing.T) {
	opus := ComputeCost("claude-opus-4", 100_000, 20_000, 0, 0)
	datedOpus := ComputeCost("claude-opus-4-20250514", 100_000, 20_000, 0, 0)
	sonnet := ComputeCost("claude-sonnet-4", 100_000, 20_000, 0, 0)

	if datedOpus != opus {
		t.Errorf("dated Opus mispriced: got %f, want Opus %f", datedOpus, opus)
	}
	if datedOpus <= sonnet {
		t.Errorf("dated Opus (%f) must cost more than Sonnet (%f); it was being priced as Sonnet", datedOpus, sonnet)
	}
}

// TestComputeCostUnknownSignals proves the fallback is observable, not silent.
func TestComputeCostUnknownSignals(t *testing.T) {
	before := UnknownModelHits()
	var seen string
	OnUnknownModel = func(m string) { seen = m }
	defer func() { OnUnknownModel = nil }()

	_ = ComputeCost("claude", 1000, 500, 0, 0) // bare runner name, unresolved
	if UnknownModelHits() != before+1 {
		t.Errorf("unknown-model fallback did not increment counter: before=%d after=%d", before, UnknownModelHits())
	}
	if seen != "claude" {
		t.Errorf("OnUnknownModel got %q, want \"claude\"", seen)
	}

	// A resolvable name must NOT signal.
	beforeResolvable := UnknownModelHits()
	_ = ComputeCost("claude-opus-4-20250514", 1000, 500, 0, 0)
	if UnknownModelHits() != beforeResolvable {
		t.Errorf("resolvable dated ID wrongly counted as unknown")
	}
}

func TestTrackerRecord(t *testing.T) {
	tr := NewTracker(0, nil)

	cost := tr.Record("claude-sonnet-4", "task-1", 5000, 1000, 0, 0)
	if cost <= 0 {
		t.Error("cost should be positive")
	}

	if tr.Total() != cost {
		t.Errorf("total should equal recorded cost")
	}
	if tr.RequestCount() != 1 {
		t.Errorf("expected 1 request, got %d", tr.RequestCount())
	}
}

func TestTrackerByModel(t *testing.T) {
	tr := NewTracker(0, nil)
	tr.Record("claude-opus-4", "", 1000, 100, 0, 0)
	tr.Record("claude-sonnet-4", "", 1000, 100, 0, 0)
	tr.Record("claude-opus-4", "", 1000, 100, 0, 0)

	byModel := tr.ByModel()
	if len(byModel) != 2 {
		t.Errorf("expected 2 models, got %d", len(byModel))
	}
	if byModel["claude-opus-4"] <= 0 {
		t.Error("opus cost should be positive")
	}
}

func TestTrackerByTask(t *testing.T) {
	tr := NewTracker(0, nil)
	tr.Record("claude-sonnet-4", "t1", 1000, 100, 0, 0)
	tr.Record("claude-sonnet-4", "t2", 1000, 100, 0, 0)
	tr.Record("claude-sonnet-4", "t1", 2000, 200, 0, 0)

	byTask := tr.ByTask()
	if len(byTask) != 2 {
		t.Errorf("expected 2 tasks, got %d", len(byTask))
	}
}

func TestTrackerTokenTotals(t *testing.T) {
	tr := NewTracker(0, nil)
	tr.Record("claude-sonnet-4", "", 1000, 500, 200, 100)
	tr.Record("claude-sonnet-4", "", 2000, 1000, 300, 0)

	input, output, cacheR, cacheW := tr.TokenTotals()
	if input != 3000 || output != 1500 || cacheR != 500 || cacheW != 100 {
		t.Errorf("unexpected totals: %d/%d/%d/%d", input, output, cacheR, cacheW)
	}
}

func TestBudgetEnforcement(t *testing.T) {
	tr := NewTracker(0.01, nil) // $0.01 budget

	// Record enough to exceed budget
	tr.Record("claude-opus-4", "", 100000, 50000, 0, 0)

	if !tr.OverBudget() {
		t.Error("should be over budget")
	}
	if tr.BudgetRemaining() >= 0 {
		t.Error("remaining should be negative")
	}
}

func TestUnlimitedBudget(t *testing.T) {
	tr := NewTracker(0, nil)
	if tr.OverBudget() {
		t.Error("unlimited budget should never be over")
	}
	if tr.BudgetRemaining() != -1 {
		t.Errorf("expected -1 for unlimited, got %f", tr.BudgetRemaining())
	}
}

func TestBudgetAlerts(t *testing.T) {
	var alerts []Alert
	alertFn := func(a Alert) {
		alerts = append(alerts, a)
	}

	tr := NewTracker(1.0, alertFn) // $1 budget

	// 50% alert
	tr.Record("claude-opus-4", "", 20000, 5000, 0, 0)
	// This alone is about $0.015 + $0.375 = $0.39

	// Push past 80%
	tr.Record("claude-opus-4", "", 20000, 5000, 0, 0)

	// Push past 100%
	tr.Record("claude-opus-4", "", 20000, 10000, 0, 0)

	if len(alerts) == 0 {
		t.Error("should have triggered at least one alert")
	}
}

func TestSummary(t *testing.T) {
	tr := NewTracker(10.0, nil)
	tr.Record("claude-sonnet-4", "", 5000, 1000, 500, 0)

	s := tr.Summary()
	if !strings.Contains(s, "Cost:") {
		t.Error("summary should contain cost")
	}
	if !strings.Contains(s, "Budget:") {
		t.Error("summary should contain budget when set")
	}
}

func TestSummaryNoBudget(t *testing.T) {
	tr := NewTracker(0, nil)
	tr.Record("claude-sonnet-4", "", 1000, 100, 0, 0)

	s := tr.Summary()
	if strings.Contains(s, "Budget:") {
		t.Error("should not show budget when unlimited")
	}
}
