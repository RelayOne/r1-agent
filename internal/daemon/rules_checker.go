package daemon

import (
	"context"

	"github.com/RelayOne/r1/internal/rules"
)

type RulesToolChecker struct {
	Registry *rules.Registry
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
