package sections

import (
	"context"
	"fmt"

	"github.com/RelayOne/r1/internal/ledger"
)

// DissentHistory queries dissent nodes in the current loop. Results are
// scoped to the current loop/task/branch (not just MissionID) and capped
// at maxItems (0 == unlimited).
func DissentHistory(ctx context.Context, scope Scope, l *ledger.Ledger, maxItems int) (string, error) {
	nodes, err := l.Query(ctx, ledger.QueryFilter{
		Type:      "dissent",
		MissionID: scope.MissionID,
	})
	if err != nil {
		return "", fmt.Errorf("query dissent: %w", err)
	}
	nodes = filterByScope(nodes, scope)
	if len(nodes) == 0 {
		return "(no dissent recorded)", nil
	}
	return renderNodeList(nodes, "objection", maxItems), nil
}
