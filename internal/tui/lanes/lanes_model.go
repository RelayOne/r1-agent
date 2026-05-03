package lanes

import (
	"context"
	"sync"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/winder/bubblelayout"
)

// layoutMode is the adaptive layout decision returned by decideMode.
//
// See specs/tui-lanes.md §"Layout algorithm" for the decision rules.
type layoutMode int8

const (
	// modeEmpty is rendered when the model has zero lanes. The status
	// bar is still shown.
	modeEmpty layoutMode = iota

	// modeStack is the narrow-terminal vertical list. cols == 1.
	modeStack

	// modeColumns is the wide-terminal grid (up to COLS_MAX columns).
	modeColumns

	// modeFocus is the 65/35 main+peers split entered via 'enter' on a
	// lane in overview mode. cols is irrelevant; renderer hardcodes 2
	// regions (focused + peer stack).
	modeFocus
)

// String returns a stable name for the layout mode (debug + tests).
func (m layoutMode) String() string {
	switch m {
	case modeEmpty:
		return "empty"
	case modeStack:
		return "stack"
	case modeColumns:
		return "columns"
	case modeFocus:
		return "focus"
	default:
		return "unknown"
	}
}

// Layout constants from the spec's decideMode pseudo-code. Documented
// here so unit tests in lanes_layout_test.go can reference the same
// numbers.
const (
	// LANE_MIN_WIDTH is the smallest cell width that still fits a lane
	// box (border + 2-col padding + ~28 cols of content).
	LANE_MIN_WIDTH = 32

	// COLS_MAX caps the grid at 4 columns; 5+ degrades readability.
	COLS_MAX = 4

	// PRODUCER_TICK is the coalesce window for runProducer. Spec
	// guarantees at most ~5 Hz per lane.
	PRODUCER_TICK_MS = 250
)

// Model is the Bubble Tea v2 model for the lanes panel.
//
// Field grouping matches specs/tui-lanes.md §"Model struct" verbatim;
// any reordering or rename here also updates that section. The mu mutex
// guards lanes / laneIndex / focusID / cursor / mode / cache against
// writes from outside the Update loop (Send* helpers — see
// specs/tui-lanes.md §"Existing Patterns to Follow"). Do NOT hold mu
// across a tea.Cmd invocation (spec §"Boundaries — What NOT To Do").
type Model struct {
	mu sync.Mutex

	// --- Identity / config ---
	sessionID string
	transport Transport
	// sub is the single fan-in channel from runProducer. Per spec
	// §"waitForLaneTick — the canonical realtime cmd", every
	// streaming message type (laneTickMsg, laneStartMsg, laneEndMsg,
	// laneListMsg, killAckMsg, budgetMsg) flows through this one
	// channel and is read by a single re-armed waitForLaneTick cmd.
	// Carrying tea.Msg (rather than a typed laneTickMsg) lets the
	// same channel carry every variant; the spec's "map[laneID]
	// laneTickMsg" coalescer wording in §"Subscription Wiring"
	// describes the producer's INTERNAL queue, not the channel
	// element type.
	sub    chan tea.Msg
	cancel context.CancelFunc

	// --- Lane state (ordered by createdAt then LaneID) ---
	lanes     []*Lane
	laneIndex map[string]int

	// --- Layout ---
	width   int
	height  int
	mode    layoutMode
	focusID string
	cursor  int
	cols    int

	// --- Render cache ---
	cache *renderCache

	// --- bubblelayout (driven by tea.WindowSizeMsg) ---
	layout bubblelayout.BubbleLayout

	// --- Components ---
	spinner spinner.Model
	budget  progress.Model
	vp      viewport.Model
	help    help.Model
	keys    keyMap

	// --- Modal state ---
	// confirmKill carries the laneID awaiting "y" confirmation. Empty
	// means no kill modal active. Any non-"y" key cancels.
	confirmKill string

	// confirmAll is set when 'K' is pressed; awaits double "y" "y".
	confirmAll bool

	// helpOpen tracks the "?" toggle for the help overlay.
	helpOpen bool

	// --- Aggregate counters (status bar) ---
	totalCost    float64
	totalTurns   int
	totalLanes   int
	currentModel string
	budgetLimit  float64
}

// keyMap and renderCache are concrete types defined in their own
// files (lanes_keys.go and lanes_cache.go respectively). They land in
// the same compilation unit so the Model struct above can name them
// directly.
