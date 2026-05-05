package lanes

import (
	"time"
)

// --- tea.Msg types ---
//
// Concrete message types fanned into the Update loop via the
// Model.sub channel. See specs/tui-lanes.md §"tea.Msg types (concrete)"
// for the canonical shape; field names below match the spec verbatim.

// laneTickMsg is the single fan-in message. The producer goroutine
// (runProducer) batches upstream events and emits one of these every
// 200–300 ms per lane that changed.
//
// LaneID == "" is the documented sentinel meaning "producer shut down";
// Update treats it as a no-op (do not re-arm the cmd).
type laneTickMsg struct {
	LaneID   string
	Activity string // single-line; truncated by renderer
	Tokens   int
	CostUSD  float64
	Status   LaneStatus
	Model    string // e.g. "haiku-4.5"
	Elapsed  time.Duration
	Err      string // empty unless StatusErrored
}

// laneStartMsg announces a new lane. The producer emits one immediately
// (bypassing the coalesce window) so the UI can render the box on first
// frame.
type laneStartMsg struct {
	LaneID    string
	Title     string // human label, e.g. "memory-recall"
	Role      string // stance/lobe role
	StartedAt time.Time
}

// laneEndMsg announces a terminal transition. Final cost and tokens are
// captured so the lane can stop spinning and freeze its footer.
type laneEndMsg struct {
	LaneID  string
	Final   LaneStatus // Done | Errored | Cancelled
	CostUSD float64
	Tokens  int
}

// laneListMsg is the initial replay-on-subscribe payload. The Transport
// emits one as the first message and re-emits it on reconnect.
type laneListMsg struct {
	Lanes []LaneSnapshot
}

// LaneSnapshot is a value-copy of a lane suitable for crossing the
// transport boundary. It mirrors Lane's exported fields except for Dirty
// (which is renderer-only).
type LaneSnapshot struct {
	ID        string
	Title     string
	Role      string
	Status    LaneStatus
	Activity  string
	Tokens    int
	CostUSD   float64
	Model     string
	Elapsed   time.Duration
	Err       string
	StartedAt time.Time
	EndedAt   time.Time
}

// killAckMsg confirms the daemon accepted (or rejected) a kill request.
// Empty Err means accepted; non-empty Err carries the daemon's reason.
type killAckMsg struct {
	LaneID string
	Err    string
}

// budgetMsg updates the spent / limit pair shown in the status bar.
type budgetMsg struct {
	SpentUSD float64
	LimitUSD float64
}

// windowChangedMsg is an internal synthetic message emitted by the
// producer when bubblelayout reports a per-cell box size change. It lets
// the cache invalidate per-lane width entries without coupling to
// tea.WindowSizeMsg directly. See spec checklist item 13.
type windowChangedMsg struct {
	Width  int
	Height int
}

// --- Lane value type ---
//
// Per specs/tui-lanes.md §"Model struct", a Lane carries a Dirty bit that
// is flipped on every field write. The renderer cache uses this to decide
// whether to repaint the lane on the next View.

// Lane is the panel-internal mutable record for one lane. Owned by the
// Model; mutated only inside Update (or via the Set* helpers below, which
// assume the caller already holds Model.mu when called from outside the
// loop — but practically, the Send* helpers pass typed messages that
// Update applies).
type Lane struct {
	ID        string
	Title     string
	Role      string
	Status    LaneStatus
	Activity  string
	Tokens    int
	CostUSD   float64
	Model     string
	Elapsed   time.Duration
	Err       string
	StartedAt time.Time
	EndedAt   time.Time

	// Dirty is set by every setter on this struct and consumed by the
	// renderer cache (see lanes_cache.go). View clears it after a
	// successful re-render.
	Dirty bool
}

// SetStatus updates Status and flips Dirty. Use instead of direct field
// write so the cache invalidation contract (§Render-Cache Contract item 1
// + 3) is preserved.
func (l *Lane) SetStatus(s LaneStatus) {
	if l.Status == s {
		return
	}
	l.Status = s
	l.Dirty = true
}

// SetActivity updates the single-line activity text and flips Dirty.
func (l *Lane) SetActivity(a string) {
	if l.Activity == a {
		return
	}
	l.Activity = a
	l.Dirty = true
}

// SetTokens updates Tokens and flips Dirty.
func (l *Lane) SetTokens(t int) {
	if l.Tokens == t {
		return
	}
	l.Tokens = t
	l.Dirty = true
}

// SetCost updates CostUSD and flips Dirty.
func (l *Lane) SetCost(c float64) {
	if l.CostUSD == c {
		return
	}
	l.CostUSD = c
	l.Dirty = true
}

// SetModel updates the model name and flips Dirty.
func (l *Lane) SetModel(m string) {
	if l.Model == m {
		return
	}
	l.Model = m
	l.Dirty = true
}

// SetElapsed updates the elapsed duration and flips Dirty.
func (l *Lane) SetElapsed(d time.Duration) {
	if l.Elapsed == d {
		return
	}
	l.Elapsed = d
	l.Dirty = true
}

// SetErr updates Err and flips Dirty.
func (l *Lane) SetErr(s string) {
	if l.Err == s {
		return
	}
	l.Err = s
	l.Dirty = true
}

// SetTitle updates Title and flips Dirty. Rare — used on reconnect when
// a snapshot revises the human label.
func (l *Lane) SetTitle(t string) {
	if l.Title == t {
		return
	}
	l.Title = t
	l.Dirty = true
}

// SetRole updates Role and flips Dirty.
func (l *Lane) SetRole(r string) {
	if l.Role == r {
		return
	}
	l.Role = r
	l.Dirty = true
}

// SetEndedAt updates EndedAt and flips Dirty.
func (l *Lane) SetEndedAt(t time.Time) {
	if l.EndedAt.Equal(t) {
		return
	}
	l.EndedAt = t
	l.Dirty = true
}
