// Package sections implements per-section ledger query and render logic
// for concern field construction.
package sections

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/RelayOne/r1-agent/internal/ledger"
)

// Scope specifies where in the task DAG this concern field is scoped.
type Scope struct {
	MissionID string
	TaskID    string
	LoopID    string
	BranchID  string
}

// QueryFunc queries the ledger for a specific section and returns rendered text.
type QueryFunc func(ctx context.Context, scope Scope, l *ledger.Ledger) (string, error)

// nodeContentString extracts a string field from a node's JSON content.
func nodeContentString(n ledger.Node, field string) string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(n.Content, &m); err != nil {
		return string(n.Content)
	}
	raw, ok := m[field]
	if !ok {
		return string(n.Content)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return string(raw)
	}
	return s
}

// renderNodeList formats a slice of nodes into a bulleted list.
func renderNodeList(nodes []ledger.Node, field string, maxItems int) string {
	var b strings.Builder
	count := 0
	for _, n := range nodes {
		if maxItems > 0 && count >= maxItems {
			break
		}
		text := nodeContentString(n, field)
		if text == "" {
			continue
		}
		fmt.Fprintf(&b, "- [%s] %s\n", n.ID, text)
		count++
	}
	return b.String()
}
