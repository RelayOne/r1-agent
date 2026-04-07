package sections

import (
	"context"
	"fmt"

	"github.com/ericmacdougall/stoke/internal/ledger"
)

// ResearchReports queries research report nodes referenced by the current draft.
func ResearchReports(ctx context.Context, scope Scope, l *ledger.Ledger) (string, error) {
	nodes, err := l.Query(ctx, ledger.QueryFilter{
		Type:      "research",
		MissionID: scope.MissionID,
	})
	if err != nil {
		return "", fmt.Errorf("query research: %w", err)
	}
	if len(nodes) == 0 {
		return "(no research reports)", nil
	}
	return renderNodeList(nodes, "findings", 0), nil
}
