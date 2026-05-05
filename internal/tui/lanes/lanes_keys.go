package lanes

import "charm.land/bubbles/v2/key"

// keyMap is the canonical set of bubbles/v2/key bindings consumed by
// the panel's Update loop. Implements help.KeyMap so the help overlay
// (spec checklist item 25) can render the bindings without duplicating
// labels.
//
// Per specs/tui-lanes.md §"Keybinding Map" and checklist item 20.
type keyMap struct {
	// Cursor / navigation in overview.
	Up       key.Binding // k / ↑
	Down     key.Binding // j / ↓
	Tab      key.Binding // tab — cycle forward
	ShiftTab key.Binding // shift+tab — cycle backward

	// Jump-to-lane number row (1..9).
	Jump key.Binding

	// Mode transitions.
	Enter key.Binding // overview → focus
	Esc   key.Binding // focus → overview, also closes overlays

	// Kill flows.
	Kill        key.Binding // x (alias k in overview)
	KillAll     key.Binding // K
	ConfirmYes  key.Binding // y (kill confirm)
	ForceRender key.Binding // r — drop cache (debug)

	// Help overlay.
	Help key.Binding // ?

	// Quit.
	Quit key.Binding // q / ctrl+c

	// Toggle panel visibility (compose hook).
	Toggle key.Binding // L
}

// defaultKeyMap returns the panel's default keybindings, mirroring
// specs/tui-lanes.md §"Keybinding Map" verbatim. Help text matches
// the spec table so `?` renders the spec rows directly.
func defaultKeyMap() keyMap {
	return keyMap{
		Up: key.NewBinding(
			key.WithKeys("k", "up"),
			key.WithHelp("↑/k", "cursor up"),
		),
		Down: key.NewBinding(
			key.WithKeys("j", "down"),
			key.WithHelp("↓/j", "cursor down"),
		),
		Tab: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp("tab", "cycle forward"),
		),
		ShiftTab: key.NewBinding(
			key.WithKeys("shift+tab"),
			key.WithHelp("shift+tab", "cycle backward"),
		),
		Jump: key.NewBinding(
			key.WithKeys("1", "2", "3", "4", "5", "6", "7", "8", "9"),
			key.WithHelp("1..9", "jump to lane"),
		),
		Enter: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "focus mode"),
		),
		Esc: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "back / close"),
		),
		Kill: key.NewBinding(
			key.WithKeys("x"),
			key.WithHelp("x", "kill lane"),
		),
		KillAll: key.NewBinding(
			key.WithKeys("K"),
			key.WithHelp("K", "kill all"),
		),
		ConfirmYes: key.NewBinding(
			key.WithKeys("y"),
			key.WithHelp("y", "confirm"),
		),
		ForceRender: key.NewBinding(
			key.WithKeys("r"),
			key.WithHelp("r", "force re-render"),
		),
		Help: key.NewBinding(
			key.WithKeys("?"),
			key.WithHelp("?", "toggle help"),
		),
		Quit: key.NewBinding(
			key.WithKeys("q", "ctrl+c"),
			key.WithHelp("q", "quit"),
		),
		Toggle: key.NewBinding(
			key.WithKeys("L"),
			key.WithHelp("L", "toggle panel"),
		),
	}
}

// ShortHelp implements help.KeyMap. Returns the bindings shown in the
// one-line short help.
func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Enter, k.Esc, k.Kill, k.Help, k.Quit}
}

// FullHelp implements help.KeyMap. Returns column-grouped bindings
// shown in the expanded help overlay. Three columns: nav, modes,
// actions.
func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.Tab, k.ShiftTab, k.Jump},
		{k.Enter, k.Esc, k.Toggle, k.ForceRender},
		{k.Kill, k.KillAll, k.ConfirmYes, k.Help, k.Quit},
	}
}
