package daemon

import (
	"context"
	"testing"

	"github.com/RelayOne/r1/internal/rules"
)

type stubExecutor struct {
	calls int
}

func (s *stubExecutor) Type() string { return "stub" }

func (s *stubExecutor) Capabilities() []string { return []string{"*"} }

func (s *stubExecutor) Execute(ctx context.Context, t *Task) ExecutionResult {
	s.calls++
	return ExecutionResult{MissionID: "stub"}
}

func TestGuardedExecutorBlocksThenAllows(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	registry := rules.NewFSRegistry(stateDir, nil)
	rule, err := registry.AddWithOptions(context.Background(), rules.AddRequest{
		Text: "never call tool delete_branch with name matching ^(staging|dev|prod)$",
	})
	if err != nil {
		t.Fatalf("AddWithOptions: %v", err)
	}

	base := &stubExecutor{}
	exec := GuardedExecutor{
		Base:    base,
		Checker: RulesToolChecker{Registry: registry},
	}

	blocked := exec.Execute(context.Background(), &Task{
		ID: "task-1",
		Meta: map[string]string{
			"tool_name": "delete_branch",
			"tool_args": `{"name":"staging"}`,
		},
	})
	if blocked.Err == nil {
		t.Fatalf("blocked.Err = nil, want non-nil")
	}
	if base.calls != 0 {
		t.Fatalf("base.calls after blocked run = %d, want 0", base.calls)
	}

	if err := registry.Delete(rule.ID); err != nil {
		t.Fatalf("Delete rule: %v", err)
	}

	allowed := exec.Execute(context.Background(), &Task{
		ID: "task-2",
		Meta: map[string]string{
			"tool_name": "delete_branch",
			"tool_args": `{"name":"feature/foo"}`,
		},
	})
	if allowed.Err != nil {
		t.Fatalf("allowed.Err = %v, want nil", allowed.Err)
	}
	if base.calls != 1 {
		t.Fatalf("base.calls after allowed run = %d, want 1", base.calls)
	}
}
