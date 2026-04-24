// Package perflog emits microsecond-precision timing events so the sow
// harness's wall-clock budget can be attributed to specific phases,
// LLM calls, and subprocess invocations.
//
// Opt-in via env: STOKE_PERFLOG=1 enables, STOKE_PERFLOG_FILE=<path>
// routes to a file (otherwise stderr). When disabled, every call is
// a single atomic-load short-circuit — no lock, no format, no alloc.
//
// Event format (one line per event, tab-separated for easy grep/awk):
//
//	[HH:MM:SS.uuuuuu]\tPHASE=phase\tDUR=1234ms\tKEY=val ...\tmsg
//
// Spans: perflog.Start("phase.name", "k=v", ...) returns a Closer; call
// Closer.End("k2=v2") when the span finishes to emit a paired event
// with the measured duration. Start alone does NOT allocate when
// disabled — the returned Closer's End is a no-op.
package perflog

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ericmacdougall/stoke/internal/r1env"
)

var (
	enabled atomic.Bool
	sink    io.Writer = os.Stderr
	mu      sync.Mutex
	once    sync.Once
)

func initIfNeeded() {
	once.Do(func() {
		if r1env.Get("R1_PERFLOG", "STOKE_PERFLOG") == "" {
			return
		}
		enabled.Store(true)
		if path := r1env.Get("R1_PERFLOG_FILE", "STOKE_PERFLOG_FILE"); path != "" {
			f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if err == nil {
				sink = f
			}
		}
	})
}

// Enabled returns true when perflog should emit.
func Enabled() bool {
	initIfNeeded()
	return enabled.Load()
}

// Event emits a single timestamped line. kv entries are free-form
// "key=value" tokens — no parsing, the reader can awk them.
func Event(phase, msg string, kv ...string) {
	if !Enabled() {
		return
	}
	now := time.Now()
	var b strings.Builder
	fmt.Fprintf(&b, "[%s]\tPHASE=%s", now.Format("15:04:05.000000"), phase)
	for _, p := range kv {
		b.WriteByte('\t')
		b.WriteString(p)
	}
	if msg != "" {
		b.WriteByte('\t')
		b.WriteString(msg)
	}
	b.WriteByte('\n')
	mu.Lock()
	sink.Write([]byte(b.String()))
	mu.Unlock()
}

// Closer is the handle returned by Start — call End when the span
// finishes. A zero-value Closer is a no-op (safe when perflog is
// disabled).
type Closer struct {
	phase string
	start time.Time
	kv    []string
}

// Start opens a span and emits a phase-start event. Call End on the
// returned Closer to emit the paired phase-end event with duration.
func Start(phase string, kv ...string) Closer {
	if !Enabled() {
		return Closer{}
	}
	c := Closer{phase: phase, start: time.Now(), kv: kv}
	Event(phase+".start", "", kv...)
	return c
}

// End emits the span's end event with the measured duration. Extra
// kv tokens are appended (useful for result-dependent data like
// token counts or exit codes).
func (c Closer) End(kv ...string) {
	if c.phase == "" {
		return
	}
	dur := time.Since(c.start)
	extra := append([]string{fmt.Sprintf("DUR=%dms", dur.Milliseconds())}, c.kv...)
	extra = append(extra, kv...)
	Event(c.phase+".end", "", extra...)
}

// Duration returns the elapsed time on a span (without emitting).
func (c Closer) Duration() time.Duration {
	if c.phase == "" {
		return 0
	}
	return time.Since(c.start)
}
