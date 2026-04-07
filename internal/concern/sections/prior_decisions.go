package sections

import (
	"context"
	"fmt"

	"github.com/ericmacdougall/stoke/internal/ledger"
)

// PriorDecisions queries decision nodes scoped to the current task.
func PriorDecisions(ctx context.Context, scope Scope, l *ledger.Ledger) (string, error) {
	nodes, err := l.Query(ctx, ledger.QueryFilter{
		Type:      "decision",
		MissionID: scope.MissionID,
	})
	if err != nil {
		return "", fmt.Errorf("query decisions: %w", err)
	}
	if len(nodes) == 0 {
		return "(no prior decisions)", nil
	}
	return renderNodeList(nodes, "rationale", 20), nil
}
