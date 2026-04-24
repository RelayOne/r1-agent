package executor

import (
	"context"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/plan"
)

// Compile-time assertion: fakeExecutor satisfies Executor. If this
// ever breaks, the interface changed shape and every real executor
// must adapt.
var _ Executor = (*fakeExecutor)(nil)

// fakeExecutor is the minimum satisfaction of Executor — exists
// purely to prove the interface is callable from outside the
// package and to let test helpers inject predictable return values.
type fakeExecutor struct {
	tt     TaskType
	result Deliverable
	err    error
}

func (f *fakeExecutor) TaskType() TaskType { return f.tt }
func (f *fakeExecutor) Execute(ctx context.Context, p Plan, e EffortLevel) (Deliverable, error) {
	return f.result, f.err
}
func (f *fakeExecutor) BuildCriteria(_ Task, _ Deliverable) []plan.AcceptanceCriterion {
	return []plan.AcceptanceCriterion{{ID: "fake-1", Description: "fake criterion"}}
}
func (f *fakeExecutor) BuildRepairFunc(_ Plan) func(context.Context, string) error {
	return func(context.Context, string) error { return nil }
}
func (f *fakeExecutor) BuildEnvFixFunc() func(context.Context, string, string) bool {
	return func(context.Context, string, string) bool { return true }
}

func TestTaskTypeString(t *testing.T) {
	cases := []struct {
		tt   TaskType
		want string
	}{
		{TaskUnknown, "unknown"},
		{TaskCode, "code"},
		{TaskResearch, "research"},
		{TaskBrowser, "browser"},
		{TaskDeploy, "deploy"},
		{TaskDelegate, "delegate"},
		{TaskChat, "chat"},
		{TaskType(999), "unknown"},
	}
	for _, tc := range cases {
		if got := tc.tt.String(); got != tc.want {
			t.Errorf("TaskType(%d).String() = %q, want %q", tc.tt, got, tc.want)
		}
	}
}

func TestEffortLevelString(t *testing.T) {
	cases := []struct {
		e    EffortLevel
		want string
	}{
		{EffortMinimal, "minimal"},
		{EffortStandard, "standard"},
		{EffortThorough, "thorough"},
		{EffortCritical, "critical"},
		{EffortLevel(42), "unknown"},
	}
	for _, tc := range cases {
		if got := tc.e.String(); got != tc.want {
			t.Errorf("EffortLevel(%d).String() = %q, want %q", tc.e, got, tc.want)
		}
	}
}

func TestFakeExecutorSatisfiesInterface(t *testing.T) {
	// Construct via the interface to make sure method set matches.
	var exec Executor = &fakeExecutor{tt: TaskCode}
	if exec.TaskType() != TaskCode {
		t.Fatalf("TaskType = %v, want TaskCode", exec.TaskType())
	}
	if _, err := exec.Execute(context.Background(), Plan{}, EffortMinimal); err != nil {
		t.Fatalf("Execute returned err = %v, want nil", err)
	}
	crit := exec.BuildCriteria(Task{}, nil)
	if len(crit) != 1 || crit[0].ID != "fake-1" {
		t.Fatalf("BuildCriteria returned %+v, want one criterion id=fake-1", crit)
	}
	if exec.BuildRepairFunc(Plan{}) == nil {
		t.Fatal("BuildRepairFunc returned nil, want non-nil")
	}
	if exec.BuildEnvFixFunc() == nil {
		t.Fatal("BuildEnvFixFunc returned nil, want non-nil")
	}
}

func TestCodeDeliverableImplementsDeliverable(t *testing.T) {
	var d Deliverable = CodeDeliverable{RepoRoot: "/tmp/r", Diff: "a\nb\n"}
	if d.Size() != 4 {
		t.Errorf("Size = %d, want 4", d.Size())
	}
	if !strings.Contains(d.Summary(), "/tmp/r") {
		t.Errorf("Summary = %q, want to contain repo path", d.Summary())
	}
	// Empty diff path.
	var empty Deliverable = CodeDeliverable{RepoRoot: "/tmp/r"}
	if empty.Size() != 0 {
		t.Errorf("empty Size = %d, want 0", empty.Size())
	}
	if !strings.Contains(empty.Summary(), "empty diff") {
		t.Errorf("empty Summary = %q, want to mention empty diff", empty.Summary())
	}
}

func TestCodeExecutorDefaultExecute(t *testing.T) {
	e := NewCodeExecutor("/tmp/repo")
	if e.TaskType() != TaskCode {
		t.Fatalf("TaskType = %v, want TaskCode", e.TaskType())
	}
	_, err := e.Execute(context.Background(), Plan{}, EffortStandard)
	if err == nil {
		t.Fatal("Execute returned nil err, want fall-back error")
	}
	if !strings.Contains(err.Error(), "stoke ship") {
		t.Errorf("error %q should point operator at `stoke ship`", err.Error())
	}
	if e.BuildCriteria(Task{}, nil) != nil {
		t.Errorf("BuildCriteria should return nil when no hook is set")
	}
	if e.BuildRepairFunc(Plan{}) != nil {
		t.Errorf("BuildRepairFunc should return nil when no hook is set")
	}
	if e.BuildEnvFixFunc() != nil {
		t.Errorf("BuildEnvFixFunc should return nil when no hook is set")
	}
}

func TestCodeExecutorHooksOverrideFallback(t *testing.T) {
	wantDeliv := CodeDeliverable{RepoRoot: "/r", Diff: "diff"}
	calledExec, calledCrit, calledRepair, calledEnv := false, false, false, false
	e := &CodeExecutor{
		RepoRoot: "/r",
		ExecuteHook: func(_ context.Context, _ Plan, _ EffortLevel) (Deliverable, error) {
			calledExec = true
			return wantDeliv, nil
		},
		BuildCriteriaHook: func(_ Task, _ Deliverable) []plan.AcceptanceCriterion {
			calledCrit = true
			return []plan.AcceptanceCriterion{{ID: "x"}}
		},
		RepairHook: func(_ Plan) func(context.Context, string) error {
			calledRepair = true
			return func(context.Context, string) error { return nil }
		},
		EnvFixHook: func() func(context.Context, string, string) bool {
			calledEnv = true
			return func(context.Context, string, string) bool { return true }
		},
	}
	deliv, err := e.Execute(context.Background(), Plan{}, EffortStandard)
	if err != nil || deliv != wantDeliv {
		t.Fatalf("Execute hook not invoked: err=%v deliv=%+v", err, deliv)
	}
	if crit := e.BuildCriteria(Task{}, nil); len(crit) != 1 || crit[0].ID != "x" {
		t.Fatalf("BuildCriteria hook not invoked: %+v", crit)
	}
	if e.BuildRepairFunc(Plan{}) == nil {
		t.Fatal("RepairHook should have produced non-nil func")
	}
	if e.BuildEnvFixFunc() == nil {
		t.Fatal("EnvFixHook should have produced non-nil func")
	}
	if !calledExec || !calledCrit || !calledRepair || !calledEnv {
		t.Fatalf("hook invocation flags: exec=%v crit=%v repair=%v env=%v", calledExec, calledCrit, calledRepair, calledEnv)
	}
}

// TestScaffoldingExecutorsReturnNotWired covered the TaskDelegate
// scaffold stub prior to work-stoke TASK 2. DelegateExecutor is
// now a real composition over Hirer + Delegator + A2A submit seam
// (see internal/executor/delegate.go and delegate_test.go for its
// behavior coverage), so the not-wired sentinel cases left in this
// file reduced to zero. The ExecutorNotWiredError sentinel is still
// in use by the BrowserExecutor interactive-mode branch and stays
// covered by browser_test.go.

