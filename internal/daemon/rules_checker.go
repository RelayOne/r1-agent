package daemon

import (
	"context"

	"github.com/RelayOne/r1/internal/rules"
	rulesenforcer "github.com/RelayOne/r1/internal/rules/enforcer"
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
	enforcer := rulesenforcer.NewRepo(req.RepoRoot)
	if c.Registry != nil {
		enforcer.Registry = c.Registry
	}
	result, err := enforcer.Check(ctx, req.ToolName, req.ToolArgs, rules.CheckContext{
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
