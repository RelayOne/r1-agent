package sections

import (
	"context"
	"fmt"

	"github.com/RelayOne/r1-agent/internal/ledger"
)

// SnapshotAnnotations queries annotation nodes for files in scope.
func SnapshotAnnotations(ctx context.Context, scope Scope, l *ledger.Ledger) (string, error) {
	nodes, err := l.Query(ctx, ledger.QueryFilter{
		Type:      "annotation",
		MissionID: scope.MissionID,
	})
	if err != nil {
		return "", fmt.Errorf("query annotations: %w", err)
	}
	if len(nodes) == 0 {
		return "(no annotations)", nil
	}
	return renderNodeList(nodes, "text", 0), nil
}
