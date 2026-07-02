package workflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/config"
	"github.com/RelayOne/r1/internal/engine"
	"github.com/RelayOne/r1/internal/model"
	"github.com/RelayOne/r1/internal/taskstate"
	"github.com/RelayOne/r1/internal/verify"
	"github.com/RelayOne/r1/internal/wisdom"
)

// cancelExecuteRunner runs the plan phase normally but blocks the execute
// phase until the context is cancelled, then returns ctx.Err() as the run
// error — simulating a Ctrl-C / timeout landing during execution.
type cancelExecuteRunner struct {
	*mockRunner
}

func (r *cancelExecuteRunner) Run(ctx context.Context, spec engine.RunSpec, onEvent engine.OnEventFunc) (engine.RunResult, error) {
	if spec.Phase.Name == "execute" {
		<-ctx.Done()
		return engine.RunResult{IsError: true, Subtype: "cancelled"}, ctx.Err()
	}
	return r.mockRunner.Run(ctx, spec, onEvent)
}

// TestExecuteCancellationClassified is the R5 workflow-side regression: when
// the parent context is cancelled during execute, Engine.Run must propagate a
// context.Canceled error (so cmd/r1 skips failure bookkeeping) and must NOT
// record the cancellation-induced failure as a learned wisdom gotcha.
func TestExecuteCancellationClassified(t *testing.T) {
	repo := initTestRepo(t)
	base := newMockRunner()
	runner := &cancelExecuteRunner{mockRunner: base}
	ws := wisdom.NewStore()

	policy := config.DefaultPolicy()
	policy.Verification.Build = false
	policy.Verification.Tests = false
	policy.Verification.Lint = false
	policy.Verification.ScopeCheck = false
	policy.Verification.CrossModelReview = false

	wf := Engine{
		RepoRoot:       repo,
		Task:           "Long task that gets cancelled",
		TaskType:       model.TaskTypeRefactor,
		WorktreeName:   "r5-cancel",
		AuthMode:       engine.AuthModeMode1,
		Policy:         policy,
		Worktrees:      stubManager{repo: repo},
		Runners:        engine.Registry{Claude: engine.NewClaudeRunner("claude")},
		Verifier:       verify.NewPipeline("", "", ""),
		State:          taskstate.NewTaskState("r5-cancel"),
		Wisdom:         ws,
		RunnerOverride: runner,
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	_, err := wf.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run err=%v, want a context.Canceled-wrapped error", err)
	}
	if got := ws.Learnings(); len(got) != 0 {
		t.Errorf("cancellation recorded as wisdom (would poison unrelated retries): %+v", got)
	}
	// The task must not be driven to a terminal Failed state on cancellation.
	if wf.State.Phase() == taskstate.Failed {
		t.Errorf("state=Failed on cancellation; cancelled work should stay resumable")
	}
}
