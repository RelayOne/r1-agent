package sections

import (
	"context"
	"fmt"

	"github.com/RelayOne/r1/internal/ledger"
)

// RecentActivity queries the last N nodes in the current scope. maxItems
// (SectionSpec.Cap) bounds both the ledger fetch and the rendered list;
// 0 means unlimited.
func RecentActivity(ctx context.Context, scope Scope, l *ledger.Ledger, maxItems int) (string, error) {
	nodes, err := l.Query(ctx, ledger.QueryFilter{
		MissionID: scope.MissionID,
		Limit:     maxItems,
	})
	if err != nil {
		return "", fmt.Errorf("query recent activity: %w", err)
	}
	nodes = filterByScope(nodes, scope)
	if len(nodes) == 0 {
		return "(no recent activity)", nil
	}
	return renderNodeList(nodes, "summary", maxItems), nil
}
