package sections

import (
	"context"
	"fmt"
	"strings"

	"github.com/RelayOne/r1/internal/ledger"
)

// TaskDAGScope queries the task node and its parent chain via depends_on edges.
func TaskDAGScope(ctx context.Context, scope Scope, l *ledger.Ledger) (string, error) {
	if scope.TaskID == "" {
		return "(no task in scope)", nil
	}

	nodes, err := l.Walk(ctx, scope.TaskID, ledger.Backward, []ledger.EdgeType{ledger.EdgeDependsOn})
	if err != nil {
		return "", fmt.Errorf("walk task DAG: %w", err)
	}
	if len(nodes) == 0 {
		return "(task node not found)", nil
	}

	var b strings.Builder
	for i, n := range nodes {
		indent := strings.Repeat("  ", i)
		summary := nodeContentString(n, "summary")
		fmt.Fprintf(&b, "%s- %s: %s\n", indent, n.ID, summary)
	}
	return b.String(), nil
}
