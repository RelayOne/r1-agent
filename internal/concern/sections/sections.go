// Package sections implements per-section ledger query and render logic
// for concern field construction.
package sections

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/RelayOne/r1/internal/ledger"
)

// Scope specifies where in the task DAG this concern field is scoped.
type Scope struct {
	MissionID string
	TaskID    string
	LoopID    string
	BranchID  string
}

// QueryFunc queries the ledger for a specific section and returns rendered
// text. maxItems is the SectionSpec.Cap from the template: the maximum
// number of entries the renderer may emit (0 == unlimited). Renderers that
// produce a list MUST honor it; single-value renderers ignore it.
type QueryFunc func(ctx context.Context, scope Scope, l *ledger.Ledger, maxItems int) (string, error)

// nodeContentField extracts a string field from a node's JSON content and
// reports whether the field was present as a string. Unlike
// nodeContentString it never falls back to the whole content blob, so a
// caller can distinguish "field absent" from "field present but empty".
func nodeContentField(n ledger.Node, field string) (string, bool) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(n.Content, &m); err != nil {
		return "", false
	}
	raw, ok := m[field]
	if !ok {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// filterByScope drops nodes that belong to a DIFFERENT loop, task, or
// branch than the one being projected. Section renderers previously
// scoped by MissionID alone, so a node written under loop/task/branch A
// leaked into loop/task/branch B's concern projection. For each scope
// dimension whose value is set, a node is dropped only when it carries
// the matching content field AND that field's value differs; a node that
// never recorded the field is treated as mission-global and kept (so
// legitimately unscoped decisions/annotations still surface). This is the
// full-key scoping the audit requires while remaining backward compatible
// with untagged nodes.
func filterByScope(nodes []ledger.Node, scope Scope) []ledger.Node {
	// Content-field scoping: a node is dropped only when it CARRIES the
	// dimension's field and that field's value differs; a node that never
	// recorded the field is kept as mission-global. This scopes nodes that
	// embed their own loop_ref / task_dag_scope / branch_ref (e.g. decision
	// and branch-annotation nodes). Nodes that reference a scoped parent
	// instead of carrying the field themselves (dissent -> draft) are
	// scoped separately via scopeByReferencedLoop.
	dims := []struct{ field, want string }{
		{"loop_ref", scope.LoopID},
		{"task_dag_scope", scope.TaskID},
		{"branch_ref", scope.BranchID},
	}
	out := make([]ledger.Node, 0, len(nodes))
	for _, n := range nodes {
		keep := true
		for _, d := range dims {
			if d.want == "" {
				continue
			}
			if got, present := nodeContentField(n, d.field); present && got != d.want {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, n)
		}
	}
	return out
}

// scopeByReferencedLoop scopes nodes that do NOT carry a loop_ref of their
// own (e.g. dissent nodes, which reference a draft via refField) by
// resolving that reference to its parent node and comparing the parent's
// loop_ref to scope.LoopID. This closes the cross-loop leak that
// filterByScope cannot: a dissent node has only draft_ref, so a bare
// MissionID query surfaced every loop's objections in every loop's
// projection. A node with no reference, an unresolvable reference, or a
// parent with no loop_ref is kept (mission-global / fail-open for
// visibility). No-op when scope.LoopID is unset.
func scopeByReferencedLoop(ctx context.Context, nodes []ledger.Node, refField string, scope Scope, l *ledger.Ledger) []ledger.Node {
	if scope.LoopID == "" || l == nil {
		return nodes
	}
	out := make([]ledger.Node, 0, len(nodes))
	for _, n := range nodes {
		ref, present := nodeContentField(n, refField)
		if !present || ref == "" {
			out = append(out, n)
			continue
		}
		parent, err := l.Get(ctx, ledger.NodeID(ref))
		if err != nil || parent == nil {
			out = append(out, n)
			continue
		}
		loop, ok := nodeContentField(*parent, "loop_ref")
		if !ok || loop == "" || loop == scope.LoopID {
			out = append(out, n)
		}
	}
	return out
}

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
