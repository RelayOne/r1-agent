package sections

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ericmacdougall/stoke/internal/ledger"
)

// ActiveLoops queries non-terminal loop nodes in the current mission.
func ActiveLoops(ctx context.Context, scope Scope, l *ledger.Ledger) (string, error) {
	nodes, err := l.Query(ctx, ledger.QueryFilter{
		Type:      "loop",
		MissionID: scope.MissionID,
	})
	if err != nil {
		return "", fmt.Errorf("query loops: %w", err)
	}
	// Filter to non-terminal loops.
	var active []ledger.Node
	for _, n := range nodes {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(n.Content, &m); err != nil {
			active = append(active, n)
			continue
		}
		raw, ok := m["status"]
		if !ok {
			active = append(active, n)
			continue
		}
		var status string
		if err := json.Unmarshal(raw, &status); err != nil || status != "terminal" {
			active = append(active, n)
		}
	}
	if len(active) == 0 {
		return "(no active loops)", nil
	}
	return renderNodeList(active, "summary", 0), nil
}
