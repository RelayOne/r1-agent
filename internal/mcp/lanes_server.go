// lanes_server.go — surface-only contribution from spec 8.
//
// STATUS: NOTE — see plans/HANDOFF.md "Spec 3 — lanes-protocol".
//
// Spec 8 (agentic-test-harness) §12 item 4 calls for a lanes_server.go
// implementing the 5 r1.lanes.* tools (list, subscribe, get, kill, pin)
// with signatures matching spec 3 (lanes-protocol). The build-order rule
// is: spec 3 ships the canonical implementation BEFORE spec 8.
//
// In the canonical merge order this file is created by spec 3 in the
// branch build/lanes-protocol; spec 8's contribution is to declare the
// tools in the catalog (see r1_server_catalog.go r1LaneTools()) and rely
// on spec 3's handlers.
//
// This worktree was branched from a checkpoint that does NOT include the
// build/lanes-protocol merge, so the 5 lane handlers are absent here.
// The catalog declarations in r1_server_catalog.go remain authoritative
// for the wire (tools/list output) so external clients see the full §4
// surface even when the daemon back-end is incomplete.
//
// Once spec 3 merges, this file is replaced with the real handler set
// per build/lanes-protocol's lanes_server.go.
//
// LaneToolNames below is consumed by the lint at
// tools/lint-view-without-api/ to verify React/Bubble Tea/Tauri lane UI
// references each declared lane tool.
//
// See:
//   - specs/lanes-protocol.md (spec 3, build_order 3)
//   - plans/HANDOFF.md "Spec 3 — lanes-protocol — STATUS: done" (on the
//     working branch claude/w521-eliminate-stoke-leftovers-2026-05-02;
//     not yet merged into this checkpoint)
package mcp

// LaneToolNames returns the canonical names of the 5 lane tools per spec 8
// §4.2. Used by the lint at tools/lint-view-without-api/ to verify the web
// LaneSidebar component references each one.
func LaneToolNames() []string {
	return []string{
		"r1.lanes.list",
		"r1.lanes.subscribe",
		"r1.lanes.get",
		"r1.lanes.kill",
		"r1.lanes.pin",
	}
}
