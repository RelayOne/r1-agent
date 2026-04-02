package report

import (
	"testing"
	"time"
)

func TestSaveAndLoadLatest(t *testing.T) {
	dir := t.TempDir()
	r := &BuildReport{
		Version:     "0.1.0",
		PlanID:      "test-plan",
		StartedAt:   time.Now().Add(-5 * time.Minute),
		CompletedAt: time.Now(),
		DurationSec: 300,
		TotalCost:   1.23,
		TasksDone:   3,
		TasksFailed: 1,
		TasksTotal:  4,
		Success:     false,
		Tasks: []TaskReport{
			{ID: "T1", Description: "first", Status: "done", CostUSD: 0.50},
			{ID: "T2", Description: "second", Status: "failed", Error: "build failed",
				Failure: &FailureReport{Class: "BuildFailed", Summary: "2 TS errors"}},
		},
	}

	if err := r.SaveLatest(dir); err != nil {
		t.Fatal(err)
	}
	if err := r.Save(dir); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadLatest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PlanID != "test-plan" {
		t.Errorf("plan_id=%q", loaded.PlanID)
	}
	if loaded.TasksDone != 3 {
		t.Errorf("tasks_done=%d", loaded.TasksDone)
	}
	if len(loaded.Tasks) != 2 {
		t.Fatalf("tasks=%d", len(loaded.Tasks))
	}
	if loaded.Tasks[1].Failure == nil {
		t.Error("task 2 should have failure report")
	}
	if loaded.Tasks[1].Failure.Class != "BuildFailed" {
		t.Errorf("class=%q", loaded.Tasks[1].Failure.Class)
	}
}

func TestReportRoundtrip(t *testing.T) {
	dir := t.TempDir()
	r := &BuildReport{
		Version:   "0.1.0",
		PlanID:    "roundtrip",
		StartedAt: time.Now(),
		Tasks: []TaskReport{
			{
				ID:      "T1",
				Status:  "done",
				Review:  &ReviewReport{Engine: "codex", Approved: true, Summary: "LGTM"},
				FilesChanged: []string{"src/auth.ts", "src/types/auth.ts"},
			},
		},
	}
	r.SaveLatest(dir)
	loaded, _ := LoadLatest(dir)
	if loaded.Tasks[0].Review == nil || !loaded.Tasks[0].Review.Approved {
		t.Error("review should roundtrip")
	}
	if len(loaded.Tasks[0].FilesChanged) != 2 {
		t.Errorf("files=%d", len(loaded.Tasks[0].FilesChanged))
	}
}
