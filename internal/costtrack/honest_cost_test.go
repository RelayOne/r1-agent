package costtrack

import "testing"

func TestBuildHonestCostReport(t *testing.T) {
	tracker := NewTracker(0, nil)
	tracker.Record("claude-sonnet-4", "task-1", 1000, 500, 0, 0)
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
}
