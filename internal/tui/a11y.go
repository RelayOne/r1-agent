// a11y.go — Accessibility tree primitives for the agentic TUI test
// harness per specs/agentic-test-harness.md §12 item 12.
//
// Bubble Tea views are character grids; agents reading them through OCR
// is brittle and slow. Instead, every interactive Bubble Tea model that
// participates in the test harness implements A11yEmitter to produce a
// structured tree of (role, name, state, children) nodes that mirrors
// the rendered surface. Callers (the teatest_shim, the lint at
// tools/lint-view-without-api/) consume this tree directly.
//
// This is the TUI counterpart to:
//   - the React DOM accessibility tree (queried via Playwright MCP), and
//   - the Tauri command surface (queried via the Tauri MCP adapter).
//
// All three normalize to the same A11yNode shape so a single
// `*.agent.feature.md` step like:
//
//   When I click the button with name "Pin lane memory-curator"
//
// resolves to the same predicate regardless of which surface the
// agent is driving.
package tui

// A11yNode is one node in the synthetic accessibility tree emitted by
// a Bubble Tea model. The fields mirror ARIA semantics so a single
// step DSL can address a button on the web UI, a list-item in the
// TUI, or a Tauri command without per-surface branches.
type A11yNode struct {
	// StableID is the deterministic identifier the agent uses to
	// address this node across renders. Example: "lane-memory-curator-
	// kill-button". Format: lowercase, dash-separated, ascii. The lint
	// at tools/lint-view-without-api/ rejects nodes with empty IDs.
	StableID string `json:"stable_id"`

	// Role mirrors ARIA: button, list, listitem, textbox, link,
	// status, dialog, etc. Pick the closest semantic match; the lint
	// rejects free-form roles.
	Role string `json:"role"`

	// Name is the accessible name (e.g. "Kill lane memory-curator").
	// Verb-noun phrasing preferred for actionables; noun phrases for
	// containers.
	Name string `json:"name"`

	// State carries dynamic flags: pressed, expanded, busy, selected,
	// disabled, etc. Values are short strings ("true", "false", or a
	// custom token like "kill-pending").
	State map[string]string `json:"state,omitempty"`

	// Children are nested A11yNodes emitted by sub-models.
	Children []A11yNode `json:"children,omitempty"`
}

// A11yEmitter is the interface every Bubble Tea model in the harness
// implements. It returns the synthetic accessibility node for the
// current model state. The signature is deliberately unparameterized:
// implementations construct the node from `m.<field>` accesses
// without runtime configuration.
//
// Example:
//
//	func (m LaneSidebar) A11y() A11yNode {
//	    children := []A11yNode{}
//	    for _, lane := range m.lanes {
//	        children = append(children, A11yNode{
//	            StableID: "lane-" + lane.ID,
//	            Role:     "listitem",
//	            Name:     "Lane " + lane.Name,
//	        })
//	    }
//	    return A11yNode{
//	        StableID: "lane-sidebar",
//	        Role:     "complementary",
//	        Name:     "Agents sidebar",
//	        Children: children,
//	    }
//	}
type A11yEmitter interface {
	// StableID returns the model's deterministic identifier.
	StableID() string
	// A11y returns the synthetic accessibility node tree rooted at
	// this model.
	A11y() A11yNode
}

// FlattenA11y walks an A11yNode tree depth-first and returns a flat
// slice. Useful for the lint to enumerate every leaf actionable, and
// for `r1.tui.snapshot` clients that want a flat list rather than a
// nested tree.
func FlattenA11y(root A11yNode) []A11yNode {
	out := []A11yNode{root}
	for _, c := range root.Children {
		out = append(out, FlattenA11y(c)...)
	}
	return out
}

// FindByStableID does a depth-first search for the node with the given
// stable_id and returns it (and true) when found. Used by the
// `r1.tui.focus_lane` handler to validate that a lane exists before
// generating key presses.
func FindByStableID(root A11yNode, id string) (A11yNode, bool) {
	if root.StableID == id {
		return root, true
	}
	for _, c := range root.Children {
		if hit, ok := FindByStableID(c, id); ok {
			return hit, true
		}
	}
	return A11yNode{}, false
}

// FindByRoleAndName searches for the first node whose Role matches
// roleEq exactly AND whose Name contains namePart (case-sensitive
// substring). This is the predicate behind feature-file steps like
// `When I click the button with name "Send"`.
func FindByRoleAndName(root A11yNode, roleEq, namePart string) (A11yNode, bool) {
	stack := []A11yNode{root}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n.Role == roleEq && containsSubstring(n.Name, namePart) {
			return n, true
		}
		// Push children in reverse so DFS visits left-to-right.
		for i := len(n.Children) - 1; i >= 0; i-- {
			stack = append(stack, n.Children[i])
		}
	}
	return A11yNode{}, false
}

// containsSubstring is the case-sensitive substring helper. Inlined to
// avoid the strings import for one call site, but extracted so a
// future case-insensitive mode can be a one-line change.
func containsSubstring(haystack, needle string) bool {
	return indexOfSubstring(haystack, needle) >= 0
}

// indexOfSubstring is the byte-level Boyer-Moore-Horspool would be
// overkill for a UI tree; this is the standard linear scan. Returns -1
// if not found, the byte offset otherwise.
func indexOfSubstring(haystack, needle string) int {
	if needle == "" {
		return 0
	}
	if len(needle) > len(haystack) {
		return -1
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
