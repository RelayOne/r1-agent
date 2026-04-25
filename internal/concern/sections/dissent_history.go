package sections

import (
	"context"
	"fmt"

	"github.com/RelayOne/r1-agent/internal/ledger"
)

// DissentHistory queries dissent nodes in the current loop.
func DissentHistory(ctx context.Context, scope Scope, l *ledger.Ledger) (string, error) {
	nodes, err := l.Query(ctx, ledger.QueryFilter{
		Type:      "dissent",
		MissionID: scope.MissionID,
	})
	if err != nil {
		return "", fmt.Errorf("query dissent: %w", err)
	}
	if len(nodes) == 0 {
		return "(no dissent recorded)", nil
	}
	return renderNodeList(nodes, "objection", 0), nil
}
