// Package lanes implements the Bubble Tea v2 panel that renders Cortex
// "lanes" — per-Lobe / per-stance / per-task concurrent thinking threads —
// as first-class TUI primitives.
//
// Spec: specs/tui-lanes.md (companion spec: specs/lanes-protocol.md, which
// owns the wire-format envelope this package consumes via its Transport
// interface). See specs/tui-lanes.md §"Component model" for the message
// taxonomy, §"Layout algorithm" for the adaptive columns/stack/focus
// decision, and §"Render-cache contract" for the caching invariants.
//
// # Boundaries
//
// This package is the only consumer of Bubble Tea v2 in the r1-agent
// codebase as of writing. The legacy panel under internal/tui/renderer/
// and internal/tui/interactive.go pin Bubble Tea v1 / lipgloss v1; the
// two stacks coexist because the v2 module paths differ. Per
// specs/tui-lanes.md §"Boundaries", do NOT migrate the legacy panels here
// and do NOT import this package from v1 code beyond the thin Mount() hook
// described in the spec checklist.
//
// # Accessibility
//
// Every status colored cell is paired with a glyph (see lanes_styles.go
// glyph table) so users running with NO_COLOR=1 or TERM=dumb see
// unambiguous status. lipgloss handles NO_COLOR / TERM=dumb automatically;
// this package never branches on those env vars directly.
//
// # Concurrency model
//
// One producer goroutine fans upstream events into a single channel
// (Model.sub) at a 200–300 ms coalesce window, never more than ~5 Hz per
// lane. The Update loop re-arms a single waitForLaneTick tea.Cmd on every
// receive. State mutation lives in Update; m.mu serializes only the
// outside-Update writers (Send* helpers).
package lanes
