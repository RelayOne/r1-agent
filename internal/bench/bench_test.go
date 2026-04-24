package bench

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/ledger"
)

// goldenDir returns the path to the golden directory relative to this test file.
func goldenDir(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine test file path")
	}
	return filepath.Join(filepath.Dir(filename), "golden")
}

func TestLoadMission(t *testing.T) {
	r := NewRunner(goldenDir(t))
	cfg, err := r.LoadMission("hello-world")
	if err != nil {
		t.Fatalf("LoadMission: %v", err)
	}
	if cfg.ID != "hello-world" {
		t.Errorf("ID = %q, want %q", cfg.ID, "hello-world")
	}
	if cfg.Title != "Hello World Greenfield" {
		t.Errorf("Title = %q, want %q", cfg.Title, "Hello World Greenfield")
	}
	if cfg.Category != "greenfield" {
		t.Errorf("Category = %q, want %q", cfg.Category, "greenfield")
	}
	if cfg.Difficulty != "easy" {
		t.Errorf("Difficulty = %q, want %q", cfg.Difficulty, "easy")
	}
	if len(cfg.Acceptance) != 3 {
		t.Errorf("Acceptance count = %d, want 3", len(cfg.Acceptance))
	}
}

func TestListMissions(t *testing.T) {
	r := NewRunner(goldenDir(t))
	missions, err := r.ListMissions()
	if err != nil {
		t.Fatalf("ListMissions: %v", err)
	}
	if len(missions) < 1 {
		t.Fatalf("ListMissions returned %d missions, want >= 1", len(missions))
	}
	found := false
	for _, m := range missions {
		if m.ID == "hello-world" {
			found = true
			break
		}
	}
	if !found {
		t.Error("hello-world mission not found in ListMissions")
	}
}

func TestCompare(t *testing.T) {
	baseline := &RunResult{
		MissionID:       "test-mission",
		TerminalState:   "converged",
		AcceptanceMet:   3,
		AcceptanceTotal: 3,
		CostUSD:         1.00,
		WallTimeMs:      5000,
		TokensUsed:      1000,
	}
	current := &RunResult{
		MissionID:       "test-mission",
		TerminalState:   "converged",
		AcceptanceMet:   3,
		AcceptanceTotal: 3,
		CostUSD:         1.10,
		WallTimeMs:      5500,
		TokensUsed:      1100,
	}

	result := Compare(baseline, current)

	if result.Mission != "test-mission" {
		t.Errorf("Mission = %q, want %q", result.Mission, "test-mission")
	}
	if result.Regression {
		t.Error("expected no regression for small cost increase")
	}
	costDelta := result.Delta["cost_usd"]
	if costDelta < 0.09 || costDelta > 0.11 {
		t.Errorf("cost delta = %f, want ~0.10", costDelta)
	}
}

func TestDetectRegressionAcceptance(t *testing.T) {
	baseline := &RunResult{
		TerminalState: "converged",
		AcceptanceMet: 3,
	}
	current := &RunResult{
		TerminalState: "converged",
		AcceptanceMet: 2,
	}
	if !DetectRegression(baseline, current) {
		t.Error("expected regression when acceptance dropped")
	}
}

func TestDetectRegressionTerminalState(t *testing.T) {
	baseline := &RunResult{
		TerminalState: "converged",
		AcceptanceMet: 3,
	}
	current := &RunResult{
		TerminalState: "escalated",
		AcceptanceMet: 3,
	}
	if !DetectRegression(baseline, current) {
		t.Error("expected regression when state changed from converged to escalated")
	}
}

func TestDetectRegressionCost(t *testing.T) {
	baseline := &RunResult{
		TerminalState: "converged",
		AcceptanceMet: 3,
		CostUSD:       1.00,
	}
	current := &RunResult{
		TerminalState: "converged",
		AcceptanceMet: 3,
		CostUSD:       2.00,
	}
	if !DetectRegression(baseline, current) {
		t.Error("expected regression when cost doubled")
	}
}

func TestDetectRegressionNoRegression(t *testing.T) {
	baseline := &RunResult{
		TerminalState: "converged",
		AcceptanceMet: 3,
		CostUSD:       1.00,
		WallTimeMs:    5000,
	}
	current := &RunResult{
		TerminalState: "converged",
		AcceptanceMet: 3,
		CostUSD:       1.20,
		WallTimeMs:    6000,
	}
	if DetectRegression(baseline, current) {
		t.Error("expected no regression for minor increases")
	}
}

func TestReport(t *testing.T) {
	results := []RunResult{
		{
			MissionID:       "test-1",
			TerminalState:   "converged",
			AcceptanceMet:   2,
			AcceptanceTotal: 3,
			CostUSD:         0.50,
			TokensUsed:      500,
			WallTimeMs:      3000,
		},
	}

	report := Report(results)
	if report == "" {
		t.Fatal("Report returned empty string")
	}
	if !strings.Contains(report, "test-1") {
		t.Error("Report does not contain mission ID")
	}
	if !strings.Contains(report, "converged") {
		t.Error("Report does not contain terminal state")
	}
	if !strings.Contains(report, "Bench Report") {
		t.Error("Report does not contain header")
	}
}

func TestComparisonReport(t *testing.T) {
	comparisons := []ComparisonResult{
		{
			Mission:    "test-1",
			Baseline:   RunResult{TerminalState: "converged"},
			Current:    RunResult{TerminalState: "escalated"},
			Regression: true,
			Delta:      map[string]float64{"cost_usd": 0.5, "acceptance_met": -1},
		},
	}

	report := ComparisonReport(comparisons)
	if report == "" {
		t.Fatal("ComparisonReport returned empty string")
	}
	if !strings.Contains(report, "YES") {
		t.Error("ComparisonReport does not indicate regression")
	}
}

func TestComputeMetricsEmptyLedger(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bench-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	l, err := ledger.New(filepath.Join(tmpDir, "ledger"))
	if err != nil {
		t.Fatalf("ledger.New: %v", err)
	}
	defer l.Close()

	b, err := bus.New(filepath.Join(tmpDir, "bus"))
	if err != nil {
		t.Fatalf("bus.New: %v", err)
	}
	defer b.Close()

	ctx := context.Background()
	result, err := ComputeMetrics(ctx, l, b, "nonexistent")
	if err != nil {
		t.Fatalf("ComputeMetrics: %v", err)
	}

	if result.CostUSD != 0 {
		t.Errorf("CostUSD = %f, want 0", result.CostUSD)
	}
	if result.TokensUsed != 0 {
		t.Errorf("TokensUsed = %d, want 0", result.TokensUsed)
	}
	if result.TrustFirings != 0 {
		t.Errorf("TrustFirings = %d, want 0", result.TrustFirings)
	}
	if result.LoopIterations != 0 {
		t.Errorf("LoopIterations = %d, want 0", result.LoopIterations)
	}
	if result.DissentCount != 0 {
		t.Errorf("DissentCount = %d, want 0", result.DissentCount)
	}
	if result.TerminalState != "converged" {
		t.Errorf("TerminalState = %q, want %q", result.TerminalState, "converged")
	}
}
