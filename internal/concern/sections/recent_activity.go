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
	// Do NOT push maxItems into the query Limit: scope filtering runs AFTER
	// the fetch, so a pre-filter LIMIT starves the result (fewer than the
	// cap even when more in-scope nodes exist deeper in the recency order).
	// Fetch the mission's nodes, scope-filter, then cap in renderNodeList.
	nodes, err := l.Query(ctx, ledger.QueryFilter{
		MissionID: scope.MissionID,
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
