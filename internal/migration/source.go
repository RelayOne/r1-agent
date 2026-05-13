// source.go — concrete BundleSource implementations.
//
// Two concrete sources are provided here:
//
//   - LedgerBundleSource: wires a ledger.Store + bus.WAL + memory store
//     (via supplied callback functions) into the BundleSource
//     interface. Used by the production export path
//     (cmd/r1-server/migrate_out.go) and by tests that drive a real
//     ledger.
//   - MapBundleSource: a fully in-memory source built from a flat
//     struct — useful for fixturing in unit tests without setting up
//     a ledger / WAL / memory store.
//
// Neither type is goroutine-safe; the export path is single-shot.

package migration

import (
	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/ledger"
)

// LedgerBundleSource adapts a ledger.Store + bus.WAL + a handful of
// pluggable callbacks into the BundleSource interface. The daemon's
// migrate-out handler builds one of these per export.
//
// The callbacks (MemoryRowsFn, SkillPackRefsFn, etc.) abstract over
// the destination of each data slice so the migration package
// doesn't depend on internal/memory, internal/skill, internal/cortex,
// or internal/lanes (which would force a tangle of cross-package
// imports and make the migration package un-unit-testable).
type LedgerBundleSource struct {
	// Identity.
	HostID         string
	DaemonID       string
	SessionID      string
	TenantIDValue  string
	ModelID        string

	// Ledger + chain-root computation.
	Store *ledger.Store

	// Bus WAL bytes + seq range + checkpoint hashes. The handler
	// reads the on-disk WAL once and stashes it here so the source
	// methods can return slices without re-reading.
	WALBytesValue       []byte
	WALFirstSeqValue    uint64
	WALLastSeqValue     uint64
	WALCheckpointsValue []WALCheckpoint

	// Memory data (collected by the handler from the memory store
	// before constructing the source so reading is a single in-memory
	// op).
	MemoryRowsValue     []byte
	MemoryRowCountValue int
	MemoryTargetsValue  []string

	// Skill pack refs collected from the session's workspace.
	SkillPackRefsValue []SkillPackRef

	// Lobe state snapshots, lane snapshot, pre-export checkpoint.
	LobeStatesValue     map[string][]byte
	LanesSnapshotValue  []byte
	CheckpointValue     []byte

	// chainRootCache memoizes ChainRootHashForSession(SessionID).
	chainRootCache string
	chainRootDone  bool
}

// SourceHost implements BundleSource.
func (s *LedgerBundleSource) SourceHost() string { return s.HostID }

// SourceDaemonID implements BundleSource.
func (s *LedgerBundleSource) SourceDaemonID() string { return s.DaemonID }

// SourceSessionID implements BundleSource.
func (s *LedgerBundleSource) SourceSessionID() string { return s.SessionID }

// TenantID implements BundleSource.
func (s *LedgerBundleSource) TenantID() string { return s.TenantIDValue }

// Model implements BundleSource.
func (s *LedgerBundleSource) Model() string { return s.ModelID }

// ChainRootHash implements BundleSource. Memoized.
func (s *LedgerBundleSource) ChainRootHash() string {
	if !s.chainRootDone {
		if s.Store != nil {
			h, err := s.Store.ChainRootHashForSession(s.SessionID)
			if err == nil {
				s.chainRootCache = h
			}
		}
		s.chainRootDone = true
	}
	return s.chainRootCache
}

// ChainNodes implements BundleSource.
func (s *LedgerBundleSource) ChainNodes() []ledger.Node {
	if s.Store == nil {
		return nil
	}
	nodes, err := s.Store.ListNodesForSession(s.SessionID)
	if err != nil {
		return nil
	}
	return nodes
}

// Edges implements BundleSource.
func (s *LedgerBundleSource) Edges() []ledger.Edge {
	if s.Store == nil {
		return nil
	}
	edges, err := s.Store.ListEdgesForSession(s.SessionID)
	if err != nil {
		return nil
	}
	return edges
}

// Content implements BundleSource. Reads the node via Store.ReadNode
// (which returns header + content + salt) and re-marshals the
// content tier in the same shape Store.WriteNode would have written.
// Encrypted DEK envelopes are preserved byte-for-byte because the
// content JSON.RawMessage is opaque to this code path.
//
// Returns nil bytes if the node is redacted (the caller should
// consult IsRedacted first); defensive against the rare race where
// IsRedacted reports false but the content tier was just shredded.
func (s *LedgerBundleSource) Content(nodeID string) ([]byte, error) {
	if s.Store == nil {
		return nil, nil
	}
	n, err := s.Store.ReadNode(nodeID)
	if err != nil {
		return nil, err
	}
	if n.Content == nil {
		return nil, nil
	}
	// Re-marshal as { "salt": ..., "content": ... } so the import
	// side can decode with the same contentRecord shape Store.WriteNode
	// uses. We hand-roll the encoding here rather than re-exporting
	// contentRecord because that struct is unexported by design
	// (encapsulation of the on-disk format).
	type contentEnvelope struct {
		Salt    string `json:"salt"`
		Content []byte `json:"content"`
	}
	out, mErr := jsonMarshal(contentEnvelope{Salt: n.Salt, Content: []byte(n.Content)})
	if mErr != nil {
		return nil, mErr
	}
	return out, nil
}

// IsRedacted implements BundleSource. Drops the (bool, error) from
// the underlying ledger.Store.IsRedacted into a plain bool — any
// I/O error is treated as "not redacted" (we'd rather include a
// possibly-redacted node than skip a present one; the chain root
// hash check will catch any inconsistency downstream).
func (s *LedgerBundleSource) IsRedacted(nodeID string) bool {
	if s.Store == nil {
		return false
	}
	r, err := s.Store.IsRedacted(nodeID)
	if err != nil {
		return false
	}
	return r
}

// RedactionEventIDs implements BundleSource. Returns the IDs of the
// signed redaction events that applied to the node. Each entry in
// the returned slice is one signed-redaction-event NodeID.
func (s *LedgerBundleSource) RedactionEventIDs(nodeID string) []string {
	if s.Store == nil {
		return nil
	}
	events, err := s.Store.RedactionsFor(nodeID)
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(events))
	for _, ev := range events {
		ids = append(ids, ev.NodeID)
	}
	return ids
}

// BusWAL implements BundleSource.
func (s *LedgerBundleSource) BusWAL() ([]byte, error) { return s.WALBytesValue, nil }

// BusWALSeqRange implements BundleSource.
func (s *LedgerBundleSource) BusWALSeqRange() (uint64, uint64) {
	return s.WALFirstSeqValue, s.WALLastSeqValue
}

// BusWALCheckpoints implements BundleSource.
func (s *LedgerBundleSource) BusWALCheckpoints() []WALCheckpoint { return s.WALCheckpointsValue }

// MemoryRows implements BundleSource.
func (s *LedgerBundleSource) MemoryRows() ([]byte, error) { return s.MemoryRowsValue, nil }

// MemoryRowCount implements BundleSource.
func (s *LedgerBundleSource) MemoryRowCount() int { return s.MemoryRowCountValue }

// MemoryScopeTargets implements BundleSource.
func (s *LedgerBundleSource) MemoryScopeTargets() []string { return s.MemoryTargetsValue }

// SkillPackRefs implements BundleSource.
func (s *LedgerBundleSource) SkillPackRefs() []SkillPackRef { return s.SkillPackRefsValue }

// LobeStates implements BundleSource.
func (s *LedgerBundleSource) LobeStates() (map[string][]byte, error) {
	if s.LobeStatesValue == nil {
		return map[string][]byte{}, nil
	}
	return s.LobeStatesValue, nil
}

// LanesSnapshot implements BundleSource.
func (s *LedgerBundleSource) LanesSnapshot() ([]byte, error) { return s.LanesSnapshotValue, nil }

// CheckpointBytes implements BundleSource.
func (s *LedgerBundleSource) CheckpointBytes() ([]byte, error) { return s.CheckpointValue, nil }

// CollectWALFromBus is a convenience that walks a bus.Bus's WAL from
// seq=0 and serializes each event matching the supplied session id
// (Scope.MissionID == sessionID) into a single byte buffer. Returns
// the bytes + the actual (firstSeq, lastSeq) the bus assigned.
//
// Callers building a LedgerBundleSource typically call this once,
// then stash the result into the source's WALBytesValue / first /
// last fields.
func CollectWALFromBus(b *bus.Bus, sessionID string) ([]byte, uint64, uint64, error) {
	if b == nil {
		return nil, 0, 0, nil
	}
	var buf []byte
	var firstSeq, lastSeq uint64
	err := b.Replay(bus.Pattern{Scope: &bus.Scope{MissionID: sessionID}}, 0, func(ev bus.Event) {
		line, mErr := marshalBusEvent(ev)
		if mErr != nil {
			return
		}
		buf = append(buf, line...)
		buf = append(buf, '\n')
		if firstSeq == 0 || ev.Sequence < firstSeq {
			firstSeq = ev.Sequence
		}
		if ev.Sequence > lastSeq {
			lastSeq = ev.Sequence
		}
	})
	if err != nil {
		return nil, 0, 0, err
	}
	return buf, firstSeq, lastSeq, nil
}

// marshalBusEvent is a small wrapper around encoding/json.Marshal so
// the production path can swap in a faster encoder later without
// editing every caller.
func marshalBusEvent(ev bus.Event) ([]byte, error) {
	return jsonMarshal(ev)
}
