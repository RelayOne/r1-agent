package lanes

import (
	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/winder/bubblelayout"
)

// Option configures a Model on construction. The functional-options
// pattern is the canonical Charm idiom and lets New stay backwards-
// compatible as we grow the panel surface (debug flush window, custom
// budget limit, alternate spinner shape, etc.).
//
// Per spec checklist item 7. Each option below documents its default
// value so callers know what the no-args path produces.
type Option func(*Model)

// WithBudgetLimit sets the per-session USD budget shown in the status
// bar. Default 0.0 (no limit; bar renders empty).
func WithBudgetLimit(usd float64) Option {
	return func(m *Model) { m.budgetLimit = usd }
}

// WithSpinner overrides the spinner shape. Default is spinner.Pulse
// per spec §"Implementation Checklist" item 7.
func WithSpinner(s spinner.Spinner) Option {
	return func(m *Model) { m.spinner = spinner.New(spinner.WithSpinner(s)) }
}

// New constructs a Model wired to the supplied Transport. The session
// id is treated as opaque by the panel and forwarded verbatim to
// transport.Subscribe / transport.Kill.
//
// Per specs/tui-lanes.md §"Implementation Checklist" item 7:
//
//	initialize spinner (Pulse), progress bar, viewport, help, keys,
//	cache.
//
// New does NOT spawn the producer goroutine; that happens in Init()
// (item 11). New does NOT dial the transport; the dial happens inside
// runProducer when the producer's context starts. This keeps New
// side-effect-free and test-friendly (no socket open during table
// tests).
func New(sessionID string, t Transport, opts ...Option) *Model {
	m := &Model{
		sessionID: sessionID,
		transport: t,
		// sub is buffered so the producer never blocks on a slow
		// frame. The buffer size matches PRODUCER_TICK_MS rate × 4
		// = 16 messages, plenty of headroom for one frame's worth of
		// coalesced ticks.
		sub:       make(chan tea.Msg, 16),
		laneIndex: make(map[string]int),
		// Default to overview with 1 column at 80x24 — the spec's
		// fallback for "no WindowSizeMsg yet". decideMode will fix
		// this on the first tea.WindowSizeMsg.
		width:  80,
		height: 24,
		mode:   modeEmpty,
		cols:   1,

		// Components (defaults documented in spec checklist item 7).
		spinner: spinner.New(spinner.WithSpinner(spinner.Pulse)),
		budget:  progress.New(progress.WithWidth(10), progress.WithoutPercentage()),
		vp:      viewport.New(),
		help:    help.New(),
		keys:    keyMap{},

		// Cache and bubblelayout — concrete impls land in their own
		// commits (items 20 + 13). New still constructs both so the
		// model invariant "non-nil cache" holds end-to-end.
		cache:  &renderCache{},
		layout: bubblelayout.New(),
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}
