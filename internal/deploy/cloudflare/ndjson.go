// Package cloudflare contains the Wrangler-based deploy adapter bits for
// Cloudflare Workers.
//
// This file (DP2-5) implements only the NDJSON tailer — the helper that
// watches the file path supplied via WRANGLER_OUTPUT_FILE_PATH and turns
// each complete JSON line into an Event for the caller. The full
// cloudflare.Deployer surface is built in DP2-6 on top of this helper.
//
// Design choice: POLLING over fsnotify.
//
// Wrangler appends to the NDJSON file from a single writer, and the
// caller's typical deploy runs in the tens of seconds; a 500ms stat-based
// poll is well within acceptable latency and keeps the dependency
// surface stdlib-only (spec §Library Preferences: "Stdlib-preferred;
// polling over fsnotify is fine to avoid the dep"). If a future caller
// needs sub-100ms latency we can swap the implementation behind the same
// TailNDJSON signature without touching callers.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/ericmacdougall/stoke/internal/logging"
)

// pollInterval is how often TailNDJSON stats the file and reads new bytes.
// Exposed as a package var so tests can tighten it; do not mutate in
// production code paths.
var pollInterval = 500 * time.Millisecond

// createWaitInterval is how often TailNDJSON retries os.Open when the
// file does not yet exist. Shorter than pollInterval so startup latency
// is bounded by the creator, not by us.
var createWaitInterval = 100 * time.Millisecond

// Event is one parsed NDJSON record emitted by Wrangler.
//
// Type is the value of the "type" JSON field. Unknown values are
// preserved verbatim — callers (DP2-6) decide how to route them.
//
// Message is the value of the "message" JSON field when present
// (common on "error" / "warning" events); empty otherwise.
//
// Raw is the complete original JSON object for the line, so callers
// can re-decode into a richer, event-specific struct without us having
// to enumerate every known payload shape here.
//
// Timestamp is the moment TailNDJSON parsed the line. The Wrangler
// NDJSON contract includes a "timestamp" field per event, but its
// format has churned across Wrangler 3.x and 4.x (RT-10 §3); we use
// wall-clock receipt here and let callers fish the original out of Raw
// if they need it.
type Event struct {
	Type      string
	Message   string
	Raw       json.RawMessage
	Timestamp time.Time
}

// minimalShape is the tiny subset of the Wrangler event shape that
// TailNDJSON itself cares about. Everything else stays in Raw.
type minimalShape struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// TailNDJSON tails the NDJSON file at path, invoking onEvent exactly once
// per complete, well-formed JSON line.
//
// Behavior:
//
//   - If path does not yet exist when TailNDJSON is called, it waits for
//     the file to appear (createWaitInterval ticks) so callers can
//     launch this helper before spawning Wrangler.
//   - Every pollInterval tick, TailNDJSON reads any bytes appended
//     since the previous tick and splits them on '\n'. Bytes following
//     the last '\n' are held in an internal buffer and re-joined with
//     the next read; partial lines never reach onEvent.
//   - Each complete line is fed to json.Unmarshal. Malformed lines are
//     logged at DEBUG and skipped. The tailer does NOT abort on
//     malformed input — Wrangler's CLI codegen is known to churn
//     (spec §Cloudflare Flag Churn Mitigation) and callers want
//     best-effort parsing.
//   - Unknown "type" values are NOT errors. onEvent receives the Event
//     with Type set to whatever the wrangler emits.
//   - TailNDJSON returns nil when ctx is cancelled, or a non-nil error
//     only for fatal I/O problems (path creation, read errors that are
//     not io.EOF). It drains any still-pending complete lines from
//     the file before returning on ctx cancellation.
//
// Threading:
//
//   - TailNDJSON runs on the caller's goroutine — it is synchronous and
//     blocks until ctx is cancelled (or a fatal error occurs). Callers
//     that need async behavior should invoke it in their own goroutine.
//   - onEvent is invoked from the same goroutine that runs TailNDJSON.
//     If the caller installs onEvent closures that touch shared state,
//     they are responsible for their own synchronization.
//   - onEvent must not block indefinitely: the tailer stalls until it
//     returns. A slow onEvent delays subsequent line parsing.
func TailNDJSON(ctx context.Context, path string, onEvent func(Event)) error {
	if onEvent == nil {
		return errors.New("cloudflare.TailNDJSON: onEvent is nil")
	}
	if path == "" {
		return errors.New("cloudflare.TailNDJSON: path is empty")
	}

	log := logging.Component("cloudflare-ndjson").With(slog.String("path", path))

	f, err := openWhenReady(ctx, path)
	if err != nil {
		return err
	}
	defer f.Close()

	var carry bytes.Buffer // trailing partial line bytes
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	// Do one immediate read so we don't have to wait a full pollInterval
	// if Wrangler already wrote a burst before TailNDJSON was called.
	drain(f, &carry, onEvent, log)

	for {
		select {
		case <-ctx.Done():
			// Final drain: callers expect that complete lines already
			// on disk are reported, even if ctx was cancelled after
			// they landed.
			drain(f, &carry, onEvent, log)
			return nil
		case <-ticker.C:
			drain(f, &carry, onEvent, log)
		}
	}
}

// openWhenReady opens path for reading. If the file is missing, it polls
// createWaitInterval ticks until it appears or ctx is cancelled. When ctx
// fires first, openWhenReady returns ctx.Err().
func openWhenReady(ctx context.Context, path string) (*os.File, error) {
	for {
		f, err := os.Open(path)
		if err == nil {
			return f, nil
		}
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("cloudflare.TailNDJSON: open %s: %w", path, err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(createWaitInterval):
		}
	}
}

// drain reads everything currently available from f, splits on newlines,
// holds any trailing partial line in carry, and dispatches completed
// lines to onEvent. Read errors other than io.EOF are logged and
// swallowed — the tailer will retry on the next tick (Wrangler may be
// mid-write; the next poll usually succeeds).
func drain(f *os.File, carry *bytes.Buffer, onEvent func(Event), log *slog.Logger) {
	var buf [8 * 1024]byte
	for {
		n, err := f.Read(buf[:])
		if n > 0 {
			carry.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Debug("read error; will retry", slog.String("err", err.Error()))
			break
		}
		if n == 0 {
			break
		}
	}
	// Split out complete lines; keep any trailing partial bytes.
	for {
		data := carry.Bytes()
		idx := bytes.IndexByte(data, '\n')
		if idx < 0 {
			break
		}
		line := make([]byte, idx)
		copy(line, data[:idx])
		// Drop the line + newline from the buffer.
		carry.Next(idx + 1)
		dispatch(line, onEvent, log)
	}
}

// dispatch parses one candidate NDJSON line and, if valid, calls onEvent.
// Empty and whitespace-only lines are silently skipped. Malformed JSON
// is logged at DEBUG and skipped — the NDJSON contract (spec §Cloudflare
// Flag Churn Mitigation #3) explicitly requires unknown-event tolerance
// but does not mandate hard-failing on a single corrupted line.
func dispatch(line []byte, onEvent func(Event), log *slog.Logger) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return
	}
	var shape minimalShape
	if err := json.Unmarshal(trimmed, &shape); err != nil {
		log.Debug("skipping malformed NDJSON line",
			slog.String("line", truncate(string(trimmed), 200)),
			slog.String("err", err.Error()))
		return
	}
	// Preserve the raw bytes so callers can decode richer shapes later.
	raw := make(json.RawMessage, len(trimmed))
	copy(raw, trimmed)
	onEvent(Event{
		Type:      shape.Type,
		Message:   shape.Message,
		Raw:       raw,
		Timestamp: time.Now().UTC(),
	})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
