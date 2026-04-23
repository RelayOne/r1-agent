package streamjson

import (
	"bytes"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestCostReporterEmitsPeriodically verifies StartCostReporter emits
// at least one stoke.cost event per change tick. We use a short
// interval (10ms) and a shared counter that increments between reads.
func TestCostReporterEmitsPeriodically(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	var total uint64
	provider := func() float64 {
		mu.Lock()
		defer mu.Unlock()
		atomic.AddUint64(&total, 1)
		return float64(atomic.LoadUint64(&total)) / 100.0
	}
	tl := NewTwoLane(&buf, true)
	stop := StartCostReporter(tl, provider, 10*time.Millisecond)
	time.Sleep(75 * time.Millisecond)
	stop()
	tl.Drain(time.Second)

	out := buf.String()
	if !strings.Contains(out, `"subtype":"stoke.cost"`) {
		t.Fatalf("expected stoke.cost events in output, got %q", out)
	}
	// Tick-driven emissions should contain the _stoke.dev/total_usd
	// field with the evolving total. Exact count depends on timing
	// but we expect at least one.
	if !strings.Contains(out, `"_stoke.dev/total_usd"`) {
		t.Errorf("expected total_usd in stoke.cost: %q", out)
	}
}

// TestCostReporterNoOpWhenDisabled verifies the reporter is a no-op
// when the emitter is disabled (CloudSwarm-mode off). Stop returns
// cleanly and nothing was written.
func TestCostReporterNoOpWhenDisabled(t *testing.T) {
	var buf bytes.Buffer
	tl := NewTwoLane(&buf, false) // disabled
	stop := StartCostReporter(tl, func() float64 { return 0.42 }, time.Millisecond)
	time.Sleep(10 * time.Millisecond)
	stop()
	if buf.Len() != 0 {
		t.Errorf("disabled emitter wrote %d bytes; want 0", buf.Len())
	}
}

// TestCostReporterNilEmitter locks the nil-safe behavior so defer
// stop() never panics when CloudSwarm mode is off.
func TestCostReporterNilEmitter(t *testing.T) {
	stop := StartCostReporter(nil, func() float64 { return 1.0 }, time.Millisecond)
	if stop == nil {
		t.Fatal("expected non-nil stop from nil emitter")
	}
	stop() // must not panic
}

// TestCostReporterStopIsIdempotent verifies calling stop() twice does
// not deadlock or panic.
func TestCostReporterStopIsIdempotent(t *testing.T) {
	var buf bytes.Buffer
	tl := NewTwoLane(&buf, true)
	stop := StartCostReporter(tl, func() float64 { return 0.5 }, 10*time.Millisecond)
	stop()
	stop() // second call should be a no-op, not hang
}

// TestCostReporterSkipsUnchanged verifies no event is emitted when
// the cost total hasn't moved since the previous tick.
func TestCostReporterSkipsUnchanged(t *testing.T) {
	var buf bytes.Buffer
	tl := NewTwoLane(&buf, true)
	// Static provider — same value on every call after the first.
	stop := StartCostReporter(tl, func() float64 { return 0.12 }, 10*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	stop()
	tl.Drain(time.Second)
	out := buf.String()
	// Static provider should produce exactly one emission (the first
	// tick), with subsequent ticks deduped and no terminal emit.
	n := strings.Count(out, `"subtype":"stoke.cost"`)
	if n > 1 {
		t.Errorf("static provider produced %d cost lines; want <=1", n)
	}
}
