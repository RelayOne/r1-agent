package sections

import (
	"context"
	"fmt"

	"github.com/RelayOne/r1/internal/ledger"
)

// SDMAdvisories queries advisory nodes for the current branch. Results are
// scoped to the current loop/task/branch (not just MissionID) and capped
// at maxItems (0 == unlimited).
func SDMAdvisories(ctx context.Context, scope Scope, l *ledger.Ledger, maxItems int) (string, error) {
	nodes, err := l.Query(ctx, ledger.QueryFilter{
		Type:      "advisory",
		MissionID: scope.MissionID,
	})
	if err != nil {
		return "", fmt.Errorf("query advisories: %w", err)
	}
	nodes = filterByScope(nodes, scope)
	if len(nodes) == 0 {
		return "(no advisories)", nil
	}
	return renderNodeList(nodes, "advisory", maxItems), nil
}
