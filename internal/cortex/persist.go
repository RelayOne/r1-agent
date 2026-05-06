// Package cortex -- persistent write-through for Workspace Notes.
//
// writeNote is the single point where the in-memory Workspace fans a freshly
// validated Note out to the durable, WAL-backed bus. The Workspace.Publish
// critical section calls this function while still holding the write mutex
// (per spec item 4) so that crash-recovery replay observes Notes in the
// same total order they appear in Workspace.notes.
//
// Two operating modes:
//
//  1. Durable mode (durable != nil): marshal the Note to JSON and emit a
//     bus.Event of Type "cortex.note.published". Errors propagate to the
//     caller, which surfaces them through Workspace.Publish.
//
//  2. In-memory mode (durable == nil): no-op. The Workspace constructor
//     accepts a nil bus to support unit harnesses, the prewarm pump, and
//     ephemeral runs that deliberately omit persistence.
package cortex

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/RelayOne/r1/internal/bus"
)

// EventTypeCortexNotePublished is the bus.EventType emitted for every Note
// that lands in a durable Workspace. Subscribers and crash-recovery
// replayers filter on this string; keeping it as a named constant prevents
// drift between writeNote and any future reader.
const EventTypeCortexNotePublished bus.EventType = "cortex.note.published"

// writeNote performs the durable write-through for a single Note.
//
// The function is internal to package cortex on purpose: callers go
// through Workspace.Publish, which already validates the Note, assigns
// its ID/Round/EmittedAt, and serializes Publishes via the workspace
// mutex. writeNote therefore assumes its Note argument is fully populated
// and structurally valid.
//
// Error contract:
//
//   - durable == nil          -> nil (in-memory mode, intentional no-op).
//   - json.Marshal failure    -> wrapped error with the "cortex/persist"
//     namespace so failure-class fingerprinting can group it.
//   - durable.Publish failure -> bubbled verbatim; bus.Bus.Publish already
//     wraps WAL append errors with its own "bus:" prefix.
func writeNote(durable *bus.Bus, n Note) error {
	if durable == nil {
		return nil
	}
	payload, err := json.Marshal(n)
	if err != nil {
		return fmt.Errorf("cortex/persist: marshal note: %w", err)
	}
	return durable.Publish(bus.Event{
		Type:    EventTypeCortexNotePublished,
		Payload: json.RawMessage(payload),
	})
}

// Replay rebuilds an empty Workspace's in-memory Note store from the
// durable WAL. It reads every event whose Type matches the canonical
// "cortex.note.published" string from sequence 0 onward, JSON-decodes
// the Payload back into a Note, and appends it directly to w.notes
// under the workspace mutex.
//
// Replay deliberately bypasses Workspace.Publish to avoid two failure
// modes that would otherwise occur during crash recovery:
//
//  1. Double-emit -- Publish fans every Note out to the event hub and
//     to subscribers. During replay the consumers of those signals
//     have not yet been wired up (or, worse, have been wired up by a
//     freshly-booted process that would treat replayed Notes as live).
//  2. Double-persist -- Publish funnels through writeNote, which would
//     append a fresh copy of every Note back into the WAL, doubling
//     the durable record on every recovery cycle.
//
// Idempotency: if w.notes is already non-empty, Replay logs the skip
// at slog.Info and returns nil. Callers may therefore invoke Replay
// unconditionally during boot without guarding against double-call.
//
// Error contract:
//
//   - durable == nil          -> nil (in-memory mode; nothing to replay).
//   - bus.Replay failure      -> bubbled verbatim.
//   - per-event Unmarshal err -> logged at slog.Warn and skipped; the
//     remaining events still load. A corrupt single payload must not
//     prevent recovery of the rest of the workspace.
//
// After a successful replay, w.drainedUpTo is set to len(w.notes) so
// the first post-recovery Drain call sees a self-consistent cursor.
// w.seq is also advanced so any subsequent Publish receives a unique
// monotonic ID that does not collide with replayed Notes.
func (w *Workspace) Replay() error {
	if w.durable == nil {
		return nil
	}

	// Idempotency check uses a read lock so concurrent readers (e.g.
	// Snapshot) are not blocked while we decide whether to replay.
	w.mu.RLock()
	already := len(w.notes)
	w.mu.RUnlock()
	if already > 0 {
		slog.Info("cortex/replay: skipping — workspace already populated",
			"notes", already)
		return nil
	}

	// bus.Pattern.Matches treats an empty TypePrefix as "match any
	// type". To restrict to cortex.note.published events specifically
	// we set TypePrefix to the full event type string; HasPrefix is
	// then exact-equal for events of that type.
	pattern := bus.Pattern{TypePrefix: string(EventTypeCortexNotePublished)}

	if err := w.durable.Replay(pattern, 0, func(evt bus.Event) {
		var n Note
		if err := json.Unmarshal(evt.Payload, &n); err != nil {
			slog.Warn("cortex/replay: skipping event with invalid payload",
				"seq", evt.Sequence,
				"id", evt.ID,
				"err", err,
			)
			return
		}
		w.mu.Lock()
		w.notes = append(w.notes, n)
		// Keep w.seq strictly greater than every replayed Note's
		// numeric suffix so subsequent Publishes do not collide.
		// Replayed Notes always have ID "note-N"; the simplest
		// invariant is seq == len(notes) which post-recovery matches
		// the original publication contract (see Workspace.Publish).
		w.seq = uint64(len(w.notes))
		w.mu.Unlock()
	}); err != nil {
		return fmt.Errorf("cortex/persist: replay: %w", err)
	}

	w.mu.Lock()
	w.drainedUpTo = uint64(len(w.notes))
	w.mu.Unlock()
	return nil
}
