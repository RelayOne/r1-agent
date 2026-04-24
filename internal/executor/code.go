package executor

import (
	"context"
	"errors"
	"fmt"

	"github.com/RelayOne/r1/internal/plan"
)

// CodeExecutor wraps the existing SOW / descent pipeline in
// cmd/stoke/sow_native.go behind the Executor interface. For
// Track B Task 19 this is an intentional pass-through wrapper:
//
//   - `stoke ship ...` and `stoke sow ...` continue to route
//     through the existing entry points UNCHANGED. That is the
//     backward-compatibility contract documented on the task spec.
//
//   - `stoke task "..."` routing to TaskCode returns a sentinel
//     error with exit code 2 and a message pointing the operator
//     at `stoke ship`. The real wiring (replace sow_native.go with
//     calls through the Executor) is scoped into a follow-up
//     commit so Task 19 stays a surgical plumbing change.
//
// Fields are exported so tests can inject fakes, and so the
// follow-up wiring can populate them from the existing setup in
// cmd/stoke without a package split.
type CodeExecutor struct {
	// RepoRoot is the absolute path to the repository the
	// executor operates on.
	RepoRoot string

	// ExecuteHook, if non-nil, overrides the default fall-back
	// behavior. The follow-up commit will populate this from
	// cmd/stoke/sow_native.go so TaskCode dispatches through the
	// existing SOW pipeline. Kept as a field (not a constructor
	// arg) so tests can swap in a fake without rewriting the
	// router integration.
	ExecuteHook func(ctx context.Context, p Plan, effort EffortLevel) (Deliverable, error)

	// BuildCriteriaHook overrides the default (nil) criteria
	// builder. Follow-up commits populate it from the SOW session
	// expansion code path.
	BuildCriteriaHook func(task Task, d Deliverable) []plan.AcceptanceCriterion

	// RepairHook overrides the default (nil) T4 repair function.
	RepairHook func(p Plan) func(ctx context.Context, directive string) error

	// EnvFixHook overrides the default (nil) T5 env-fix function.
	EnvFixHook func() func(ctx context.Context, rootCause, stderr string) bool
}

// NewCodeExecutor returns a CodeExecutor rooted at repoRoot. All
// hooks default to nil / fall-back behavior; callers (tests or the
// follow-up wiring) populate them before dispatching work.
func NewCodeExecutor(repoRoot string) *CodeExecutor {
	return &CodeExecutor{RepoRoot: repoRoot}
}

// TaskType reports TaskCode — CodeExecutor is the trunk executor
// for Stoke's primary use case.
func (e *CodeExecutor) TaskType() TaskType { return TaskCode }

// Execute routes through ExecuteHook when set; otherwise returns
// the documented fall-back error. See the package doc above for
// why.
func (e *CodeExecutor) Execute(ctx context.Context, p Plan, effort EffortLevel) (Deliverable, error) {
	if e.ExecuteHook != nil {
		return e.ExecuteHook(ctx, p, effort)
	}
	return nil, errors.New("CodeExecutor.Execute: use `stoke ship` or `stoke sow` for now; direct task routing lands in a follow-up commit (Track B Task 19)")
}

// BuildCriteria routes through BuildCriteriaHook when set; else nil.
func (e *CodeExecutor) BuildCriteria(task Task, d Deliverable) []plan.AcceptanceCriterion {
	if e.BuildCriteriaHook != nil {
		return e.BuildCriteriaHook(task, d)
	}
	return nil
}

// BuildRepairFunc routes through RepairHook when set; else nil.
func (e *CodeExecutor) BuildRepairFunc(p Plan) func(context.Context, string) error {
	if e.RepairHook != nil {
		return e.RepairHook(p)
	}
	return nil
}

// BuildEnvFixFunc routes through EnvFixHook when set; else nil.
func (e *CodeExecutor) BuildEnvFixFunc() func(context.Context, string, string) bool {
	if e.EnvFixHook != nil {
		return e.EnvFixHook()
	}
	return nil
}

// CodeDeliverable is the output shape for TaskCode — the repo
// root the change was produced in and the unified diff produced
// against BaseCommit.
type CodeDeliverable struct {
	RepoRoot string
	Diff     string
}

// Summary renders a one-line description of the code deliverable.
func (d CodeDeliverable) Summary() string {
	if d.Diff == "" {
		return fmt.Sprintf("code deliverable (repo=%s, empty diff)", d.RepoRoot)
	}
	return fmt.Sprintf("code deliverable (repo=%s, %d bytes diff)", d.RepoRoot, len(d.Diff))
}

// Size returns the byte length of the diff. Zero means no change.
func (d CodeDeliverable) Size() int { return len(d.Diff) }

// Compile-time assertion that *CodeExecutor satisfies Executor.
var _ Executor = (*CodeExecutor)(nil)

// Compile-time assertion that CodeDeliverable satisfies Deliverable.
var _ Deliverable = CodeDeliverable{}
