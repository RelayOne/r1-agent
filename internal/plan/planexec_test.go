package plan

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type countingRunner struct {
	calls []string
	failOn map[string]bool
}

func (c *countingRunner) RunStep(_ context.Context, step Step) StepResult {
	c.calls = append(c.calls, step.ID)
	return StepResult{
		StepID: step.ID,
		Passed: !c.failOn[step.ID],
		Output: "ran " + step.ID,
	}
}

func TestComplexity_ShouldPlan(t *testing.T) {
	if ComplexityTrivial.ShouldPlan() || ComplexitySimple.ShouldPlan() {
		t.Error("trivial/simple should skip planning")
	}
	if !ComplexityModerate.ShouldPlan() || !ComplexityComplex.ShouldPlan() {
		t.Error("moderate/complex should plan")
	}
}

func TestExecute_SimpleSkipsPlanner(t *testing.T) {
	runner := &countingRunner{}
	res, err := Execute(context.Background(), ComplexitySimple, "build", "build a thing", nil, nil, runner)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(runner.calls) != 1 || runner.calls[0] != "main" {
		t.Errorf("simple should dispatch a single synthetic step; got %v", runner.calls)
	}
	if !res.Completed {
		t.Error("expected Completed=true")
	}
}

func TestExecute_ComplexRequiresPlanner(t *testing.T) {
	runner := &countingRunner{}
	_, err := Execute(context.Background(), ComplexityComplex, "x", "y", nil, nil, runner)
	if !errors.Is(err, ErrNoPlanner) {
		t.Errorf("want ErrNoPlanner, got %v", err)
	}
}

func TestExecute_NoRunner(t *testing.T) {
	_, err := Execute(context.Background(), ComplexitySimple, "x", "y", nil, nil, nil)
	if !errors.Is(err, ErrNoRunner) {
		t.Errorf("want ErrNoRunner, got %v", err)
	}
}

func TestExecute_PlannerAndRunner(t *testing.T) {
	runner := &countingRunner{}
	res, err := Execute(context.Background(), ComplexityComplex, "x",
		"step one\nstep two\nstep three", nil, SimplePlanner{}, runner)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(runner.calls) != 3 {
		t.Errorf("expected 3 steps, got %d (%v)", len(runner.calls), runner.calls)
	}
	if !res.Completed {
		t.Error("all steps passed → Completed=true")
	}
}

func TestExecute_StopOnFail(t *testing.T) {
	runner := &countingRunner{failOn: map[string]bool{"step-2": true}}
	// Build a plan with StopOnFail=true manually since
	// SimplePlanner defaults to false.
	p := &ExecPlan{
		TaskTitle:  "x",
		StopOnFail: true,
		Steps: []Step{
			{ID: "step-1"},
			{ID: "step-2"},
			{ID: "step-3"},
		},
	}
	res := runExec(context.Background(), p, runner)
	if res.Completed {
		t.Error("expected Completed=false after step-2 failed")
	}
	if !res.StoppedEarly {
		t.Error("expected StoppedEarly=true")
	}
	if len(runner.calls) != 2 {
		t.Errorf("expected 2 calls (step-1 + step-2) got %d (%v)", len(runner.calls), runner.calls)
	}
}

func TestExecute_ContinueOnFailWhenNotSet(t *testing.T) {
	runner := &countingRunner{failOn: map[string]bool{"step-2": true}}
	p := &ExecPlan{
		TaskTitle:  "x",
		StopOnFail: false,
		Steps: []Step{
			{ID: "step-1"},
			{ID: "step-2"},
			{ID: "step-3"},
		},
	}
	res := runExec(context.Background(), p, runner)
	if res.Completed {
		t.Error("any fail → Completed=false")
	}
	if res.StoppedEarly {
		t.Error("StopOnFail=false should not stop early")
	}
	if len(runner.calls) != 3 {
		t.Errorf("all 3 should run; got %v", runner.calls)
	}
}

func TestExecute_DependencyFailureSkips(t *testing.T) {
	runner := &countingRunner{failOn: map[string]bool{"a": true}}
	p := &ExecPlan{
		Steps: []Step{
			{ID: "a"},
			{ID: "b", Dependencies: []string{"a"}},
			{ID: "c"},
		},
	}
	res := runExec(context.Background(), p, runner)
	if len(runner.calls) != 2 {
		t.Errorf("b should skip due to dep; got %v", runner.calls)
	}
	// b's result should mention skipped.
	var bResult StepResult
	for _, r := range res.Results {
		if r.StepID == "b" {
			bResult = r
		}
	}
	if bResult.Passed {
		t.Error("b should not pass when dep failed")
	}
	if !strings.Contains(bResult.Output, "skipped") {
		t.Errorf("b.Output should mention skipped; got %q", bResult.Output)
	}
}

func TestSimplePlanner_SplitsLines(t *testing.T) {
	p, _ := SimplePlanner{}.Plan(context.Background(), "task", "do the first thing\ndo the second thing\n- bullet third", nil)
	if len(p.Steps) != 3 {
		t.Errorf("expected 3 steps, got %d (%+v)", len(p.Steps), p.Steps)
	}
	if !strings.Contains(p.Steps[2].Description, "bullet third") {
		t.Errorf("bullet prefix should be stripped; got %q", p.Steps[2].Description)
	}
}

func TestSimplePlanner_EmptyDescFallback(t *testing.T) {
	p, _ := SimplePlanner{}.Plan(context.Background(), "task", "", nil)
	if len(p.Steps) != 1 || p.Steps[0].ID != "main" {
		t.Errorf("empty desc should fall back to single 'main' step; got %+v", p.Steps)
	}
}

func TestRenderMarkdown(t *testing.T) {
	p := &ExecPlan{
		TaskTitle: "Refactor auth",
		Rationale: "Split JWT validation from session mgmt.",
		Steps: []Step{
			{ID: "s1", Title: "Extract JWT verify",
				Description: "Pull out the ed25519 verify helper.",
				Rationale: "Reusable across Web + Mobile"},
			{ID: "s2", Title: "Wire caller",
				Dependencies: []string{"s1"}},
		},
	}
	md := p.RenderMarkdown()
	for _, want := range []string{
		"# Plan: Refactor auth",
		"Extract JWT verify",
		"Depends on:_ s1",
		"Reusable across Web",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q\n---\n%s", want, md)
		}
	}
}
