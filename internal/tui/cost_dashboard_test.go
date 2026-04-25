package tui

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1-agent/internal/hub"
)

// syncWriter wraps bytes.Buffer for safe concurrent writes during async
// ticker tests.
type syncWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// stripANSI removes ANSI escape sequences so assertions can focus on the
// visible text content of a render.
var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

func stripANSI(s string) string {
	return ansiRE.ReplaceAllString(s, "")
}

// TestCostDashboard_AccumulatesFromBus drives three synthetic
// EventModelPostCall events through a real hub.Bus and asserts that
// Snapshot() returns correct per-model rows.
func TestCostDashboard_AccumulatesFromBus(t *testing.T) {
	bus := hub.New()
	var w bytes.Buffer
	d := NewCostDashboard(bus, &w).WithANSI(false).WithTickInterval(time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Start(ctx)

	events := []*hub.ModelEvent{
		{Model: "claude-sonnet-4-6", InputTokens: 412388, OutputTokens: 83202, CostUSD: 2.50},
		{Model: "claude-opus-4-6", InputTokens: 21000, OutputTokens: 12400, CostUSD: 1.20},
		{Model: "claude-sonnet-4-6", InputTokens: 10000, OutputTokens: 5000, CostUSD: 0.50},
	}
	for _, m := range events {
		bus.Emit(ctx, &hub.Event{Type: hub.EventModelPostCall, Model: m})
	}

	// Observe-mode handlers run asynchronously. Poll for settlement
	// rather than racing the goroutines.
	var snap CostSnapshot
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snap = d.Snapshot()
		if snap.Total > 4.19 { // 2.50 + 1.20 + 0.50 = 4.20
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if got, want := len(snap.Rows), 2; got != want {
		t.Fatalf("rows: got %d models, want %d (%+v)", got, want, snap.Rows)
	}

	son := snap.Rows["claude-sonnet-4-6"]
	if son.PromptTok != 422388 {
		t.Errorf("sonnet prompt tok: got %d, want 422388", son.PromptTok)
	}
	if son.CompletionTok != 88202 {
		t.Errorf("sonnet completion tok: got %d, want 88202", son.CompletionTok)
	}
	if son.Calls != 2 {
		t.Errorf("sonnet calls: got %d, want 2", son.Calls)
	}
	if diff := son.USD - 3.0; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("sonnet usd: got %f, want 3.00", son.USD)
	}

	opus := snap.Rows["claude-opus-4-6"]
	if opus.Calls != 1 {
		t.Errorf("opus calls: got %d, want 1", opus.Calls)
	}
	if opus.PromptTok != 21000 {
		t.Errorf("opus prompt tok: got %d, want 21000", opus.PromptTok)
	}
}

// TestCostDashboard_TotalsAggregate drives multiple models through Ingest
// and asserts Snapshot.Total equals the sum of per-model USD.
func TestCostDashboard_TotalsAggregate(t *testing.T) {
	d := NewCostDashboard(nil, &bytes.Buffer{}).WithANSI(false)

	d.Ingest(&hub.ModelEvent{Model: "a", CostUSD: 1.25, InputTokens: 100, OutputTokens: 50})
	d.Ingest(&hub.ModelEvent{Model: "b", CostUSD: 2.75, InputTokens: 200, OutputTokens: 75})
	d.Ingest(&hub.ModelEvent{Model: "a", CostUSD: 0.50, InputTokens: 25, OutputTokens: 10})

	snap := d.Snapshot()

	want := 1.25 + 2.75 + 0.50
	if diff := snap.Total - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("total: got %f, want %f", snap.Total, want)
	}

	// Per-model USD summed must equal the Total.
	var perModelSum float64
	for _, r := range snap.Rows {
		perModelSum += r.USD
	}
	if diff := perModelSum - snap.Total; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("sum of per-model USD %f != Total %f", perModelSum, snap.Total)
	}

	// Nil event is a no-op.
	d.Ingest(nil)
	// Empty model name is a no-op.
	d.Ingest(&hub.ModelEvent{Model: "", CostUSD: 99})
	if d.Snapshot().Total != want {
		t.Errorf("nil/empty events mutated total")
	}
}

// TestCostDashboard_RenderStable ingests data, renders into a buffer, and
// asserts the ANSI-stripped output contains expected header and row tokens.
func TestCostDashboard_RenderStable(t *testing.T) {
	var buf bytes.Buffer
	d := NewCostDashboard(nil, &buf).WithANSI(false).WithBudget(15.00)

	d.Ingest(&hub.ModelEvent{
		Model: "claude-sonnet-4-6", InputTokens: 412388, OutputTokens: 83202, CostUSD: 2.50,
	})
	d.Ingest(&hub.ModelEvent{
		Model: "claude-sonnet-4-6", InputTokens: 0, OutputTokens: 0, CostUSD: 0.0,
	}) // call #2, USD unchanged
	d.Ingest(&hub.ModelEvent{
		Model: "claude-opus-4-6", InputTokens: 21000, OutputTokens: 12400, CostUSD: 1.20,
	})

	d.Render()

	out := stripANSI(buf.String())

	// Header: total + budget.
	if !strings.Contains(out, "Cost — session total $3.70 / $15.00 budget") {
		t.Errorf("header missing/wrong; got: %q", out)
	}

	// Column titles.
	for _, col := range []string{"model", "in-tok", "out-tok", "usd", "calls"} {
		if !strings.Contains(out, col) {
			t.Errorf("column %q missing; got:\n%s", col, out)
		}
	}

	// Comma-formatted numbers from the spec example.
	if !strings.Contains(out, "412,388") {
		t.Errorf("comma-formatted prompt tok missing; got:\n%s", out)
	}
	if !strings.Contains(out, "83,202") {
		t.Errorf("comma-formatted completion tok missing; got:\n%s", out)
	}

	// USD rendered as dollar amount.
	if !strings.Contains(out, "$2.50") {
		t.Errorf("$2.50 row missing; got:\n%s", out)
	}
	if !strings.Contains(out, "$1.20") {
		t.Errorf("$1.20 row missing; got:\n%s", out)
	}

	// Model names present.
	if !strings.Contains(out, "claude-sonnet-4-6") || !strings.Contains(out, "claude-opus-4-6") {
		t.Errorf("model names missing; got:\n%s", out)
	}

	// Deterministic row order: highest USD first (sonnet $2.50 before opus $1.20).
	sonIdx := strings.Index(out, "claude-sonnet-4-6")
	opusIdx := strings.Index(out, "claude-opus-4-6")
	if sonIdx < 0 || opusIdx < 0 || sonIdx > opusIdx {
		t.Errorf("rows not sorted by USD desc; sonnet=%d opus=%d\n%s", sonIdx, opusIdx, out)
	}

	// Second render with ANSI enabled emits a rewind prefix.
	buf.Reset()
	d2 := NewCostDashboard(nil, &buf).WithANSI(true)
	d2.Ingest(&hub.ModelEvent{Model: "m", InputTokens: 10, OutputTokens: 5, CostUSD: 0.1})
	d2.Render()
	firstLen := buf.Len()
	d2.Render()
	// Second render must have emitted a leading \r and at least one ESC[A.
	second := buf.Bytes()[firstLen:]
	if !bytes.Contains(second, []byte("\r")) || !bytes.Contains(second, []byte("\x1b[A")) {
		t.Errorf("second render missing rewind prefix; got: %q", string(second))
	}
}

// TestCostDashboard_RenderWithoutBudget covers the header branch where no
// budget is configured.
func TestCostDashboard_RenderWithoutBudget(t *testing.T) {
	var buf bytes.Buffer
	d := NewCostDashboard(nil, &buf).WithANSI(false)
	d.Ingest(&hub.ModelEvent{Model: "x", CostUSD: 0.42})
	d.Render()

	out := stripANSI(buf.String())
	if !strings.Contains(out, "Cost — session total $0.42") {
		t.Errorf("total header missing; got: %q", out)
	}
	if strings.Contains(out, "budget") {
		t.Errorf("budget mentioned with zero budget; got: %q", out)
	}
}

// TestCostDashboard_TickerStopsOnCtxCancel ensures Start's ticker goroutine
// exits cleanly when the context is cancelled and emits a final draw.
func TestCostDashboard_TickerStopsOnCtxCancel(t *testing.T) {
	bus := hub.New()
	var w syncWriter
	d := NewCostDashboard(bus, &w).WithANSI(false).WithTickInterval(10 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	d.Start(ctx)

	bus.Emit(ctx, &hub.Event{
		Type:  hub.EventModelPostCall,
		Model: &hub.ModelEvent{Model: "m", InputTokens: 1, OutputTokens: 1, CostUSD: 0.05},
	})

	// Let the ticker fire at least once.
	time.Sleep(30 * time.Millisecond)
	cancel()
	// Give the ticker goroutine time to observe ctx.Done and emit Final.
	time.Sleep(30 * time.Millisecond)

	out := w.String()
	if !strings.Contains(out, "$0.05") {
		t.Errorf("expected final draw to contain $0.05; got:\n%s", out)
	}
}
