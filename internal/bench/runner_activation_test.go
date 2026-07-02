package bench

import (
	"context"
	"strings"
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

// TestRun_UseFullTemplates is the A100 end-to-end proof: with the opt-in
// set, Run goes through the production spawn path
// (harness.NewWithRoleTemplates), the dev role template renders the seeded
// mission/task ledger nodes into concern sections, and the runner delivers
// them to the model inside the system prompt.
func TestRun_UseFullTemplates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	mock := &harness.MockProvider{Responses: []*harness.ChatResponse{{
		Content:   "mission acknowledged",
		TokensIn:  10,
		TokensOut: 5,
	}}}

	r := NewRunner(t.TempDir())
	r.UseFullTemplates = true
	r.Provider = mock

	result, err := r.Run(ctx, activationMission())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	calls := mock.Calls()
	if len(calls) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(calls))
	}
	sp := calls[0].SystemPrompt
	for _, want := range []string{
		`<section name="original_user_intent">`,
		"Runner activation check", // mission Title, projected as the goal
		`<section name="task_dag_scope">`,
	} {
		if !strings.Contains(sp, want) {
			t.Errorf("SystemPrompt missing %q — role template did not render", want)
		}
	}

	if result.TokensUsed != 15 {
		t.Errorf("TokensUsed = %d, want 15", result.TokensUsed)
	}
	if result.LedgerCorrupted {
		t.Error("ledger corrupted")
	}
}

// TestRun_DefaultStaysSectionless pins the intentional default: without the
// opt-in, bench uses its section-less templates and no concern sections
// appear in the stance system prompt (fixture-weight guarantee).
func TestRun_DefaultStaysSectionless(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	mock := &harness.MockProvider{Responses: []*harness.ChatResponse{{
		Content: "mission acknowledged",
	}}}

	r := NewRunner(t.TempDir())
	r.Provider = mock

	if _, err := r.Run(ctx, activationMission()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	calls := mock.Calls()
	if len(calls) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(calls))
	}
	if strings.Contains(calls[0].SystemPrompt, `<section name=`) {
		t.Error("section-less default rendered concern sections — bypass regressed")
	}
}
