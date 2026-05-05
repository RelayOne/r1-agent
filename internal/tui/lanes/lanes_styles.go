package lanes

import (
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"
)

// LaneStatus is the panel-internal lifecycle enum used by the renderer.
//
// It is an int8 (cheap to compare, switch-friendly) and carries glyph and
// color metadata via the package-level tables below. Conversions to and
// from the wire-format hub.LaneStatus string enum live in
// lanes_transport.go (StatusFromHub / StatusToHub).
//
// See specs/tui-lanes.md §"Status enum" for the canonical ordering.
type LaneStatus int8

// Lane lifecycle values. The order is fixed by spec §"Status enum"; do
// NOT reorder — the iota values are referenced by glyph and style tables.
const (
	StatusPending LaneStatus = iota
	StatusRunning
	StatusBlocked
	StatusDone
	StatusErrored
	StatusCancelled
)

// String returns the human-readable lowercase name for the status. Used by
// log lines and aggregate status counts in the status bar.
func (s LaneStatus) String() string {
	switch s {
	case StatusPending:
		return "pending"
	case StatusRunning:
		return "running"
	case StatusBlocked:
		return "blocked"
	case StatusDone:
		return "done"
	case StatusErrored:
		return "errored"
	case StatusCancelled:
		return "cancelled"
	default:
		return "unknown"
	}
}

// IsTerminal reports whether the status is one of the three terminal
// states (done, errored, cancelled). Mirrors hub.LaneStatus.IsTerminal.
func (s LaneStatus) IsTerminal() bool {
	return s == StatusDone || s == StatusErrored || s == StatusCancelled
}

// glyphTable maps each LaneStatus to its single-cell glyph.
//
// Per specs/tui-lanes.md §"Implementation Checklist" item 3:
//
//	StatusPending   = "·"
//	StatusRunning   = "▸"
//	StatusBlocked   = "⏸"
//	StatusDone      = "✓"
//	StatusErrored   = "✗"
//	StatusCancelled = "⊘"
//
// Glyphs are paired with color so that NO_COLOR / TERM=dumb terminals
// remain unambiguous (accessibility — see package doc).
var glyphTable = [...]string{
	StatusPending:   "·",
	StatusRunning:   "▸",
	StatusBlocked:   "⏸",
	StatusDone:      "✓",
	StatusErrored:   "✗",
	StatusCancelled: "⊘",
}

// Glyph returns the single-cell status glyph. Out-of-range statuses
// degrade to a neutral "?" rather than panicking.
func (s LaneStatus) Glyph() string {
	if int(s) < 0 || int(s) >= len(glyphTable) {
		return "?"
	}
	return glyphTable[s]
}

// Tokyo Night palette per RT-TUI-LANES (specs/tui-lanes.md §Risks &
// Mitigations note). compat.AdaptiveColor lets lipgloss v2 honor
// NO_COLOR and dumb terminals automatically.
var (
	colorPending   = compat.AdaptiveColor{Light: lipgloss.Color("#6c7086"), Dark: lipgloss.Color("#9aa0a6")}
	colorRunning   = compat.AdaptiveColor{Light: lipgloss.Color("#1a73e8"), Dark: lipgloss.Color("#7dcfff")}
	colorBlocked   = compat.AdaptiveColor{Light: lipgloss.Color("#d97706"), Dark: lipgloss.Color("#e0af68")}
	colorDone      = compat.AdaptiveColor{Light: lipgloss.Color("#16a34a"), Dark: lipgloss.Color("#9ece6a")}
	colorErrored   = compat.AdaptiveColor{Light: lipgloss.Color("#dc2626"), Dark: lipgloss.Color("#f7768e")}
	colorCancelled = compat.AdaptiveColor{Light: lipgloss.Color("#6b7280"), Dark: lipgloss.Color("#565f89")}
)

// statusColor returns the AdaptiveColor for a status. Used by both the
// glyph and the lane border weight.
func statusColor(s LaneStatus) compat.AdaptiveColor {
	switch s {
	case StatusPending:
		return colorPending
	case StatusRunning:
		return colorRunning
	case StatusBlocked:
		return colorBlocked
	case StatusDone:
		return colorDone
	case StatusErrored:
		return colorErrored
	case StatusCancelled:
		return colorCancelled
	default:
		return colorPending
	}
}

// Package-level styles. Defined at package init so they survive the
// lifetime of the program; lipgloss styles are immutable so this is safe
// for concurrent reads. Per specs/tui-lanes.md §"Existing Patterns" we
// keep v2 styles in this file (lanes_styles.go).
var (
	// laneBoxStyle is the bordered box around an unfocused lane in
	// overview mode. Border color is overridden per lane based on status.
	laneBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder(), true).
			Padding(0, 1)

	// laneBoxFocusedStyle is the bordered box for a focused lane (cursor
	// or focus mode main pane). Heavier border weight to disambiguate.
	laneBoxFocusedStyle = lipgloss.NewStyle().
				Border(lipgloss.ThickBorder(), true).
				Padding(0, 1)

	// laneTitleStyle styles the lane title row inside the box.
	laneTitleStyle = lipgloss.NewStyle().Bold(true)

	// laneActivityStyle styles the single-line activity row.
	laneActivityStyle = lipgloss.NewStyle()

	// laneFooterStyle styles the footer row (tokens / cost / model).
	laneFooterStyle = lipgloss.NewStyle().Faint(true)

	// statusBarStyle styles the always-on bottom status bar.
	statusBarStyle = lipgloss.NewStyle().Padding(0, 1)

	// modalStyle styles the kill-confirm overlay box.
	modalStyle = lipgloss.NewStyle().
			Border(lipgloss.DoubleBorder(), true).
			Padding(0, 2)
)
