package daemon

import (
	"context"

	"github.com/RelayOne/r1/internal/rules"
)

type RulesToolChecker struct {
	Registry *rules.Registry
}

func WrapExecutorWithRules(base Executor, registry *rules.Registry) Executor {
	switch guarded := base.(type) {
	case GuardedExecutor:
		guarded.Checker = RulesToolChecker{Registry: registry}
		return guarded
	case *GuardedExecutor:
		guarded.Checker = RulesToolChecker{Registry: registry}
		return guarded
	default:
		return GuardedExecutor{
			Base:    base,
			Checker: RulesToolChecker{Registry: registry},
		}
	}
}

func (c RulesToolChecker) CheckTool(ctx context.Context, req ToolCheckRequest) (ToolCheckResult, error) {
	if c.Registry == nil {
		return ToolCheckResult{Verdict: "PASS"}, nil
	}
	result, err := c.Registry.Check(ctx, req.ToolName, req.ToolArgs, rules.CheckContext{
		RepoRoot: req.RepoRoot,
		TaskID:   req.TaskID,
	})
	if err != nil {
		return ToolCheckResult{}, err
	}
	return ToolCheckResult{
		Verdict: string(result.Verdict),
		Reason:  result.Reason,
	}, nil
}
