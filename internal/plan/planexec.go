// Package plan — planexec.go
//
// STOKE-022 primitive #3: plan-and-execute wrapper. For
// complex tasks, generating an explicit markdown plan BEFORE
// entering a ReAct loop improves success rates vs. pure
// ReAct. The wrapper:
//
//   1. Calls a planner (LLM or deterministic) to produce a
//      markdown plan: sections + ordered steps.
//   2. Executes each step sequentially via a caller-supplied
//      runner, collecting per-step results.
//   3. Short-circuits on failure if StopOnFail is set.
//
// Scope of this file:
//
//   - Plan + Step types
//   - Planner interface (pluggable — LLM in production,
//     deterministic in tests)
//   - Execute(plan, runner) driver
//   - RenderMarkdown helpers for prompt injection
//
// The harness wires this into complex-task dispatch so the
// agent sees a plan before doing anything; simple tasks skip
// the planner entirely (configurable via Complexity hint).
package plan

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Step is one entry in a plan. Ordered + named so results
// can be matched back to steps for reporting.
type Step struct {
	ID          string
	Title       string
	Description string
	// Rationale, if set, is a short "why" for this step.
	// Shown in the rendered plan so the executor + a human
	// reader both see the reasoning.
	Rationale string
	// Dependencies lists other step IDs that must complete
	// before this step runs. Empty = no prerequisites.
	Dependencies []string
}

// ExecPlan is an ordered bundle of steps the runner will
// execute sequentially (respecting dependencies).
type ExecPlan struct {
	TaskTitle   string
	Rationale   string
	Steps       []Step
	// StopOnFail halts execution on the first step that
	// fails rather than continuing with remaining steps.
	// Safer default for side-effecting work; set false only
	// for independent-step pipelines.
	StopOnFail bool
}

// Complexity hints whether a task needs planning. Simple
// tasks skip the planner entirely (no LLM call, no markdown
// overhead).
type Complexity string

const (
	ComplexityTrivial Complexity = "trivial"  // bypass planning
	ComplexitySimple  Complexity = "simple"   // bypass planning
	ComplexityModerate Complexity = "moderate" // plan + execute
	ComplexityComplex Complexity = "complex"  // plan + execute (always)
)

// ShouldPlan reports whether a task at this complexity
// benefits from the planning wrapper.
func (c Complexity) ShouldPlan() bool {
	return c == ComplexityModerate || c == ComplexityComplex
}

// Planner produces an ExecPlan for a task. LLM-backed in
// production; deterministic in tests.
type Planner interface {
	Plan(ctx context.Context, taskTitle, taskDescription string, availableTools []string) (*ExecPlan, error)
}

// StepRunner executes one step and returns its outcome.
// Implementations typically dispatch the step to an agent
// loop + collect the result.
type StepRunner interface {
	RunStep(ctx context.Context, step Step) StepResult
}

// StepResult is the outcome of one step.
type StepResult struct {
	StepID   string
	Passed   bool
	Output   string
	Err      error
}

// PlanResult summarizes an execution.
type PlanResult struct {
	Plan        *ExecPlan
	Results     []StepResult
	Completed   bool // true when every step passed
	StoppedEarly bool
}

// ErrNoPlanner is returned when Execute is called without a
// planner for a complex-enough task.
var ErrNoPlanner = errors.New("plan: no planner supplied for a complex task")

// ErrNoRunner is returned when Execute is called without a
// StepRunner.
var ErrNoRunner = errors.New("plan: no step runner supplied")

// Execute produces + runs a plan. For Trivial / Simple
// complexity, skips the planner and dispatches a single
// synthetic step so the caller doesn't need to branch.
func Execute(ctx context.Context, complexity Complexity, taskTitle, taskDesc string, tools []string, planner Planner, runner StepRunner) (*PlanResult, error) {
	if runner == nil {
		return nil, ErrNoRunner
	}
	var p *ExecPlan
	if complexity.ShouldPlan() {
		if planner == nil {
			return nil, ErrNoPlanner
		}
		got, err := planner.Plan(ctx, taskTitle, taskDesc, tools)
		if err != nil {
			return nil, fmt.Errorf("plan: planner: %w", err)
		}
		p = got
	} else {
		// Single-step pseudo-plan so the downstream code
		// path is unified.
		p = &ExecPlan{
			TaskTitle: taskTitle,
			Rationale: "trivial/simple complexity — executing directly without plan generation",
			Steps: []Step{
				{ID: "main", Title: taskTitle, Description: taskDesc},
			},
		}
	}
	return runExec(ctx, p, runner), nil
}

// runExec dispatches each step, respecting dependencies.
// A step whose dependency failed is skipped (result marked
// Passed=false, Output="skipped: upstream dep failed").
//
// The plan is topologically sorted by Dependencies before
// execution begins so a step whose prerequisite appears
// LATER in the input order still runs correctly. Cycles + non-
// existent dep references surface via the sort: offending
// steps are appended to the end and will skip at runtime
// because their dep never ran.
func runExec(ctx context.Context, p *ExecPlan, runner StepRunner) *PlanResult {
	res := &PlanResult{Plan: p, Completed: true}
	ordered := topoSortSteps(p.Steps)
	done := map[string]StepResult{}
	for _, step := range ordered {
		if ctx.Err() != nil {
			res.Completed = false
			break
		}
		if depFailed := checkDependencies(step, done); depFailed != "" {
			skipped := StepResult{
				StepID: step.ID, Passed: false,
				Output: "skipped: upstream dep " + depFailed + " did not pass or was not reachable",
			}
			res.Results = append(res.Results, skipped)
			done[step.ID] = skipped
			res.Completed = false
			continue
		}
		result := runner.RunStep(ctx, step)
		result.StepID = step.ID
		res.Results = append(res.Results, result)
		done[step.ID] = result
		if !result.Passed {
			res.Completed = false
			if p.StopOnFail {
				res.StoppedEarly = true
				break
			}
		}
	}
	return res
}

// topoSortSteps returns the plan's steps in dependency-
// first order. Uses Kahn's algorithm: start with steps that
// have zero unsatisfied deps, emit them, remove their
// out-edges, repeat. Steps with missing/cyclic deps are
// appended at the end so their execution attempt records a
// proper skipped-upstream-dep outcome rather than
// disappearing.
//
// Stable secondary ordering: within a ready-set, steps
// preserve the original input order.
func topoSortSteps(steps []Step) []Step {
	if len(steps) <= 1 {
		return steps
	}
	byID := make(map[string]int, len(steps))
	for i, s := range steps {
		byID[s.ID] = i
	}
	// indegree[step index] = number of deps referring to
	// OTHER STEPS IN THIS PLAN (external-dep references are
	// treated as "already satisfied" so the step isn't
	// forever gated on something we don't track).
	indegree := make([]int, len(steps))
	for i, s := range steps {
		for _, dep := range s.Dependencies {
			if _, ok := byID[dep]; ok {
				indegree[i]++
			}
		}
	}
	// Queue: input-order ready steps.
	queue := make([]int, 0, len(steps))
	for i, d := range indegree {
		if d == 0 {
			queue = append(queue, i)
		}
	}
	out := make([]Step, 0, len(steps))
	visited := make([]bool, len(steps))
	for len(queue) > 0 {
		i := queue[0]
		queue = queue[1:]
		if visited[i] {
			continue
		}
		visited[i] = true
		out = append(out, steps[i])
		// Decrement indegree of every step that depends on i.
		for j, s := range steps {
			if visited[j] {
				continue
			}
			for _, dep := range s.Dependencies {
				if dep == steps[i].ID {
					indegree[j]--
					if indegree[j] == 0 {
						queue = append(queue, j)
					}
					break
				}
			}
		}
	}
	// Append any remaining unvisited steps (cycle / missing
	// dep). They preserve input order so the caller sees
	// deterministic output; their runtime dep check will
	// skip them with the "upstream did not pass or was not
	// reachable" message.
	for i, s := range steps {
		if !visited[i] {
			out = append(out, s)
		}
	}
	return out
}

func checkDependencies(step Step, done map[string]StepResult) string {
	for _, dep := range step.Dependencies {
		r, ok := done[dep]
		if !ok || !r.Passed {
			return dep
		}
	}
	return ""
}

// RenderMarkdown produces a markdown representation of the
// plan suitable for injection into an LLM prompt. The
// executor sees the plan as structured text rather than a
// Go struct.
func (p *ExecPlan) RenderMarkdown() string {
	if p == nil {
		return ""
	}
	var b strings.Builder
	if p.TaskTitle != "" {
		fmt.Fprintf(&b, "# Plan: %s\n\n", p.TaskTitle)
	}
	if strings.TrimSpace(p.Rationale) != "" {
		fmt.Fprintf(&b, "%s\n\n", strings.TrimSpace(p.Rationale))
	}
	if len(p.Steps) > 0 {
		b.WriteString("## Steps\n\n")
		for i, s := range p.Steps {
			fmt.Fprintf(&b, "%d. **%s** (id: %s)\n", i+1, s.Title, s.ID)
			if strings.TrimSpace(s.Description) != "" {
				fmt.Fprintf(&b, "   - %s\n", strings.TrimSpace(s.Description))
			}
			if strings.TrimSpace(s.Rationale) != "" {
				fmt.Fprintf(&b, "   - _Why:_ %s\n", strings.TrimSpace(s.Rationale))
			}
			if len(s.Dependencies) > 0 {
				fmt.Fprintf(&b, "   - _Depends on:_ %s\n", strings.Join(s.Dependencies, ", "))
			}
		}
	}
	return b.String()
}

// --- Deterministic planner for tests + heuristic fallback ---

// SimplePlanner is a deterministic planner that decomposes a
// task by splitting the description on newlines + numbered-
// list markers. Good enough for tests; production wires an
// LLM-backed planner through the Planner interface.
type SimplePlanner struct{}

// Plan implements Planner via heuristic line splitting.
func (SimplePlanner) Plan(_ context.Context, taskTitle, taskDesc string, _ []string) (*ExecPlan, error) {
	lines := strings.Split(taskDesc, "\n")
	var steps []Step
	id := 1
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Strip leading enumerators "1." / "- " / "* " / "• ".
		line = strings.TrimPrefix(line, "- ")
		line = strings.TrimPrefix(line, "* ")
		line = strings.TrimPrefix(line, "• ")
		for i := 0; i < 3; i++ {
			if len(line) > 2 && line[0] >= '0' && line[0] <= '9' && line[1] == '.' {
				line = strings.TrimSpace(line[2:])
			}
		}
		if line == "" {
			continue
		}
		steps = append(steps, Step{
			ID:          fmt.Sprintf("step-%d", id),
			Title:       planexecTruncate(line, 60),
			Description: line,
		})
		id++
	}
	if len(steps) == 0 {
		steps = []Step{{ID: "main", Title: taskTitle, Description: taskDesc}}
	}
	return &ExecPlan{
		TaskTitle:  taskTitle,
		Rationale:  "deterministic heuristic: one step per non-empty line in the task description",
		Steps:      steps,
		StopOnFail: false,
	}, nil
}

// Compile-time interface assertion.
var _ Planner = SimplePlanner{}

func planexecTruncate(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}
