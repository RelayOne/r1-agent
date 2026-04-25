// Package streamjson — twolane.go
//
// Spec-2 cloudswarm-protocol item 1: two-lane NDJSON emitter.
//
// CloudSwarm (Temporal activity ExecuteStokeActivity) pipes stdout
// NDJSON line-by-line into Postgres + NATS. The subprocess may pause
// via SIGSTOP at any time — any buffered data in userspace stays
// frozen until SIGCONT. To survive, we:
//
//   1. Use an unbuffered os.Stdout (Emitter already does this).
//   2. Write each event atomically via json.Encoder.Encode under a
//      mutex — one syscall per line, no interleave.
//   3. Split emission into TWO lanes so critical events (hitl_required,
//      task.complete, error, complete, mission.aborted) never drop,
//      while observability events (descent.tier, cost.update,
//      progress, etc.) drop-oldest under back-pressure.
//
// Critical channel: cap 256, blocking on send — CloudSwarm is expected
// to drain fast. If it doesn't, stoke stalls; Temporal activity
// timeout bounds the overall failure window.
//
// Observability channel: cap 1024, drop-oldest. Every 5s tick the
// background writer emits a "stream.dropped" event with the count
// of drops since last tick so consumers can detect gaps.
package streamjson

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// Event type constants mirrored from the descent-hardening protocol.
// Kept here alongside TwoLane so the entire wire surface lives in
// one package (no new internal/events/ per decision C1).
const (
	TypeHITLRequired   = "hitl_required"
	TypeComplete       = "complete"
	TypeError          = "error"
	TypeMissionAborted = "mission.aborted"
)

// TwoLane wraps an underlying Emitter with two in-memory lanes:
// critical (blocking 256-cap) and observability (drop-oldest 1024-cap).
// A background goroutine drains both onto the writer under the
// Emitter's mutex.
type TwoLane struct {
	base     *Emitter
	critical chan map[string]any
	observ   chan map[string]any
	dropped  atomic.Uint64
	done     chan struct{}
	// stop is closed by Drain to signal the run goroutine + senders
	// to exit. Using a separate stop channel (instead of closing
	// tl.critical) avoids the race between sendCritical's `<-`
	// receive on the closed channel and Drain's close call.
	stop     chan struct{}
	stopOnce sync.Once
	stopped  atomic.Bool
}

// NewTwoLane constructs a TwoLane bound to the same writer as an
// underlying Emitter. When enabled is false, the inner Emitter is a
// no-op so TwoLane inherits silence without branching at every call.
func NewTwoLane(w io.Writer, enabled bool) *TwoLane {
	tl := &TwoLane{
		base:     New(w, enabled),
		critical: make(chan map[string]any, 256),
		observ:   make(chan map[string]any, 1024),
		done:     make(chan struct{}),
		stop:     make(chan struct{}),
	}
	go tl.run()
	return tl
}

// SessionID returns the underlying emitter's session id. Useful when
// callers want to correlate emitted lines with bus events.
func (tl *TwoLane) SessionID() string { return tl.base.SessionID() }

// Enabled reports whether the underlying emitter is writing output.
func (tl *TwoLane) Enabled() bool { return tl.base.Enabled() }

// isCritical identifies event names that must NEVER drop, even under
// pipe back-pressure. Every other subtype routes to the observability
// lane (which drops-oldest). This keeps the list explicit so accidental
// critical events don't silently block the process.
func isCriticalType(eventType, subtype string) bool {
	switch eventType {
	case TypeHITLRequired, TypeError, TypeComplete, TypeMissionAborted:
		return true
	}
	if subtype == "task.complete" {
		return true
	}
	return false
}

// EmitSystem writes an event of type "system" with the given subtype.
// Observability subtypes are drop-oldest; "task.complete" is critical.
func (tl *TwoLane) EmitSystem(subtype string, extra map[string]any) {
	if !tl.Enabled() {
		return
	}
	evt := buildEvent("system", subtype, tl.base.SessionID(), extra)
	if isCriticalType("system", subtype) {
		tl.sendCritical(evt)
		return
	}
	tl.sendObserv(evt)
}

// EmitTopLevel writes a top-level event (type != "system"). Used for
// hitl_required, error, complete, mission.aborted — all critical.
// Any non-critical type still routes through the observability lane.
func (tl *TwoLane) EmitTopLevel(eventType string, extra map[string]any) {
	if !tl.Enabled() {
		return
	}
	evt := map[string]any{
		"type":       eventType,
		"uuid":       uuid.NewString(),
		"session_id": tl.base.SessionID(),
	}
	for k, v := range extra {
		evt[k] = v
	}
	if isCriticalType(eventType, "") {
		tl.sendCritical(evt)
		return
	}
	tl.sendObserv(evt)
}

// sendCritical blocks until the critical lane accepts the event OR
// the TwoLane is shutting down. A full critical lane means CloudSwarm
// has stopped draining — stalling here is intentional: Temporal's
// activity timeout will eventually kill us, preferable to silently
// losing an hitl_required. When Drain has signalled stop, the event
// falls through to a direct synchronous write via the base emitter.
func (tl *TwoLane) sendCritical(evt map[string]any) {
	if tl.stopped.Load() {
		tl.base.writeEvent(evt)
		return
	}
	select {
	case tl.critical <- evt:
	case <-tl.stop:
		tl.base.writeEvent(evt)
	}
}

// sendObserv drops-oldest under back-pressure. A full observability
// lane loses the oldest event to make room; a full-after-eviction
// lane increments the drop counter and moves on. Stop-signal takes
// priority over lane sends — a late observability event after Drain
// falls through to a direct synchronous write.
func (tl *TwoLane) sendObserv(evt map[string]any) {
	if tl.stopped.Load() {
		tl.base.writeEvent(evt)
		return
	}
	// Fast path: lane has room.
	select {
	case <-tl.stop:
		tl.base.writeEvent(evt)
		return
	case tl.observ <- evt:
		return
	default:
	}
	// Lane full — drop oldest.
	select {
	case <-tl.observ:
	default:
	}
	select {
	case <-tl.stop:
		tl.base.writeEvent(evt)
	case tl.observ <- evt:
	default:
		tl.dropped.Add(1)
	}
}

// run is the background drainer. Pulls from both lanes under the
// Emitter's mutex so writes never interleave, and fires
// stream.dropped every 5s when the counter is non-zero.
func (tl *TwoLane) run() {
	defer close(tl.done)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-tl.stop:
			tl.flushRemaining()
			return
		case evt := <-tl.critical:
			tl.base.writeEvent(evt)
		case evt := <-tl.observ:
			tl.base.writeEvent(evt)
		case <-ticker.C:
			if n := tl.dropped.Swap(0); n > 0 {
				tl.base.writeEvent(buildEvent("system", "stream.dropped", tl.base.SessionID(), map[string]any{
					"_stoke.dev/count": n,
				}))
			}
		}
	}
}

// flushRemaining drains any remaining events on both lanes
// post-shutdown. Called from run() after the stop signal fires so
// events queued before shutdown still reach the writer.
func (tl *TwoLane) flushRemaining() {
	// Drain critical first — those are contract-level events.
	for {
		select {
		case evt := <-tl.critical:
			tl.base.writeEvent(evt)
		default:
			goto drainObserv
		}
	}
drainObserv:
	for {
		select {
		case evt := <-tl.observ:
			tl.base.writeEvent(evt)
		default:
			return
		}
	}
}

// Drain blocks up to timeout waiting for both lanes to empty and the
// background goroutine to exit. Callers should invoke it before
// os.Exit so pending events make it to stdout.
//
// Drain closes the tl.stop signal channel (not the lane channels)
// so in-flight senders see the stop via their `select case <-tl.stop`
// and fall back to a direct synchronous write instead of racing with
// a close on the lane channel.
func (tl *TwoLane) Drain(timeout time.Duration) {
	tl.stopOnce.Do(func() {
		tl.stopped.Store(true)
		close(tl.stop)
	})
	select {
	case <-tl.done:
	case <-time.After(timeout):
	}
}

// EmitTerminal drains all pending lanes, then writes one synchronous
// line to the underlying sink so the caller guarantees this line is
// the LAST byte written. Used by runCommandExitCode to emit "complete"
// as the final NDJSON line per the CloudSwarm contract — critical and
// observability lanes drain in nondeterministic order while the
// background goroutine runs, so a separate terminal write is needed
// to make a "last line" guarantee possible.
//
// After EmitTerminal returns, further Emit* calls route through the
// stopped-lane fallback (direct write under the Emitter mutex) so
// they remain safe but NO event should race the terminal line.
func (tl *TwoLane) EmitTerminal(eventType string, extra map[string]any) {
	tl.Drain(5 * time.Second)
	evt := map[string]any{
		"type":       eventType,
		"uuid":       uuid.NewString(),
		"session_id": tl.base.SessionID(),
	}
	for k, v := range extra {
		evt[k] = v
	}
	tl.base.writeEvent(evt)
}

// buildEvent composes a minimal event map with type / subtype / uuid /
// session_id and merges in extra. Extracted so EmitSystem and
// EmitTopLevel share the canonical shape.
func buildEvent(eventType, subtype, sessionID string, extra map[string]any) map[string]any {
	evt := map[string]any{
		"type":       eventType,
		"uuid":       uuid.NewString(),
		"session_id": sessionID,
	}
	if subtype != "" {
		evt["subtype"] = subtype
	}
	for k, v := range extra {
		evt[k] = v
	}
	return evt
}

// descentSubtypeBridge is a helper used by tests to verify that the
// TwoLane correctly forwards descent-family subtypes through the
// observability lane (they're subtype-driven and do NOT promote to
// critical). Kept as a method on the TwoLane for symmetry — callers
// normally use EmitSystem directly.
//
// EmitSystem handles D-032 dual-emit for stoke.* subtypes automatically.
func (tl *TwoLane) EmitDescent(kind string, payload map[string]any) {
	tl.EmitSystem("stoke.descent."+kind, payload)
}

// ErrDrainTimeout is returned conceptually when Drain hits its
// deadline with events still buffered. Today Drain doesn't return an
// error, but the variable exists for future API evolution.
var ErrDrainTimeout = fmt.Errorf("streamjson: drain timeout")
