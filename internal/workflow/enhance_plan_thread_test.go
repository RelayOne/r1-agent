package workflow

import (
	"context"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/config"
	"github.com/RelayOne/r1/internal/engine"
	"github.com/RelayOne/r1/internal/model"
	"github.com/RelayOne/r1/internal/taskstate"
	"github.com/RelayOne/r1/internal/verify"
	"github.com/RelayOne/r1/internal/wisdom"
)

// TestExecutePromptCarriesPlanOutput is the P1 regression: the plan phase
// runs a full agent turn, but its ResultText was previously consumed ONLY in
// PlanOnly mode and discarded on the execute path. This asserts that with
// PlanOnly=false the plan-phase output is threaded into the execute prompt the
// runner actually receives.
func TestExecutePromptCarriesPlanOutput(t *testing.T) {
	repo := initTestRepo(t)
	mock := newMockRunner()
	// A distinctive plan text that would never appear in the execute prompt
	// unless it was explicitly threaded through from the plan phase.
	mock.PlanOutput = "PLAN_MARKER_ABC123: Step 1 edit main.go. Step 2 wire the handler."
	mock.FilesToWrite = map[string]string{
		"main.go": "package main\n\nfunc main() {}\n",
	}

	policy := config.DefaultPolicy()
	policy.Verification.Build = false
	policy.Verification.Tests = false
	policy.Verification.Lint = false
	policy.Verification.ScopeCheck = false
	policy.Verification.CrossModelReview = false

	wf := Engine{
		RepoRoot:       repo,
		Task:           "Investigate the handler subsystem and refactor request routing to support pluggable middleware, updating the wiring, storage, and tests",
		TaskType:       model.TaskTypeRefactor,
		WorktreeName:   "p1-plan-thread",
		AuthMode:       engine.AuthModeMode1,
		Policy:         policy,
		Worktrees:      stubManager{repo: repo},
		Runners:        engine.Registry{Claude: engine.NewClaudeRunner("claude")},
		Verifier:       verify.NewPipeline("", "", ""),
		State:          taskstate.NewTaskState("p1-plan-thread"),
		Wisdom:         wisdom.NewStore(),
		PlanOnly:       false,
		RunnerOverride: mock,
	}

	result, err := wf.Run(context.Background())
	// Merge may fail on the stub git tree; anything up to merge should pass.
	if err != nil && !strings.Contains(err.Error(), "merge") {
		t.Fatalf("workflow failed (non-merge): %v", err)
	}

	execPrompts := mock.Prompts["execute"]
	if len(execPrompts) == 0 {
		t.Fatal("execute phase never ran; cannot verify plan threading")
	}
	first := execPrompts[0]
	if !strings.Contains(first, "PLAN_MARKER_ABC123") {
		t.Errorf("execute prompt does not carry the plan-phase output; prompt:\n%s", first)
	}
	if !strings.Contains(first, "Implementation plan (from plan phase)") {
		t.Errorf("execute prompt missing the plan section header")
	}
	// Result.PlanOutput must be populated even off the PlanOnly path.
	if result.PlanOutput != mock.PlanOutput {
		t.Errorf("Result.PlanOutput=%q, want %q", result.PlanOutput, mock.PlanOutput)
	}
}

// TestPlanPhaseSkippedForTrivialTask covers SOTA gap #3: the always-on
// plan phase is now gated on task complexity. A trivial/clear-scope task
// must skip the plan phase (no plan agent call) and go straight to
// execute+verify, while --plan-only always forces planning.
func TestPlanPhaseSkippedForTrivialTask(t *testing.T) {
	// Sanity: the classifier must actually rate this simple, else the test
	// proves nothing.
	if taskComplexity("Add a getter").ShouldPlan() {
		t.Fatal("precondition: 'Add a getter' should classify as non-planning")
	}
	repo := initTestRepo(t)
	mock := newMockRunner()
	policy := config.DefaultPolicy()
	policy.Verification.Build = false
	policy.Verification.Tests = false
	policy.Verification.Lint = false
	policy.Verification.ScopeCheck = false
	policy.Verification.CrossModelReview = false

	wf := Engine{
		RepoRoot:       repo,
		Task:           "Add a getter",
		TaskType:       model.TaskTypeRefactor,
		WorktreeName:   "trivial-noplan",
		AuthMode:       engine.AuthModeMode1,
		Policy:         policy,
		Worktrees:      stubManager{repo: repo},
		Runners:        engine.Registry{Claude: engine.NewClaudeRunner("claude")},
		Verifier:       verify.NewPipeline("", "", ""),
		State:          taskstate.NewTaskState("trivial-noplan"),
		RunnerOverride: mock,
	}
	_, _ = wf.Run(context.Background())

	if mock.Calls["plan"] != 0 {
		t.Errorf("plan calls=%d, want 0 (trivial task must skip planning)", mock.Calls["plan"])
	}
	if mock.Calls["execute"] == 0 {
		t.Error("execute must still run when planning is skipped")
	}
}
