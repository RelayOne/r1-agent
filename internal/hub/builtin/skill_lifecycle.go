// Package builtin — skill_lifecycle.go
//
// Reusable emitters for skill_loaded + skill_unloaded ledger nodes.
// SkillInjector.handle() in this package owns the load path; the
// unload path is intentionally a free function so callers in other
// packages (compactor, scope manager, operator-driven UI) can fire
// it without dragging the whole SkillInjector struct.
//
// Spec r1-server-ui-v2-event-rendering §5.2 + §5.3.
package builtin

import (
	"context"
	"encoding/json"

	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/ledger/nodes"
)

// EmitSkillUnloaded appends a SkillUnloaded ledger node. Reason MUST
// be one of "compactor_evicted" / "scope_exit" / "explicit_unload"
// (validated by nodes.SkillUnloaded.Validate). Returns the assigned
// NodeID for callers that want to thread it.
//
// The function is best-effort: an empty ledger pointer is a no-op
// (returns "" and nil) so feature-flagged callers can keep the same
// call site whether emission is wired in or not.
func EmitSkillUnloaded(ctx context.Context, led *ledger.Ledger, n *nodes.SkillUnloaded) (ledger.NodeID, error) {
	if led == nil || n == nil {
		return "", nil
	}
	if err := n.Validate(); err != nil {
		return "", err
	}
	payload, err := json.Marshal(n)
	if err != nil {
		return "", err
	}
	return led.AddNode(ctx, ledger.Node{
		Type:          n.NodeType(),
		SchemaVersion: n.SchemaVersion(),
		CreatedBy:     "builtin.skill_lifecycle",
		Content:       payload,
	})
}
