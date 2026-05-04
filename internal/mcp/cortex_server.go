// cortex_server.go — surface-only contribution from spec 8.
//
// STATUS: PARTIAL — see plans/HANDOFF.md "Spec 1/2 — cortex-core/cortex-concerns".
//
// Spec 8 (agentic-test-harness) §12 item 5 calls for a cortex_server.go
// implementing the 5 r1.cortex.* tools (notes, publish, lobes_list,
// lobe_pause, lobe_resume) with signatures matching specs 1/2
// (cortex-core, cortex-concerns). The build-order rule is: specs 1/2
// ship the canonical implementation BEFORE spec 8.
//
// In the canonical merge order this file is created by spec 1/2 in the
// branches build/cortex-core / build/cortex-concerns; spec 8's
// contribution is to declare the tools in the catalog (see
// r1_server_catalog.go r1CortexTools()) and rely on those handlers.
//
// This worktree was branched from a checkpoint that does NOT include
// the cortex back-end merge, so the 5 cortex handlers are absent here.
// The catalog declarations in r1_server_catalog.go remain authoritative
// for the wire (tools/list output) so external clients see the full §4
// surface even when the daemon back-end is incomplete.
//
// CortexToolNames below is consumed by the lint at
// tools/lint-view-without-api/ to verify the React/Bubble Tea Workspace
// pane references each declared cortex tool.
//
// See:
//   - specs/cortex-core.md (spec 1, build_order 1)
//   - specs/cortex-concerns.md (spec 2, build_order 2)
//   - plans/HANDOFF.md cortex/lanes build resumption
package mcp

// CortexToolNames returns the canonical names of the 5 cortex tools per
// spec 8 §4.3. Used by the lint at tools/lint-view-without-api/ to verify
// the web Workspace pane and the TUI Workspace renderer reference each
// one.
func CortexToolNames() []string {
	return []string{
		"r1.cortex.notes",
		"r1.cortex.publish",
		"r1.cortex.lobes_list",
		"r1.cortex.lobe_pause",
		"r1.cortex.lobe_resume",
	}
}
