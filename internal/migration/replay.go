// replay.go — WAL replay adapter. Spec §7.2 step 9 + T11.
//
// The bundle's bus/wal.ndjson holds one bus.Event JSON per line. The
// destination's bus.Bus already knows how to Publish; ReplayBundleWAL
// adapts the raw bytes onto Publish calls, re-scoping each event's
// scope.MissionID to the destination session id (since the source's
// id has been replaced by the destination's allocation).
//
// Incremental progress: after every checkpointInterval events, and at
// EOF, onProgress is invoked with the current bus.Event.Sequence so
// the importer can recompute the destination's partial chain root
// and compare against the bundle's wal_checkpoint_hashes entry at the
// same seq (T14 divergence detection). A non-nil error from
// onProgress aborts the replay and propagates back to the caller.

package migration

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/RelayOne/r1/internal/bus"
)

// DefaultCheckpointInterval is the spec-§7.2-recommended event
// interval at which onProgress is invoked. Configurable per-replayer
// for tests that want denser checks.
const DefaultCheckpointInterval = 100

// EventPublisher is the contract the WAL replayer needs from the
// destination's bus. Production wiring passes *bus.Bus directly
// (bus.Bus.Publish satisfies it). Tests can supply a capture-list
// shim for assertions.
type EventPublisher interface {
	Publish(evt bus.Event) error
}

// BusWALReplayer adapts EventPublisher onto the WALReplayer
// contract. Decode-then-Publish per line; invoke onProgress every
// CheckpointInterval events. The replayer returns the number of
// events actually published (zero on empty input).
type BusWALReplayer struct {
	Publisher          EventPublisher
	CheckpointInterval int

	// ScopeMissionRewrite controls whether each replayed event's
	// scope.MissionID gets rewritten from the source session id to
	// the destination session id. Default true (preserves session
	// semantics on the destination); tests that want byte-faithful
	// replay can set to false.
	ScopeMissionRewrite bool

	// SourceSessionID is the source-side session id used as the
	// rewrite key. Events whose scope.MissionID matches this string
	// have it rewritten to destSessionID; other events pass through
	// unmodified.
	SourceSessionID string
}

// NewBusWALReplayer constructs a replayer with the defaults: 100-
// event checkpoint interval, scope rewrite ON.
func NewBusWALReplayer(pub EventPublisher, sourceSessionID string) *BusWALReplayer {
	return &BusWALReplayer{
		Publisher:           pub,
		CheckpointInterval:  DefaultCheckpointInterval,
		ScopeMissionRewrite: true,
		SourceSessionID:     sourceSessionID,
	}
}

// ReplayWAL implements migration.WALReplayer. Returns the number of
// events successfully published. onProgress fires every
// CheckpointInterval events AND at EOF (so the last partial-root
// check is observable). An onProgress error aborts the replay; the
// caller's ChainRootMismatch handling takes over from there.
func (r *BusWALReplayer) ReplayWAL(destSessionID string, walBytes []byte, onProgress func(seq uint64) error) (uint64, error) {
	if r.Publisher == nil {
		return 0, fmt.Errorf("migration: BusWALReplayer: no Publisher wired")
	}
	interval := r.CheckpointInterval
	if interval <= 0 {
		interval = DefaultCheckpointInterval
	}

	var count uint64
	var lastSeq uint64
	for _, line := range bytes.Split(walBytes, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev bus.Event
		if err := json.Unmarshal(line, &ev); err != nil {
			return count, fmt.Errorf("migration: replay parse: %w", err)
		}
		if r.ScopeMissionRewrite && r.SourceSessionID != "" && ev.Scope.MissionID == r.SourceSessionID {
			ev.Scope.MissionID = destSessionID
		}
		// Reset Sequence so the destination bus assigns a fresh seq
		// from its own monotonic counter — the source-side seq lives
		// in the manifest's wal_first_seq / wal_last_seq for the
		// partial-root index, but the destination's bus owns its own
		// numbering post-Publish.
		ev.Sequence = 0
		// Also reset ID so a re-import doesn't collide with prior
		// event IDs on the destination's bus.
		ev.ID = ""
		if err := r.Publisher.Publish(ev); err != nil {
			return count, fmt.Errorf("migration: replay publish: %w", err)
		}
		count++
		// Use the SOURCE's last-seq for the progress callback (so the
		// importer can match against wal_checkpoint_hashes entries
		// keyed by source-side seq).
		// We extracted the SOURCE seq before zeroing ev.Sequence —
		// recover it from the original line.
		srcSeq := extractEventSeq(line)
		if srcSeq > lastSeq {
			lastSeq = srcSeq
		}
		if onProgress != nil && count%uint64(interval) == 0 {
			if err := onProgress(srcSeq); err != nil {
				return count, err
			}
		}
	}
	// Final EOF callback. Even if onProgress already fired at the
	// last checkpoint boundary, we re-fire at EOF so the last partial
	// root + the manifest's final chain root agree.
	if onProgress != nil {
		if err := onProgress(lastSeq); err != nil {
			return count, err
		}
	}
	return count, nil
}

// extractEventSeq peeks the JSON line for its `sequence` field. We
// decode the full Event for Publish above but the unmarshaled struct
// has been mutated (ID + Sequence zeroed) before we reach the
// progress hook — re-parse the raw line for the source-side seq.
// Best-effort: malformed JSON returns 0.
func extractEventSeq(line []byte) uint64 {
	var probe struct {
		Sequence uint64 `json:"sequence"`
	}
	if err := json.Unmarshal(line, &probe); err != nil {
		return 0
	}
	return probe.Sequence
}
