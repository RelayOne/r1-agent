package sections

import (
	"context"
	"fmt"

	"github.com/RelayOne/r1-agent/internal/ledger"
)

// SDMAdvisories queries advisory nodes for the current branch.
func SDMAdvisories(ctx context.Context, scope Scope, l *ledger.Ledger) (string, error) {
	nodes, err := l.Query(ctx, ledger.QueryFilter{
		Type:      "advisory",
		MissionID: scope.MissionID,
	})
	if err != nil {
		return "", fmt.Errorf("query advisories: %w", err)
	}
	if len(nodes) == 0 {
		return "(no advisories)", nil
	}
	return renderNodeList(nodes, "advisory", 0), nil
}
