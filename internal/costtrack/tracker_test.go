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
