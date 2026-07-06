package sections

import (
	"context"
	"fmt"

	"github.com/RelayOne/r1/internal/ledger"
)

// DissentHistory queries dissent nodes for the current loop. Dissent nodes
// carry only draft_ref (no loop_ref of their own), so they are scoped by
// resolving draft_ref -> draft.loop_ref against scope.LoopID; without this
// every loop's objections leaked into every loop's projection. Capped at
// maxItems (0 == unlimited).
func DissentHistory(ctx context.Context, scope Scope, l *ledger.Ledger, maxItems int) (string, error) {
	nodes, err := l.Query(ctx, ledger.QueryFilter{
		Type:      "dissent",
		MissionID: scope.MissionID,
	})
	if err != nil {
		return "", fmt.Errorf("query dissent: %w", err)
	}
	nodes = scopeByReferencedLoop(ctx, nodes, "draft_ref", scope, l)
	if len(nodes) == 0 {
		return "(no dissent recorded)", nil
	}
	return renderNodeList(nodes, "objection", maxItems), nil
}
