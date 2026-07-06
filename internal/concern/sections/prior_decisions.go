package sections

import (
	"context"
	"fmt"

	"github.com/RelayOne/r1/internal/ledger"
)

// PriorDecisions queries decision nodes scoped to the current task. Results
// are scoped to the current loop/task/branch (not just MissionID) and
// capped at maxItems (0 == unlimited).
func PriorDecisions(ctx context.Context, scope Scope, l *ledger.Ledger, maxItems int) (string, error) {
	nodes, err := l.Query(ctx, ledger.QueryFilter{
		Type:      "decision",
		MissionID: scope.MissionID,
	})
	if err != nil {
		return "", fmt.Errorf("query decisions: %w", err)
	}
	nodes = filterByScope(nodes, scope)
	if len(nodes) == 0 {
		return "(no prior decisions)", nil
	}
	return renderNodeList(nodes, "rationale", maxItems), nil
}
