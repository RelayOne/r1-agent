package costtrack

import "testing"

func TestBuildHonestCostReport(t *testing.T) {
	tracker := NewTracker(0, nil)
	tracker.Record("claude-sonnet-4", "task-1", 1000, 500, 0, 0)
	tracker.Record("gpt-4o", "task-1", 200, 100, 0, 0)
	tracker.RecordEnvCost("task-1", 2.5)
	report := BuildHonestCostReport(tracker, "task-1", 180)
	if report.TaskID != "task-1" {
		t.Fatalf("task=%q", report.TaskID)
	}
	if report.ByProvider["anthropic"] <= 0 {
		t.Fatalf("anthropic provider missing: %#v", report.ByProvider)
	}
	if report.HumanMinutes <= 0 {
		t.Fatalf("human minutes=%f want > 0", report.HumanMinutes)
	}
	if len(report.ProviderGroups) != 2 {
		t.Fatalf("provider groups = %d, want 2", len(report.ProviderGroups))
	}
	if report.EquivalentMeteredUSD == 0 {
		t.Fatalf("equivalent metered usd = %f, want > 0", report.EquivalentMeteredUSD)
	}
	if report.ProviderGroups[0].Requests == 0 {
		t.Fatalf("provider group requests = %d, want > 0", report.ProviderGroups[0].Requests)
	}
	if report.ProviderGroups[0].EquivalentMarginPct < 0 {
		t.Fatalf("provider group margin pct = %f, want non-negative", report.ProviderGroups[0].EquivalentMarginPct)
	}
}
