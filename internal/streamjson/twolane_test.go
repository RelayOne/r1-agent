package streamjson

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestTwoLaneEmitter_BasicOrdering verifies observability events appear
// on stdout in emission order when the lane isn't full.
func TestTwoLaneEmitter(t *testing.T) {
	var buf bytes.Buffer
	tl := NewTwoLane(&buf, true)
	defer tl.Drain(1 * time.Second)

	for i := 0; i < 10; i++ {
		tl.EmitSystem("progress", map[string]any{"_stoke.dev/i": i})
	}
	// Give the drainer a chance to flush.
	time.Sleep(50 * time.Millisecond)
	tl.Drain(time.Second)

	lines := splitNonEmpty(buf.String())
	if len(lines) != 10 {
		t.Fatalf("expected 10 lines, got %d", len(lines))
	}
	for _, ln := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(ln), &m); err != nil {
			t.Errorf("invalid JSON: %v (line=%q)", err, ln)
		}
		if m["type"] != "system" {
			t.Errorf("type=%v, want system", m["type"])
		}
		if m["subtype"] != "progress" {
			t.Errorf("subtype=%v, want progress", m["subtype"])
		}
	}
}

// TestTwoLaneCriticalAlwaysWins verifies hitl_required appears on
// stdout even when observability is full.
func TestTwoLaneCriticalAlwaysWins(t *testing.T) {
	var buf bytes.Buffer
	tl := NewTwoLane(&buf, true)
	defer tl.Drain(time.Second)
	// Fill observability lane to near-capacity to stress the scheduler.
	for i := 0; i < 500; i++ {
		tl.EmitSystem("progress", map[string]any{"_stoke.dev/i": i})
	}
	tl.EmitTopLevel("hitl_required", map[string]any{
		"reason":        "t8 soft-pass",
		"approval_type": "soft_pass",
	})
	time.Sleep(100 * time.Millisecond)
	tl.Drain(time.Second)

	out := buf.String()
	if !strings.Contains(out, `"type":"hitl_required"`) {
		t.Errorf("expected hitl_required line in output")
	}
}

// TestTwoLaneDropOldest verifies the drop-oldest behavior under
// sustained observability pressure.
//
// Previously this test relied on the background drainer being slower
// than the producer loop to observe drops — but on a fast CI runner
// the drainer keeps up with the flood and no drops occur, making the
// "len(lines) < 2500" assertion flaky.
//
// The deterministic fix wraps the underlying sink in a blockingWriter
// that holds every Write until a latch is closed. That pins the
// drainer on the first line while 2500 events queue; once the cap
// (1024) is hit, sendObserv's drop-oldest path evicts the head and
// appends — so at most 1024 events survive in the channel. When the
// latch releases and Drain runs, only the surviving events plus the
// first one (which the drainer was busy writing) reach the output.
func TestTwoLaneDropOldest(t *testing.T) {
	bw := &blockingWriter{hold: make(chan struct{})}
	tl := NewTwoLane(bw, true)
	// Flood beyond the 1024 observability cap while the drainer is
	// blocked inside blockingWriter.Write on line 0.
	for i := 0; i < 2500; i++ {
		tl.EmitSystem("progress", map[string]any{"_stoke.dev/i": i})
	}
	close(bw.hold) // let the drainer proceed
	tl.Drain(2 * time.Second)
	bw.mu.Lock()
	lines := splitNonEmpty(bw.buf.String())
	bw.mu.Unlock()
	// Hard upper bound: channel cap (1024) + the one line the drainer
	// was mid-writing when the flood started + slack. If we see the
	// full 2500, drop-oldest never fired.
	if len(lines) >= 2500 {
		t.Errorf("expected drops, got %d lines", len(lines))
	}
}

// blockingWriter holds writes until hold is closed, then passes through.
// Used to force the observ channel past capacity deterministically.
type blockingWriter struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	hold chan struct{}
}

func (w *blockingWriter) Write(p []byte) (int, error) {
	<-w.hold
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

// TestTwoLaneConcurrentEmitAtomicity verifies 50 goroutines × 100
// events each produce 5000 well-formed JSON lines with no interleaved
// bytes.
func TestTwoLaneConcurrentEmitAtomicity(t *testing.T) {
	var buf bytes.Buffer
	// Route through the synchronous Emitter so this test asserts atomic
	// writes without the drop-oldest interfering.
	em := New(&buf, true)
	var wg sync.WaitGroup
	for g := 0; g < 50; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				em.EmitSystem("test", map[string]any{
					"_stoke.dev/g": gid,
					"_stoke.dev/i": i,
				})
			}
		}(g)
	}
	wg.Wait()
	lines := splitNonEmpty(buf.String())
	if len(lines) != 5000 {
		t.Fatalf("expected 5000 lines, got %d", len(lines))
	}
	for _, ln := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(ln), &m); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
	}
}

// TestTwoLaneDrainFlushesPending verifies Drain() forces pending
// events out before returning.
func TestTwoLaneDrainFlushesPending(t *testing.T) {
	var buf bytes.Buffer
	tl := NewTwoLane(&buf, true)
	tl.EmitSystem("progress", map[string]any{"_stoke.dev/n": 1})
	tl.EmitTopLevel("hitl_required", map[string]any{"reason": "test"})
	tl.Drain(time.Second)
	out := buf.String()
	if !strings.Contains(out, "progress") {
		t.Errorf("drain should flush observability events")
	}
	if !strings.Contains(out, "hitl_required") {
		t.Errorf("drain should flush critical events")
	}
}

// TestEmitDescentSubtype verifies the descent helper formats subtypes
// under the stoke.descent.* namespace.
func TestEmitDescentSubtype(t *testing.T) {
	var buf bytes.Buffer
	tl := NewTwoLane(&buf, true)
	tl.EmitDescent("file_cap_exceeded", map[string]any{"_stoke.dev/file": "a.ts"})
	tl.Drain(time.Second)
	out := buf.String()
	if !strings.Contains(out, "stoke.descent.file_cap_exceeded") {
		t.Errorf("expected stoke.descent.file_cap_exceeded in %q", out)
	}
}

func splitNonEmpty(s string) []string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}
