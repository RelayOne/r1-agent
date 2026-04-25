package bench_test

import (
	"context"
	"testing"

	"github.com/RelayOne/r1-agent/bench/cost"
	"github.com/RelayOne/r1-agent/bench/harnesses"
	"github.com/RelayOne/r1-agent/bench/judge"
	"github.com/RelayOne/r1-agent/bench/metrics"
	"github.com/RelayOne/r1-agent/bench/reports"
)

func TestHarnessInterface(t *testing.T) {
	// Verify all harnesses implement the interface
	var _ harnesses.Harness = &harnesses.Stoke{}
	var _ harnesses.Harness = &harnesses.ClaudeCode{}
	var _ harnesses.Harness = &harnesses.Codex{}
	var _ harnesses.Harness = &harnesses.Aider{}
}

func TestHarnessNames(t *testing.T) {
	tests := []struct {
		h    harnesses.Harness
		want string
	}{
		{&harnesses.Stoke{}, "stoke"},
		{&harnesses.ClaudeCode{}, "claude-code"},
		{&harnesses.Codex{}, "codex"},
		{&harnesses.Aider{}, "aider"},
	}
	for _, tt := range tests {
		if got := tt.h.Name(); got != tt.want {
			t.Errorf("%T.Name() = %q, want %q", tt.h, got, tt.want)
		}
	}
}

func TestDeterministicJudge(t *testing.T) {
	j := &judge.DeterministicJudge{}
	task := &judge.Task{ID: "test-1", Category: "basic", Language: "go"}
	v := j.Judge(context.Background(), task, "/nonexistent")
	// With nonexistent workspace, should not panic
	if v.Score < 0 || v.Score > 1 {
		t.Errorf("score out of range: %f", v.Score)
	}
}

func TestHonestyJudge(t *testing.T) {
	j := &judge.HonestyJudge{
		Deterministic: &judge.DeterministicJudge{},
	}
	task := &judge.Task{ID: "test-1", Category: "basic"}
	v := j.Judge(context.Background(), task, "/nonexistent")
	if v.HonestyScore < 0 || v.HonestyScore > 1 {
		t.Errorf("honesty score out of range: %f", v.HonestyScore)
	}
}

func TestCostTracker(t *testing.T) {
	tracker := cost.NewRunTracker()
	tracker.Record(cost.CostEntry{TaskID: "t1", Harness: "stoke", CostUSD: 0.50})
	tracker.Record(cost.CostEntry{TaskID: "t2", Harness: "stoke", CostUSD: 0.30})
	tracker.Record(cost.CostEntry{TaskID: "t1", Harness: "claude-code", CostUSD: 0.80})

	if total := tracker.Total(); total < 1.59 || total > 1.61 {
		t.Errorf("expected total ~1.60, got %f", total)
	}

	perHarness := tracker.PerHarness()
	if stokeCost := perHarness["stoke"]; stokeCost < 0.79 || stokeCost > 0.81 {
		t.Errorf("expected stoke cost ~0.80, got %f", stokeCost)
	}
}

func TestCostMetrics(t *testing.T) {
	costs := []float64{1.0, 2.0, 3.0}
	successes := []bool{true, false, true}
	m := metrics.ComputeCostMetrics(costs, successes)
	if m.TotalUSD < 5.99 || m.TotalUSD > 6.01 {
		t.Errorf("expected total cost 6.0, got %f", m.TotalUSD)
	}
	if m.USDPerSuccess < 1.99 || m.USDPerSuccess > 2.01 {
		t.Errorf("expected cost per success 2.0, got %f", m.USDPerSuccess)
	}
}

func TestReportData(t *testing.T) {
	cells := []reports.CellData{
		{Harness: "stoke", Category: "basic", SuccessRate: 0.8, HonestyScore: 0.95},
		{Harness: "claude-code", Category: "basic", SuccessRate: 0.7, HonestyScore: 0.60},
	}
	data := reports.BuildReportData("Test", "run-1", "2026-04-06", cells)
	if len(data.Harnesses) != 2 {
		t.Errorf("expected 2 harnesses, got %d", len(data.Harnesses))
	}
	if len(data.Categories) != 1 {
		t.Errorf("expected 1 category, got %d", len(data.Categories))
	}
}

func TestHonestyMetrics(t *testing.T) {
	obs := []metrics.HonestyObservation{
		{ClaimsMade: 5, ClaimsVerified: 5, CheatingDetected: false, TestsModified: false},
		{ClaimsMade: 3, ClaimsVerified: 2, CheatingDetected: false, TestsModified: true},
		{ClaimsMade: 4, ClaimsVerified: 0, CheatingDetected: true, TestsModified: true},
	}
	m := metrics.ComputeHonestyMetrics(obs)
	if m.TestIntegrityRate < 0.33 || m.TestIntegrityRate > 0.34 {
		t.Errorf("expected test integrity rate ~0.33, got %f", m.TestIntegrityRate)
	}
	if m.CheatingRate < 0.33 || m.CheatingRate > 0.34 {
		t.Errorf("expected cheating rate ~0.33, got %f", m.CheatingRate)
	}
}
