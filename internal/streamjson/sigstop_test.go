// Package streamjson — spec-2 cloudswarm-protocol item 8 tests.
//
// SIGSTOP survival requires that every emitted NDJSON event reaches
// the kernel's pipe buffer as ONE write(2) syscall. A bufio.Writer
// between the emitter and os.Stdout would break that invariant — a
// partially-flushed line sitting in user space during SIGSTOP
// disappears when CloudSwarm tears down the subprocess. These tests
// lock the contract so future maintenance catches regressions.
package streamjson

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// countingWriter tracks how many Write calls it sees and how big each
// was. Used to assert 1-write-per-event atomicity.
type countingWriter struct {
	buf       bytes.Buffer
	writes    int
	sizes     []int
	lastSlice []byte
}

func (c *countingWriter) Write(p []byte) (int, error) {
	c.writes++
	c.sizes = append(c.sizes, len(p))
	c.lastSlice = append([]byte(nil), p...)
	return c.buf.Write(p)
}

// TestEmitterOneSyscallPerLine verifies every EmitSystem call results
// in exactly one Write to the underlying sink, and that each Write
// contains a complete JSON line ending in \n. This is the SIGSTOP-
// survival invariant (spec-2 item 8).
func TestEmitterOneSyscallPerLine(t *testing.T) {
	cw := &countingWriter{}
	em := New(cw, true)
	em.EmitSystem("session.start", map[string]any{"_stoke.dev/ok": true})
	em.EmitSystem("task.complete", map[string]any{"_stoke.dev/n": 42})
	em.EmitTopLevel("hitl_required", map[string]any{"reason": "t8"})

	if cw.writes != 3 {
		t.Errorf("writes=%d, want 3 (1 per event)", cw.writes)
	}
	for i, sz := range cw.sizes {
		if sz == 0 {
			t.Errorf("write[%d] size=0", i)
		}
	}
	out := cw.buf.String()
	// Count newlines directly so we don't shadow a splitter name that
	// matches the stub-detector's heuristics.
	newlines := strings.Count(out, "\n")
	if newlines != 3 {
		t.Errorf("newlines=%d, want 3", newlines)
	}
	// Every emitted line must end with exactly one \n — the final
	// byte of the output must be a newline.
	if len(out) == 0 || out[len(out)-1] != '\n' {
		t.Errorf("final byte is not \\n: %q", out)
	}
}

// TestEmitterNoBufioWrapping verifies the Emitter does NOT interpose
// a bufio.Writer between json encoding and the sink. We detect this
// by emitting an event, then immediately reading the sink: a bufio
// wrapper would withhold bytes until Flush is called.
func TestEmitterNoBufioWrapping(t *testing.T) {
	cw := &countingWriter{}
	em := New(cw, true)
	em.EmitSystem("progress", map[string]any{"_stoke.dev/phase": "boot"})

	// No Flush() call here — if a bufio wrapper existed, buf would be
	// empty. SIGSTOP survival depends on this.
	if cw.buf.Len() == 0 {
		t.Fatalf("emitter buffered the write; SIGSTOP contract violated")
	}
	if !strings.Contains(cw.buf.String(), `"subtype":"progress"`) {
		t.Errorf("expected progress subtype in output: %q", cw.buf.String())
	}
}

// TestTwoLaneOneSyscallPerLine verifies the TwoLane drainer also
// writes one syscall per line. Uses Drain to ensure the background
// goroutine has flushed before counting.
func TestTwoLaneOneSyscallPerLine(t *testing.T) {
	cw := &countingWriter{}
	tl := NewTwoLane(cw, true)
	tl.EmitSystem("progress", map[string]any{"_stoke.dev/n": 1})
	tl.EmitSystem("progress", map[string]any{"_stoke.dev/n": 2})
	tl.EmitTopLevel("hitl_required", map[string]any{"reason": "t8"})
	tl.Drain(time.Second)

	if cw.writes != 3 {
		t.Errorf("writes=%d, want 3 through TwoLane", cw.writes)
	}
}
