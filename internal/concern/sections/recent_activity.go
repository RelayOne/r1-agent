package sections

import (
	"context"
	"fmt"

	"github.com/RelayOne/r1/internal/ledger"
)

// RecentActivity queries the last N nodes in the current scope.
func RecentActivity(ctx context.Context, scope Scope, l *ledger.Ledger) (string, error) {
	nodes, err := l.Query(ctx, ledger.QueryFilter{
		MissionID: scope.MissionID,
		Limit:     10,
	})
	if err != nil {
		return "", fmt.Errorf("query recent activity: %w", err)
	}
	if len(nodes) == 0 {
		return "(no recent activity)", nil
	}
	return renderNodeList(nodes, "summary", 10), nil
}
