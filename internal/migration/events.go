// events.go — production EventEmitter that publishes onto a bus.Bus.
// Spec T25.

package migration

import (
	"encoding/json"

	"github.com/RelayOne/r1/internal/bus"
)

// BusEventEmitter implements EventEmitter by publishing each of the
// three migration events onto a *bus.Bus. The destination daemon
// wires this in at Importer construction time; tests use a no-op
// or capture-list emitter.
type BusEventEmitter struct {
	Bus *bus.Bus

	// EmitterID is stamped onto Event.EmitterID for audit trail.
	// Typically the daemon's `host_id` + ":session-migration".
	EmitterID string
}

// NewBusEventEmitter is a convenience constructor.
func NewBusEventEmitter(b *bus.Bus, emitterID string) *BusEventEmitter {
	return &BusEventEmitter{Bus: b, EmitterID: emitterID}
}

// EmitMigrateExported emits session.migrate.exported. Called by the
// source-side handler after a successful bundle stream.
func (e *BusEventEmitter) EmitMigrateExported(sourceSessionID, sourceHost, chainRootHash string, nodeCount int) {
	if e == nil || e.Bus == nil {
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"source_session_id": sourceSessionID,
		"source_host":       sourceHost,
		"chain_root_hash":   chainRootHash,
		"node_count":        nodeCount,
	})
	_ = e.Bus.Publish(bus.Event{
		Type:      bus.EvtSessionMigrateExported,
		EmitterID: e.EmitterID,
		Scope:     bus.Scope{MissionID: sourceSessionID},
		Payload:   payload,
	})
}

// EmitMigrateImported implements EventEmitter.
func (e *BusEventEmitter) EmitMigrateImported(sourceSessionID, destSessionID, chainRootHash string, nodeCount int, walReplayed uint64) {
	if e == nil || e.Bus == nil {
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"source_session_id": sourceSessionID,
		"dest_session_id":   destSessionID,
		"chain_root_hash":   chainRootHash,
		"node_count":        nodeCount,
		"wal_replayed":      walReplayed,
	})
	_ = e.Bus.Publish(bus.Event{
		Type:      bus.EvtSessionMigrateImported,
		EmitterID: e.EmitterID,
		Scope:     bus.Scope{MissionID: destSessionID},
		Payload:   payload,
	})
}

// EmitMigrateDivergent implements EventEmitter.
func (e *BusEventEmitter) EmitMigrateDivergent(sourceSessionID, destSessionID, expectedRoot, actualRoot string, divergentAtSeq uint64) {
	if e == nil || e.Bus == nil {
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"source_session_id": sourceSessionID,
		"dest_session_id":   destSessionID,
		"expected":          expectedRoot,
		"actual":            actualRoot,
		"divergent_at_seq":  divergentAtSeq,
	})
	_ = e.Bus.Publish(bus.Event{
		Type:      bus.EvtSessionMigrateDivergent,
		EmitterID: e.EmitterID,
		Scope:     bus.Scope{MissionID: destSessionID},
		Payload:   payload,
	})
}

// NoopEventEmitter satisfies EventEmitter without doing anything.
// Used by tests that don't care about event emission and by the CLI
// path when no bus is wired.
type NoopEventEmitter struct{}

// EmitMigrateImported implements EventEmitter.
func (NoopEventEmitter) EmitMigrateImported(string, string, string, int, uint64) {}

// EmitMigrateDivergent implements EventEmitter.
func (NoopEventEmitter) EmitMigrateDivergent(string, string, string, string, uint64) {}

// CaptureEventEmitter is a test helper that records every emitted
// event into a slice for later assertion. Mutex-guarded so concurrent
// emitters don't race on slice append (the importer is single-call
// per bundle, but the integration test uses both sides at once).
type CaptureEventEmitter struct {
	Imported  []ImportedEvent
	Divergent []DivergentEvent
}

// ImportedEvent is one captured imported event.
type ImportedEvent struct {
	SourceSessionID string
	DestSessionID   string
	ChainRootHash   string
	NodeCount       int
	WALReplayed     uint64
}

// DivergentEvent is one captured divergent event.
type DivergentEvent struct {
	SourceSessionID string
	DestSessionID   string
	Expected        string
	Actual          string
	DivergentAtSeq  uint64
}

// EmitMigrateImported implements EventEmitter.
func (c *CaptureEventEmitter) EmitMigrateImported(src, dest, root string, n int, replayed uint64) {
	c.Imported = append(c.Imported, ImportedEvent{
		SourceSessionID: src,
		DestSessionID:   dest,
		ChainRootHash:   root,
		NodeCount:       n,
		WALReplayed:     replayed,
	})
}

// EmitMigrateDivergent implements EventEmitter.
func (c *CaptureEventEmitter) EmitMigrateDivergent(src, dest, expected, actual string, seq uint64) {
	c.Divergent = append(c.Divergent, DivergentEvent{
		SourceSessionID: src,
		DestSessionID:   dest,
		Expected:        expected,
		Actual:          actual,
		DivergentAtSeq:  seq,
	})
}
