// tui_server.go — surface contribution from spec 8 for the 4 r1.tui.* tools.
//
// The MCP-facing handler is a thin delegator: it parses inputs from JSON-RPC
// arguments and forwards to internal/tui.Shim (defined in
// internal/tui/teatest_shim.go, ships with item 11). Per the spec §3
// "Existing Patterns to Follow" rule, every error mapped to the stokerr/
// taxonomy at the boundary; raw Go errors are never returned.
//
// The 4 tools per §4.8:
//   - r1.tui.press_key    -> Shim.PressKey
//   - r1.tui.snapshot     -> Shim.Snapshot
//   - r1.tui.get_model    -> Shim.GetModel
//   - r1.tui.focus_lane   -> Shim.FocusLane
//
// Because the catalog declarations (r1_server_catalog.go) drive
// tools/list and the handler dispatch table at the daemon boundary,
// this file declares ONLY the names + the helper TUIToolNames(); the
// concrete handler bodies live with the daemon (internal/r1d) where
// they have access to the singleton Shim instance.
package mcp

// TUIToolNames returns the canonical names of the 4 TUI tools per spec 8
// §4.8. Used by the lint at tools/lint-view-without-api/ to verify every
// Bubble Tea model with an A11yEmitter implementation references each
// declared TUI tool.
func TUIToolNames() []string {
	return []string{
		"r1.tui.press_key",
		"r1.tui.snapshot",
		"r1.tui.get_model",
		"r1.tui.focus_lane",
	}
}
