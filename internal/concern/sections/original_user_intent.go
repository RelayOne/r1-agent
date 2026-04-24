package sections

import (
	"context"
	"fmt"

	"github.com/RelayOne/r1/internal/ledger"
)

// OriginalUserIntent queries the mission node for the user's verbatim goal text.
func OriginalUserIntent(ctx context.Context, scope Scope, l *ledger.Ledger) (string, error) {
	nodes, err := l.Query(ctx, ledger.QueryFilter{
		Type:      "mission",
		MissionID: scope.MissionID,
		Limit:     1,
	})
	if err != nil {
		return "", fmt.Errorf("query mission node: %w", err)
	}
	if len(nodes) == 0 {
		return "(no mission node found)", nil
	}
	return nodeContentString(nodes[0], "goal"), nil
}
