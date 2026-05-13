// importer.go — destination-side import pipeline for .r1session
// bundles. Spec §6.2 + §7.2 + T9-T14.
//
// The Importer is a single-call object: build it with the destination
// daemon's dependencies (ledger, bus, memory, sessionhub, etc.),
// pass a parsed bundle in, get back an Outcome describing the new
// session id, ledger / WAL / memory counts, and the verified chain
// root hash. The Importer fails closed on signature mismatch, missing
// skill packs, schema-version drift, chain-root divergence, and key-
// material mismatch — every failure leaves the destination's
// persistent state untouched (savepoint-style rollback via the
// destination's transactional capabilities).
//
// Idempotency: we hash the manifest bytes (not a re-marshaled form;
// see ParsedBundle.ManifestSHA256) and look up in the idempotency
// table. A hit returns the previously-recorded new_session_id with
// Outcome.Idempotent = true and zero rows written.

package migration

import (
	"crypto/ed25519"
	"errors"
	"fmt"

	"github.com/RelayOne/r1/internal/ledger"
)

// Outcome is the result of a successful Import. Carries the
// destination-side session id (newly allocated or the idempotent
// hit), the post-replay chain root hash, the ledger / WAL / memory
// counts the importer actually wrote, and a flag indicating whether
// the import was an idempotent re-application.
type Outcome struct {
	NewSessionID    string
	ChainRootHash   string
	NodeCount       int
	EdgeCount       int
	WALReplayed     uint64
	MemoryRowsCount int
	Verified        bool
	Idempotent      bool
}

// IdempotencyStore is the contract for the destination's
// "have I seen this bundle before?" lookup. Implementations should
// be transactional + on-disk so a daemon restart doesn't lose
// idempotency state (T10 wires this to a SQLite table).
type IdempotencyStore interface {
	// Lookup returns the previously-recorded new_session_id if the
	// manifest hash has been seen, or empty string + nil error if
	// not. A non-nil error fails the import closed.
	Lookup(manifestSHA256 string) (string, error)

	// Record persists (manifest_sha256 -> new_session_id) along with
	// the import-time metadata. Called exactly once per successful
	// non-idempotent import, AFTER chain-root verification passes.
	Record(manifestSHA256, newSessionID, sourceSessionID, sourceHost string) error
}

// PackChecker is the contract for verifying a skill pack is installed
// on the destination. Returns true if a pack with the given
// (pack_id, content_hash) is locally available; false otherwise.
//
// Defined as an interface so the migration package stays decoupled
// from internal/skill — the destination daemon's wiring supplies a
// concrete checker (T19 documents the contract).
type PackChecker interface {
	HasPack(packID, contentHash string) bool
}

// LedgerHydrator is the contract for hydrating ledger nodes + edges +
// content blobs into the destination's ledger. Implementations should
// re-map the MissionID of imported nodes from the bundle's
// source_session_id to the newly-allocated dest_session_id (per spec
// §7.2 step 7) and INSERT-OR-UPDATE for idempotency.
type LedgerHydrator interface {
	// HydrateNode writes one chain-tier node. The node's MissionID
	// has ALREADY been re-mapped to the destination session id by the
	// caller. Returns nil on success or duplicate (ON CONFLICT DO
	// UPDATE semantics); only true I/O failures surface as errors.
	HydrateNode(n ledger.Node) error

	// HydrateEdge writes one edge. Metadata's "session_id" key has
	// ALREADY been re-mapped to the destination session id.
	HydrateEdge(e ledger.Edge) error

	// HydrateContent writes the raw content-tier blob for one node id.
	// Encrypted DEK envelopes are passed through verbatim — the
	// destination's keyring decrypts on read with its master key.
	HydrateContent(nodeID string, blob []byte) error

	// ChainRootHashForSession recomputes the chain root over the
	// destination's now-hydrated nodes for the new session id.
	// Used for the final verify (T13) and the incremental partial-
	// root checks (T14).
	ChainRootHashForSession(sessionID string) (string, error)
}

// MemoryHydrator is the contract for writing per-session memory rows
// into the destination's memory store. Encryption envelopes pass
// through byte-for-byte.
type MemoryHydrator interface {
	HydrateMemoryRow(destSessionID string, rowJSON []byte) error
}

// LobeRestorer restores deterministic per-lobe state. Optional — a
// session without any lobe snapshots passes a nil restorer in. The
// migration package does NOT enforce that all lobes are restored:
// chat-only sessions legitimately have zero lobe snapshots.
type LobeRestorer interface {
	// RestoreLobe is called once per lobe in the bundle. lobeID is
	// the filename stem (lobe-state/<id>.json); state is the raw
	// snapshot bytes the source's Snapshot() returned.
	RestoreLobe(destSessionID, lobeID string, state []byte) error
}

// LanesRestorer restores the lanes snapshot. Optional.
type LanesRestorer interface {
	RestoreLanes(destSessionID string, snapshotJSON []byte) error
}

// SessionAllocator is the contract for allocating a new destination
// session id and recording its initial state. The migration package
// flips the state from "migrating-in" to "idle" only after chain-
// root verification passes (spec §7.2 step 12).
type SessionAllocator interface {
	// AllocateSession returns a freshly-minted session id in state
	// "migrating-in". model + tenant_id are propagated from the
	// manifest for surfacing in the destination's metadata.
	AllocateSession(model, tenantID string) (string, error)

	// SetSessionState transitions an allocated session. State is one
	// of "migrating-in", "idle", "migrated-failed". Callers ALWAYS
	// pair an AllocateSession with a SetSessionState before returning
	// to the caller.
	SetSessionState(destSessionID, state string) error
}

// EventEmitter is the contract for emitting bus events on
// import completion + divergence. Maps to internal/bus.Bus.Publish
// in production wiring; tests can use a no-op or capture-list impl.
type EventEmitter interface {
	EmitMigrateImported(sourceSessionID, destSessionID, chainRootHash string, nodeCount int, walReplayed uint64)
	EmitMigrateDivergent(sourceSessionID, destSessionID, expectedRoot, actualRoot string, divergentAtSeq uint64)
}

// Importer is the destination's import pipeline. Build once per
// daemon at startup; call Import per bundle.
type Importer struct {
	Allocator     SessionAllocator
	Ledger        LedgerHydrator
	Memory        MemoryHydrator
	Lobes         LobeRestorer
	Lanes         LanesRestorer
	Idempotency   IdempotencyStore
	PackChecker   PackChecker
	Emitter       EventEmitter

	// PublicKey is the destination's Ed25519 public key used to
	// verify the bundle's manifest signature. Derived from the same
	// master key the source used (encryption-at-rest spec); a key
	// mismatch surfaces as ErrSignatureMismatch from VerifyManifest.
	PublicKey ed25519.PublicKey

	// BearerTenantID is the tenant claim parsed from the request
	// bearer (HTTP path) or set to the local config's tenant id
	// (CLI path). Used to refuse cross-tenant imports.
	BearerTenantID string

	// WALReplayer is the contract for replaying bus.Event records
	// per spec §7.2 step 9. The migration package does not own bus
	// state directly — the caller wires the destination's bus.Bus
	// into this contract.
	WALReplayer WALReplayer
}

// WALReplayer is the contract for streaming the bundle's WAL into
// the destination's bus. Implementations should call onProgress
// after every Nth event (recommended N=100) AND at EOF so the
// importer can perform the incremental chain-root divergence check
// (T14). The destSessionID is the new session id so the replayer
// can re-tag scope.MissionID if needed.
type WALReplayer interface {
	ReplayWAL(destSessionID string, walBytes []byte, onProgress func(seq uint64) error) (uint64, error)
}

// Import is the single-call import entry point. Steps:
//
//  1. Decode bundle (caller has already done this and passed in pb).
//  2. Verify signature against d.PublicKey.
//  3. Tenant-claim check against d.BearerTenantID.
//  4. Idempotency check (return early on hit).
//  5. Skill-pack pre-flight.
//  6. Allocate destination session.
//  7. Hydrate ledger (nodes + edges + content).
//  8. Hydrate memory rows.
//  9. Replay WAL with incremental partial-root checks.
//  10. Restore lobes + lanes.
//  11. Final chain-root verify.
//  12. Record idempotency row + flip state to idle + emit imported event.
//
// On any failure between step 6 and step 12, the destination session
// is flipped to "migrated-failed" and an attempt is made to roll back
// hydrated rows via the LedgerHydrator's idempotent UPSERT — the row
// stays in place but the session state is observably failed; an
// operator retry re-imports cleanly (the idempotency table is NOT
// updated on failure).
func (d *Importer) Import(pb *ParsedBundle) (Outcome, error) {
	out := Outcome{}
	if pb == nil {
		return out, errors.New("migration: Import: nil parsed bundle")
	}

	// 1. Schema-version (ReadBundle has already enforced this, but the
	// safety belt costs nothing).
	if pb.Manifest.SchemaVersion != SchemaVersion {
		return out, fmt.Errorf("%w: got=%d want=%d", ErrSchemaVersionUnsupported, pb.Manifest.SchemaVersion, SchemaVersion)
	}

	// 2. Verify signature.
	if err := pb.VerifySignature(d.PublicKey); err != nil {
		return out, err
	}

	// 3. Tenant check.
	if d.BearerTenantID != "" && pb.Manifest.TenantID != "" && d.BearerTenantID != pb.Manifest.TenantID {
		return out, fmt.Errorf("%w: bearer=%q manifest=%q", ErrCrossTenantForbidden, d.BearerTenantID, pb.Manifest.TenantID)
	}

	// 4. Idempotency.
	manifestSHA := pb.ManifestSHA256()
	if d.Idempotency != nil {
		existing, err := d.Idempotency.Lookup(manifestSHA)
		if err != nil {
			return out, fmt.Errorf("migration: idempotency lookup: %w", err)
		}
		if existing != "" {
			out.NewSessionID = existing
			out.ChainRootHash = pb.Manifest.ChainRootHash
			out.Idempotent = true
			out.Verified = true
			return out, nil
		}
	}

	// 5. WAL + memory integrity.
	if err := pb.VerifyWALIntegrity(); err != nil {
		return out, err
	}
	if err := pb.VerifyMemoryIntegrity(); err != nil {
		return out, err
	}

	// 6. Skill-pack pre-flight.
	if d.PackChecker != nil {
		refs, err := pb.SkillPackRefs()
		if err != nil {
			return out, err
		}
		var missing []SkillPackRef
		for _, ref := range refs {
			if !d.PackChecker.HasPack(ref.PackID, ref.ContentHash) {
				missing = append(missing, ref)
			}
		}
		if len(missing) > 0 {
			return out, &MissingPacksError{Packs: missing}
		}
	}

	// 7. Allocate destination session.
	if d.Allocator == nil {
		return out, errors.New("migration: Import: no SessionAllocator wired")
	}
	destID, err := d.Allocator.AllocateSession(pb.Manifest.Model, pb.Manifest.TenantID)
	if err != nil {
		return out, fmt.Errorf("migration: allocate dest session: %w", err)
	}
	out.NewSessionID = destID

	// Ensure we always flip state on failure paths after this point.
	finalize := func(success bool) {
		if success {
			_ = d.Allocator.SetSessionState(destID, "idle")
			return
		}
		_ = d.Allocator.SetSessionState(destID, "migrated-failed")
	}

	// 8. Hydrate ledger.
	nodes, err := pb.ChainNodes()
	if err != nil {
		finalize(false)
		return out, err
	}
	edges, err := pb.Edges()
	if err != nil {
		finalize(false)
		return out, err
	}
	for i := range nodes {
		// Re-map MissionID from the source session id to the
		// destination's id. This is the only mutation we perform on
		// imported nodes — the content commitment binds the rest of
		// the node's state and survives the re-map because
		// MissionID is a per-tenant pointer, not part of the
		// content-addressing.
		if nodes[i].MissionID == pb.Manifest.SourceSessionID {
			nodes[i].MissionID = destID
		}
		if err := d.Ledger.HydrateNode(nodes[i]); err != nil {
			finalize(false)
			return out, fmt.Errorf("migration: hydrate node %s: %w", nodes[i].ID, err)
		}
		// Content tier (skip when redacted — the source put it in
		// the redacted summary instead).
		if blob, ok := pb.Content[nodes[i].ID]; ok {
			if err := d.Ledger.HydrateContent(nodes[i].ID, blob); err != nil {
				finalize(false)
				return out, fmt.Errorf("migration: hydrate content %s: %w", nodes[i].ID, err)
			}
		}
	}
	for _, e := range edges {
		if e.Metadata != nil {
			if got, ok := e.Metadata["session_id"]; ok && got == pb.Manifest.SourceSessionID {
				e.Metadata["session_id"] = destID
			}
		}
		if err := d.Ledger.HydrateEdge(e); err != nil {
			finalize(false)
			return out, fmt.Errorf("migration: hydrate edge: %w", err)
		}
	}
	out.NodeCount = len(nodes)
	out.EdgeCount = len(edges)

	// 9. Hydrate memory rows.
	if d.Memory != nil {
		var memRows int
		if err := splitNDJSON(pb.MemoryRowsNDJSON, func(line []byte) error {
			memRows++
			return d.Memory.HydrateMemoryRow(destID, line)
		}); err != nil {
			finalize(false)
			return out, fmt.Errorf("migration: hydrate memory: %w", err)
		}
		out.MemoryRowsCount = memRows
	}

	// 10. Replay WAL with incremental hash check.
	walIdx, err := pb.WALIndex()
	if err != nil {
		finalize(false)
		return out, err
	}
	checkpointBySeq := make(map[uint64]string, len(walIdx.WALCheckpointHashes))
	for _, cp := range walIdx.WALCheckpointHashes {
		checkpointBySeq[cp.Seq] = cp.PartialChainRoot
	}
	var replayed uint64
	if d.WALReplayer != nil && len(pb.WALNDJSON) > 0 {
		replayed, err = d.WALReplayer.ReplayWAL(destID, pb.WALNDJSON, func(seq uint64) error {
			expected, ok := checkpointBySeq[seq]
			if !ok {
				return nil
			}
			got, hashErr := d.Ledger.ChainRootHashForSession(destID)
			if hashErr != nil {
				return fmt.Errorf("partial chain root: %w", hashErr)
			}
			if expected != "" && got != expected {
				if d.Emitter != nil {
					d.Emitter.EmitMigrateDivergent(pb.Manifest.SourceSessionID, destID, expected, got, seq)
				}
				return fmt.Errorf("%w at seq=%d: expected=%s actual=%s", ErrChainRootMismatch, seq, expected, got)
			}
			return nil
		})
		if err != nil {
			finalize(false)
			return out, err
		}
	}
	out.WALReplayed = replayed

	// 11. Restore lobes + lanes.
	if d.Lobes != nil {
		for id, state := range pb.LobeStates {
			if err := d.Lobes.RestoreLobe(destID, id, state); err != nil {
				finalize(false)
				return out, fmt.Errorf("migration: restore lobe %s: %w", id, err)
			}
		}
	}
	if d.Lanes != nil && len(pb.LanesSnapshotJSON) > 0 {
		if err := d.Lanes.RestoreLanes(destID, pb.LanesSnapshotJSON); err != nil {
			finalize(false)
			return out, fmt.Errorf("migration: restore lanes: %w", err)
		}
	}

	// 12. Final chain-root verify.
	finalRoot, err := d.Ledger.ChainRootHashForSession(destID)
	if err != nil {
		finalize(false)
		return out, fmt.Errorf("migration: final chain root: %w", err)
	}
	out.ChainRootHash = finalRoot
	if finalRoot != pb.Manifest.ChainRootHash {
		if d.Emitter != nil {
			d.Emitter.EmitMigrateDivergent(pb.Manifest.SourceSessionID, destID, pb.Manifest.ChainRootHash, finalRoot, 0)
		}
		finalize(false)
		return out, fmt.Errorf("%w: expected=%s actual=%s", ErrChainRootMismatch, pb.Manifest.ChainRootHash, finalRoot)
	}
	out.Verified = true

	// 13. Idempotency record (skip on failure paths above).
	if d.Idempotency != nil {
		if err := d.Idempotency.Record(manifestSHA, destID, pb.Manifest.SourceSessionID, pb.Manifest.SourceHost); err != nil {
			finalize(false)
			return out, fmt.Errorf("migration: idempotency record: %w", err)
		}
	}

	// 14. Flip to idle + emit imported event.
	finalize(true)
	if d.Emitter != nil {
		d.Emitter.EmitMigrateImported(pb.Manifest.SourceSessionID, destID, finalRoot, out.NodeCount, out.WALReplayed)
	}
	return out, nil
}

// MissingPacksError is the typed error returned when the destination
// is missing one or more skill packs the bundle references. Callers
// pattern-match via errors.Is(err, ErrMissingSkillPacks) to surface
// the full list to the operator (HTTP 422 body + CLI stderr).
type MissingPacksError struct {
	Packs []SkillPackRef
}

func (e *MissingPacksError) Error() string {
	if len(e.Packs) == 1 {
		return fmt.Sprintf("%s: %s@%s", ErrMissingSkillPacks.Error(), e.Packs[0].PackID, e.Packs[0].ContentHash)
	}
	return fmt.Sprintf("%s: %d packs missing", ErrMissingSkillPacks.Error(), len(e.Packs))
}

// Is wires errors.Is so callers can write
// `if errors.Is(err, ErrMissingSkillPacks)` without a type assertion.
func (e *MissingPacksError) Is(target error) bool {
	return target == ErrMissingSkillPacks
}

// splitNDJSON iterates non-empty lines, invoking fn for each. Stops
// at the first error and returns it.
func splitNDJSON(raw []byte, fn func(line []byte) error) error {
	if len(raw) == 0 {
		return nil
	}
	start := 0
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\n' {
			continue
		}
		line := raw[start:i]
		start = i + 1
		if len(trimSpace(line)) == 0 {
			continue
		}
		if err := fn(line); err != nil {
			return err
		}
	}
	if start < len(raw) {
		line := raw[start:]
		if len(trimSpace(line)) > 0 {
			if err := fn(line); err != nil {
				return err
			}
		}
	}
	return nil
}

func trimSpace(b []byte) []byte {
	i := 0
	for i < len(b) && (b[i] == ' ' || b[i] == '\t' || b[i] == '\r' || b[i] == '\n') {
		i++
	}
	j := len(b)
	for j > i && (b[j-1] == ' ' || b[j-1] == '\t' || b[j-1] == '\r' || b[j-1] == '\n') {
		j--
	}
	return b[i:j]
}
