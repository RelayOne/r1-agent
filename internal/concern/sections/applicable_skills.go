package sections

import (
	"context"
	"fmt"

	"github.com/RelayOne/r1-agent/internal/ledger"
)

// ApplicableSkills queries skill nodes matching the current mission.
func ApplicableSkills(ctx context.Context, scope Scope, l *ledger.Ledger) (string, error) {
	nodes, err := l.Query(ctx, ledger.QueryFilter{
		Type:      "skill",
		MissionID: scope.MissionID,
	})
	if err != nil {
		return "", fmt.Errorf("query skills: %w", err)
	}
	if len(nodes) == 0 {
		return "", nil
	}
	return renderNodeList(nodes, "description", 0), nil
}
