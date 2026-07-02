package bench

import (
	"context"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/harness"
)

// activationMission is a minimal in-memory golden mission for the runner
// activation tests — Run only consumes ID/Title/Category/Acceptance.
func activationMission() *MissionConfig {
	return &MissionConfig{
		ID:         "activation-check",
		Title:      "Runner activation check",
		Category:   "greenfield",
		Acceptance: []string{"stance performs at least one model turn"},
	}
}

// TestRun_DrivesRunnerTurn proves bench missions now execute real substrate
// work (audit A041): the spawned stance is driven through the StanceRunner
// and its per-turn cost_record accounting lands in the mission metrics.
func TestRun_DrivesRunnerTurn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	r := NewRunner(t.TempDir())
	// Inject a mock with non-zero usage so the cost plumbing is
	// observable end-to-end: runner turn -> cost_record ledger node ->
	// ComputeMetrics -> RunResult.
	r.Provider = &harness.MockProvider{Responses: []*harness.ChatResponse{{
		Content:   "mission acknowledged",
		TokensIn:  120,
		TokensOut: 30,
		CostUSD:   0.005,
	}}}

	result, err := r.Run(ctx, activationMission())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.TokensUsed != 150 {
		t.Errorf("TokensUsed = %d, want 150 (runner turn not accounted)", result.TokensUsed)
	}
	if result.CostUSD < 0.004 || result.CostUSD > 0.006 {
		t.Errorf("CostUSD = %f, want ~0.005", result.CostUSD)
	}
	if result.LoopIterations != 1 {
		t.Errorf("LoopIterations = %d, want 1 (one stance spawned)", result.LoopIterations)
	}
	if result.LedgerCorrupted {
		t.Error("ledger corrupted")
	}
}

// TestRun_DefaultProviderOffline proves the default (nil Provider) path
// still runs a turn — deterministically, offline, and at zero recorded cost
// so golden baselines hold.
func TestRun_DefaultProviderOffline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	r := NewRunner(t.TempDir())
	result, err := r.Run(ctx, activationMission())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.CostUSD != 0 {
		t.Errorf("CostUSD = %f, want 0 for the offline default provider", result.CostUSD)
	}
	if result.TokensUsed != 0 {
		t.Errorf("TokensUsed = %d, want 0 for the offline default provider", result.TokensUsed)
	}
	if result.TerminalState != "converged" {
		t.Errorf("TerminalState = %q, want converged", result.TerminalState)
	}
}
