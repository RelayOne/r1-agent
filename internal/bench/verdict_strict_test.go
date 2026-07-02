package bench

import (
	"context"
	"testing"
)

// TestRequireExecutionStrictMode covers SOTA gap #5: with RequireExecution,
// symbol/file/diff heuristics can no longer mark an item complete — only a
// passing held-out test does.
func TestRequireExecutionStrictMode(t *testing.T) {
	ctx := context.Background()

	// An item satisfied under the lenient heuristic (its required symbol is
	// in the diff) but with NO test command.
	symOnly := PlanItem{RequiredSymbols: []string{"func Handler"}}
	diff := "+func Handler() {}\n"

	lenient := &VerdictScorer{}
	if !lenient.planItemSatisfied(ctx, "", symOnly, diff) {
		t.Fatal("precondition: lenient scorer should satisfy the symbol-only item")
	}

	strict := &VerdictScorer{RequireExecution: true}
	if strict.planItemSatisfied(ctx, "", symOnly, diff) {
		t.Error("strict mode must NOT satisfy an item on symbols alone")
	}

	// With a passing test command, strict mode satisfies it.
	withTest := PlanItem{TestCommand: "go test ./..."}
	strictWithExec := &VerdictScorer{
		RequireExecution: true,
		ExecCommand: func(_ context.Context, _, _ string) (int, error) { return 0, nil },
	}
	if !strictWithExec.planItemSatisfied(ctx, "/w", withTest, diff) {
		t.Error("strict mode must satisfy an item whose held-out test passes")
	}
	// Failing test -> not satisfied.
	strictFail := &VerdictScorer{
		RequireExecution: true,
		ExecCommand: func(_ context.Context, _, _ string) (int, error) { return 1, nil },
	}
	if strictFail.planItemSatisfied(ctx, "/w", withTest, diff) {
		t.Error("strict mode must NOT satisfy an item whose held-out test fails")
	}
}
