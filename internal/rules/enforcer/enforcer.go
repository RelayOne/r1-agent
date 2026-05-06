package enforcer

import (
	"context"
	"encoding/json"

	"github.com/RelayOne/r1/internal/rules"
	"github.com/RelayOne/r1/internal/rules/monitor"
)

type Enforcer struct {
	Registry *rules.Registry
	Monitor  *monitor.Monitor
}

func NewRepo(repoRoot string) *Enforcer {
	repoRoot = rules.InferRepoRoot(repoRoot)
	return &Enforcer{
		Registry: rules.NewRepoRegistry(repoRoot, nil),
		Monitor:  monitor.NewRepo(repoRoot),
	}
}

func (e *Enforcer) Check(ctx context.Context, toolName string, toolArgs json.RawMessage, checkCtx rules.CheckContext) (rules.CheckResult, error) {
	if e == nil || e.Registry == nil {
		return rules.CheckResult{Verdict: rules.VerdictPass}, nil
	}
	result, err := e.Registry.Check(ctx, toolName, toolArgs, checkCtx)
	if err != nil {
		return rules.CheckResult{}, err
	}
	if e.Monitor != nil {
		_ = e.Monitor.Record(monitor.Decision{
			RepoRoot:     checkCtx.RepoRoot,
			TaskID:       checkCtx.TaskID,
			ToolName:     toolName,
			Verdict:      string(result.Verdict),
			Reason:       result.Reason,
			RuleHits:     len(result.Evaluations),
			Blocked:      result.Verdict == rules.VerdictBlock,
			MatchedRules: matchedRuleIDs(result.Evaluations),
		})
	}
	return result, nil
}

func matchedRuleIDs(evals []rules.Evaluation) []string {
	ids := make([]string, 0, len(evals))
	for _, eval := range evals {
		ids = append(ids, eval.RuleID)
	}
	return ids
}
