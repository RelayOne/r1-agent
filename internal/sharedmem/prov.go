// Package sharedmem — prov.go
//
// PROV-AGENT metadata attached to every write. Every mutation
// carries the source agent, timestamp, contributing data sources,
// and confidence so audit readers can trace who contributed what
// without re-running the writing agent.
package sharedmem

import (
	"fmt"
	"time"
)

// ProvenanceEntry is the PROV-AGENT record for one write. Fields
// follow the PROV-AGENT vocabulary where applicable; Stoke-
// specific fields are namespaced.
type ProvenanceEntry struct {
	// AgentID identifies the writing agent. Ed25519 public key
	// derived from stancesign (STOKE-013) when available;
	// otherwise an opaque stance+session identifier.
	AgentID string `json:"agent_id"`

	// Action is the operation class: "create", "insert",
	// "replace", "rethink", "rollback". Human-readable for
	// audit UIs.
	Action string `json:"action"`

	// Timestamp is when the write was applied.
	Timestamp time.Time `json:"timestamp"`

	// Sources lists contributing data refs (ledger node IDs,
	// file paths, URLs) the agent drew on to produce this
	// write. Optional but encouraged — a write whose origin
	// isn't traceable is hard to audit.
	Sources []string `json:"sources,omitempty"`

	// Confidence is the writer's self-reported confidence in
	// the value, on [0, 1]. Consumers weigh writes by this
	// score when reconciling contradictions. Zero-value is
	// treated as "unreported" not "zero confidence".
	Confidence float64 `json:"confidence,omitempty"`

	// Note is free-form explanation. Kept short in production;
	// callers that want rich notes should write them to a
	// separate ledger node and put the node ID here via Sources.
	Note string `json:"note,omitempty"`

	// ReplayValue carries the target value for Rollback writes.
	// Populated only for rollback entries; other actions leave
	// it nil. Kept for backward-compat with callers that pass an
	// explicit value to Rollback; SnapshotValue (below) makes
	// this optional in the common case.
	ReplayValue any `json:"replay_value,omitempty"`

	// RolledBackTo names the version the rollback targeted.
	// Zero for non-rollback entries.
	RolledBackTo int `json:"rolled_back_to,omitempty"`

	// SnapshotValue captures the Block.Value AS IT EXISTED right
	// after this provenance entry's write was applied. Populated by
	// Create + Apply at write time; nil for entries authored by
	// older callers or by Rollback writes (which already record the
	// target Value via ReplayValue).
	//
	// Rollback walks the provenance to find the entry whose
	// post-write version matches the target and uses that entry's
	// SnapshotValue to restore Block.Value — eliminating the need
	// for callers to remember and re-supply the historical value.
	// Surfaced by audit/scan-go-stubs.md item #7 ("Rollback
	// semantics is contractually incomplete" — this closes that
	// gap).
	//
	// Caveat: SnapshotValue is stored by reference. Callers MUST
	// treat Block.Value as immutable across writes — if you mutate
	// the value after Apply returns, the snapshot is corrupted. The
	// reducer + Apply contract already requires returning a NEW
	// value rather than mutating the input (per the package doc
	// invariants); this field reuses that contract without forcing a
	// deep clone on every write.
	SnapshotValue any `json:"snapshot_value,omitempty"`
}

// validateProvEntry enforces that a write's provenance carries at
// least an AgentID and a Timestamp. These are the minimum for an
// auditable write — Sources + Confidence + Note are optional.
func validateProvEntry(p ProvenanceEntry) error {
	if p.AgentID == "" {
		return fmt.Errorf("%w: agent_id required", ErrNoProvenance)
	}
	if p.Timestamp.IsZero() {
		return fmt.Errorf("%w: timestamp required", ErrNoProvenance)
	}
	if p.Confidence < 0 || p.Confidence > 1 {
		return fmt.Errorf("sharedmem: confidence out of range [0,1]: %v", p.Confidence)
	}
	return nil
}

// validateProv checks a whole provenance slice. Every entry must
// satisfy validateProvEntry; an empty slice is rejected for
// Create writes (a block starts with at least one creation
// entry).
func validateProv(prov []ProvenanceEntry) error {
	if len(prov) == 0 {
		return fmt.Errorf("%w: initial block needs at least one creation entry", ErrNoProvenance)
	}
	for i, p := range prov {
		if err := validateProvEntry(p); err != nil {
			return fmt.Errorf("provenance[%d]: %w", i, err)
		}
	}
	return nil
}
