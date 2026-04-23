// CostDashboard is a live, rewritable terminal widget that subscribes to the
// hub.Bus model post-call events (hub.EventModelPostCall) and renders a
// per-model cost table to an io.Writer (typically stderr, so it coexists
// with `--output-format stream-json` on stdout).
//
// Rendering uses `\r` + ANSI cursor-up escape codes so the dashboard rewrites
// itself in place each tick instead of scrolling. A final Render() call on
// context cancellation prints the terminal state without the leading
// cursor-rewind, leaving the total on a fresh line.
//
// The dashboard is intentionally decoupled from the v1 `internal/costtrack`
// Tracker: it derives its rows entirely from bus events so it works in both
// v1 and v2 wiring. The event shape relied upon is hub.ModelEvent (Model,
// InputTokens, OutputTokens, CachedTokens, CostUSD).
//
// Wire-up: spec work-stoke.md TASK 3. Composition lives in cmd/stoke/main.go
// or cmd/r1-server/main.go — this file only implements the widget and its
// Start() subscription helper.

package tui

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/ericmacdougall/stoke/internal/hub"
)

// costRow accumulates per-model token + USD + call totals.
type costRow struct {
	PromptTok     int64
	CompletionTok int64
	CachedTok     int64
	USD           float64
	Calls         int64
}

// CostSnapshot is a point-in-time view of the dashboard state. Returned by
// Snapshot() for tests and programmatic callers. Maps are copies — mutating
// them after the call is safe.
type CostSnapshot struct {
	Total  float64
	Budget float64
	Rows   map[string]CostRow
}

// CostRow is the exported per-model snapshot row.
type CostRow struct {
	Model         string
	PromptTok     int64
	CompletionTok int64
	CachedTok     int64
	USD           float64
	Calls         int64
}

// CostDashboard is a thread-safe live cost widget.
type CostDashboard struct {
	bus    *hub.Bus
	writer io.Writer
	budget float64

	mu       sync.Mutex
	rows     map[string]*costRow
	total    float64
	lastDraw time.Time

	// Number of lines emitted by the previous Render call. Used to
	// build the ANSI cursor-rewind prefix for the next draw so the
	// widget overwrites itself in place.
	lastLines int

	// tickInterval is how often Start() redraws. Exported for tests
	// via WithTickInterval.
	tickInterval time.Duration

	// subID is set when Start() registers with the bus, so Stop-style
	// callers could unregister if we add one later.
	subID string

	// now and ansiEnabled are injectable for tests.
	now         func() time.Time
	ansiEnabled bool
}

// NewCostDashboard creates a dashboard that writes to writer and subscribes
// to bus. Budget may be zero (no budget line rendered).
func NewCostDashboard(bus *hub.Bus, writer io.Writer) *CostDashboard {
	return &CostDashboard{
		bus:          bus,
		writer:       writer,
		rows:         make(map[string]*costRow),
		tickInterval: 500 * time.Millisecond,
		now:          time.Now,
		ansiEnabled:  true,
	}
}

// WithBudget sets the session budget cap (USD). Zero disables the budget
// line in the header.
func (d *CostDashboard) WithBudget(budget float64) *CostDashboard {
	d.mu.Lock()
	d.budget = budget
	d.mu.Unlock()
	return d
}

// WithTickInterval overrides the redraw interval. For tests.
func (d *CostDashboard) WithTickInterval(interval time.Duration) *CostDashboard {
	d.mu.Lock()
	d.tickInterval = interval
	d.mu.Unlock()
	return d
}

// WithANSI toggles ANSI cursor-rewind escape codes. Tests set this false
// so Render produces stable byte output.
func (d *CostDashboard) WithANSI(enabled bool) *CostDashboard {
	d.mu.Lock()
	d.ansiEnabled = enabled
	d.mu.Unlock()
	return d
}

// Start subscribes the dashboard to the bus and launches a ticker goroutine
// that redraws at the configured interval until ctx is done. Safe to call
// at most once per dashboard.
func (d *CostDashboard) Start(ctx context.Context) {
	if d.bus != nil {
		d.subID = fmt.Sprintf("tui.cost_dashboard.%d", d.now().UnixNano())
		d.bus.Register(hub.Subscriber{
			ID:       d.subID,
			Events:   []hub.EventType{hub.EventModelPostCall},
			Mode:     hub.ModeObserve,
			Priority: 9500,
			Handler:  d.handle,
		})
	}

	go d.runTicker(ctx)
}

// handle is the bus handler for hub.EventModelPostCall.
func (d *CostDashboard) handle(ctx context.Context, ev *hub.Event) *hub.HookResponse {
	if ev == nil || ev.Model == nil {
		return &hub.HookResponse{Decision: hub.Allow}
	}
	d.Ingest(ev.Model)
	return &hub.HookResponse{Decision: hub.Allow}
}

// Ingest folds a single ModelEvent into the dashboard. Exposed separately
// from the bus so tests can drive the dashboard without a live bus, and so
// callers that already have a *hub.ModelEvent in hand can update directly.
func (d *CostDashboard) Ingest(m *hub.ModelEvent) {
	if m == nil || m.Model == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	row, ok := d.rows[m.Model]
	if !ok {
		row = &costRow{}
		d.rows[m.Model] = row
	}
	row.PromptTok += int64(m.InputTokens)
	row.CompletionTok += int64(m.OutputTokens)
	row.CachedTok += int64(m.CachedTokens)
	row.USD += m.CostUSD
	row.Calls++
	d.total += m.CostUSD
}

// Snapshot returns a copy of the current dashboard state. Safe for
// concurrent callers; the returned maps are independent of internal state.
func (d *CostDashboard) Snapshot() CostSnapshot {
	d.mu.Lock()
	defer d.mu.Unlock()

	out := CostSnapshot{
		Total:  d.total,
		Budget: d.budget,
		Rows:   make(map[string]CostRow, len(d.rows)),
	}
	for model, r := range d.rows {
		out.Rows[model] = CostRow{
			Model:         model,
			PromptTok:     r.PromptTok,
			CompletionTok: r.CompletionTok,
			CachedTok:     r.CachedTok,
			USD:           r.USD,
			Calls:         r.Calls,
		}
	}
	return out
}

// Render writes the current dashboard state to the configured writer. If
// ANSI is enabled and this is not the first render, it first emits a
// cursor-rewind sequence (`\r` + ESC[A * lastLines) so the output overwrites
// the previous draw in place.
func (d *CostDashboard) Render() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.renderLocked(false)
}

// Final is equivalent to Render but does not issue the cursor-rewind
// prefix, so it leaves a permanent copy of the final state. Callers should
// invoke this once after the dashboard's context is cancelled.
func (d *CostDashboard) Final() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.renderLocked(true)
}

func (d *CostDashboard) renderLocked(final bool) {
	if d.writer == nil {
		return
	}

	var b strings.Builder

	// Rewind to overwrite the previous draw. Skipped on first draw
	// (lastLines == 0) and on Final() so the terminal keeps the
	// final state.
	if d.ansiEnabled && !final && d.lastLines > 0 {
		b.WriteString("\r")
		for i := 0; i < d.lastLines; i++ {
			b.WriteString("\x1b[A\x1b[2K")
		}
	}

	// Header.
	if d.budget > 0 {
		fmt.Fprintf(&b, "Cost — session total $%0.2f / $%0.2f budget\n",
			d.total, d.budget)
	} else {
		fmt.Fprintf(&b, "Cost — session total $%0.2f\n", d.total)
	}

	// Body — tab-aligned columns via text/tabwriter.
	var tbuf strings.Builder
	tw := tabwriter.NewWriter(&tbuf, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  model\tin-tok\tout-tok\tusd\tcalls")
	for _, r := range d.sortedRowsLocked() {
		fmt.Fprintf(tw, "  %s\t%s\t%s\t$%0.2f\t%d\n",
			r.Model,
			formatInt(r.PromptTok),
			formatInt(r.CompletionTok),
			r.USD,
			r.Calls,
		)
	}
	_ = tw.Flush()
	b.WriteString(tbuf.String())

	out := b.String()
	_, _ = io.WriteString(d.writer, out)

	d.lastDraw = d.now()
	d.lastLines = strings.Count(out, "\n")
	// When we injected a rewind prefix, the leading `\r\x1b[A...`
	// sequence only repositions — it doesn't emit newlines. The
	// line count we care about for the next rewind is the body
	// only, which is already what strings.Count above returned
	// (the prefix has no \n).
}

func (d *CostDashboard) sortedRowsLocked() []CostRow {
	rows := make([]CostRow, 0, len(d.rows))
	for model, r := range d.rows {
		rows = append(rows, CostRow{
			Model:         model,
			PromptTok:     r.PromptTok,
			CompletionTok: r.CompletionTok,
			CachedTok:     r.CachedTok,
			USD:           r.USD,
			Calls:         r.Calls,
		})
	}
	// Sort by USD descending, model ascending as tiebreak. Deterministic.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].USD != rows[j].USD {
			return rows[i].USD > rows[j].USD
		}
		return rows[i].Model < rows[j].Model
	})
	return rows
}

// runTicker drives periodic Render() calls until ctx is cancelled, then
// emits one Final() draw so the terminal keeps the last state visible.
func (d *CostDashboard) runTicker(ctx context.Context) {
	d.mu.Lock()
	interval := d.tickInterval
	d.mu.Unlock()

	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	t := time.NewTicker(interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			d.Final()
			return
		case <-t.C:
			d.Render()
		}
	}
}

// formatInt renders an int64 with thousands separators — matches the
// spec's "412,388" shape in the render table.
func formatInt(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		if neg {
			return "-" + s
		}
		return s
	}
	// Insert commas every 3 digits from the right.
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
		if len(s) > pre {
			b.WriteByte(',')
		}
	}
	for i := pre; i < len(s); i += 3 {
		b.WriteString(s[i : i+3])
		if i+3 < len(s) {
			b.WriteByte(',')
		}
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}
