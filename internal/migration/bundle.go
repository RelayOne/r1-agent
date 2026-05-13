// Package migration implements the .r1session bundle format and the
// export/import primitives for cross-machine session migration.
//
// Spec: specs/cross-machine-session-migration.md (C1).
//
// The bundle is a gzip-tar archive containing every byte required to
// resume a session on a different daemon: per-session ledger nodes +
// edges + content blobs, the bus WAL, memory rows scoped to the
// session, skill-pack content-hash refs, deterministic Lobe / lanes
// snapshots, a pre-export checkpoint, and an Ed25519 signature over a
// canonical manifest body computed via
// ledger.CanonicalManifestSignBody — the same canonical body the
// tracebundle v2 path signs, so downstream verifiers reuse a single
// code path.
//
// Skill-pack continuity contract: the bundle records pack refs
// (pack_id + content_hash + version), NEVER the pack bodies. The
// destination operator pre-stages packs via `r1 skills pack install`
// before importing. Missing-pack imports fail closed with the full
// list of (pack_id, content_hash) pairs the operator must install —
// internal/skill.Registry's content-addressed install path is the
// canonical staging mechanism. The migration package itself only
// records the contract via the BundleSource.SkillPackRefs() return.
//
// Key-material contract: encrypted memory rows + encrypted ledger
// content blobs travel byte-for-byte. The destination daemon decrypts
// using its locally-held master key. If the two daemons disagree on
// master key material, decryption fails on first read and import
// aborts with key_material_mismatch. Cross-tenant migration is
// forbidden in v1 (see spec §10).
//
// All archive entries appear in a deterministic, fixed order so a
// byte-for-byte round-trip is observable (T22). The manifest +
// signature are emitted LAST so the writer can stream every data file
// before computing the SHA-256 inputs for the manifest's wal_sha256 /
// memory_sha256 fields. Stdlib only — archive/tar + compress/gzip +
// encoding/json + crypto/ed25519 + crypto/sha256 (no third-party
// deps; matches the tracebundle export constraint).
package migration

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/RelayOne/r1/internal/ledger"
)

// FormatTag is the discriminator written into manifest.Format and
// fed to ledger.CanonicalManifestSignBody. Kept distinct from the
// tracebundle's "tracebundle" tag so cross-format confusion (signing
// a tracebundle's body as a migration bundle or vice-versa) is
// detectable on verification.
const FormatTag = "r1session"

// SchemaVersion is the bundle schema version. Bumping requires a
// reader-side migration path; v1 readers reject any other value.
const SchemaVersion = 1

// Fixed archive paths. The order constants are written in writeBundle
// is the canonical order — readers don't require any particular
// order, but the writer's determinism is the property the
// idempotency / round-trip tests verify.
const (
	PathManifest         = "manifest.json"
	PathSignature        = "signature.ed25519"
	PathLedgerChain      = "ledger/chain.ndjson"
	PathLedgerEdges      = "ledger/edges.ndjson"
	PathLedgerContentDir = "ledger/content/"
	PathLedgerRedacted   = "ledger/content/redacted.json"
	PathBusWAL           = "bus/wal.ndjson"
	PathBusWALIndex      = "bus/wal.index.json"
	PathMemoryRows       = "memory/rows.ndjson"
	PathMemoryIndex      = "memory/index.json"
	PathSkillPackRefs    = "skills/pack-refs.json"
	PathLobeStateDir     = "lobe-state/"
	PathLanesSnapshot    = "lanes/snapshot.json"
	PathCheckpoint       = "checkpoint/pre-export.json"
)

// WALCheckpoint is one (seq, partial_chain_root) entry in the WAL
// index. The export side records these every N events; the import
// side recomputes the destination's partial chain root at the same
// seq boundaries and compares — any divergence is data corruption
// (T7 + T14).
type WALCheckpoint struct {
	Seq              uint64 `json:"seq"`
	PartialChainRoot string `json:"partial_chain_root"`
}

// WALIndex is the bus/wal.index.json payload. Carries fast metadata
// (count + sha256) plus the partial-chain-root checkpoints described
// in spec §7.1 step 4 and T7.
type WALIndex struct {
	FirstSeq             uint64          `json:"first_seq"`
	LastSeq              uint64          `json:"last_seq"`
	Count                uint64          `json:"count"`
	SHA256               string          `json:"sha256"`
	WALCheckpointHashes  []WALCheckpoint `json:"wal_checkpoint_hashes,omitempty"`
}

// MemoryIndex is the memory/index.json payload. Tracks the memory
// row count + scope targets seen, plus a sha256 over the rows file
// bytes so the import side can detect on-disk corruption between
// export and import.
type MemoryIndex struct {
	Count        int      `json:"count"`
	ScopeTargets []string `json:"scope_targets,omitempty"`
	SHA256       string   `json:"sha256"`
}

// SkillPackRef is one entry of skills/pack-refs.json. Carries the
// pack identity (pack_id + content_hash) the destination must have
// pre-installed. Bodies are NOT shipped — pre-stage with
// `r1 skills pack install <pack_id>@<content_hash>` on the
// destination before import.
type SkillPackRef struct {
	PackID      string `json:"pack_id"`
	ContentHash string `json:"content_hash"`
	Version     string `json:"version,omitempty"`
}

// RedactedEntry mirrors the tracebundle's content/redacted.json
// shape so any verifier that already understands the tracebundle
// format can read a migration bundle's redacted summary unchanged.
type RedactedEntry struct {
	NodeID            string   `json:"node_id"`
	RedactionEventIDs []string `json:"redaction_event_ids,omitempty"`
}

// RedactedSummary is the ledger/content/redacted.json payload.
type RedactedSummary struct {
	Redacted []RedactedEntry `json:"redacted"`
}

// Manifest is the bundle's manifest.json payload. Field order is
// the on-disk canonical order; encoding/json writes struct fields in
// declaration order, giving a stable canonical form.
//
// SignatureHex is omitempty so an unsigned manifest is detectable
// at parse time (verification fails closed in ReadAndVerifyManifest).
type Manifest struct {
	Format          string         `json:"format"`
	SchemaVersion   int            `json:"schema_version"`
	Version         int            `json:"version"`
	SourceHost      string         `json:"source_host"`
	SourceDaemonID  string         `json:"source_daemon_id"`
	SourceSessionID string         `json:"source_session_id"`
	TenantID        string         `json:"tenant_id,omitempty"`
	ExportedAt      string         `json:"exported_at"`
	ChainRootHash   string         `json:"chain_root_hash"`
	WALSHA256       string         `json:"wal_sha256"`
	MemorySHA256    string         `json:"memory_sha256"`
	NodeCount       int            `json:"node_count"`
	EdgeCount       int            `json:"edge_count"`
	WALFirstSeq     uint64         `json:"wal_first_seq"`
	WALLastSeq      uint64         `json:"wal_last_seq"`
	Model           string         `json:"model,omitempty"`
	SkillPackRefs   []SkillPackRef `json:"skill_pack_refs,omitempty"`
	Signer          string         `json:"signer"`
	SignatureHex    string         `json:"signature_hex,omitempty"`
}

// SignerFingerHex returns a stable 12-char hex prefix of sha256 of
// the supplied public key, matching ledger/redact_sign.go's
// keyFingerHex idiom. Stamped into Manifest.Signer so multiple keys
// can coexist in the same audit trail across rotations. Exported
// because both the bundle writer AND the daemon's signing wiring
// need it; callers MUST pass the same value they used at
// SignManifest time when verifying.
func SignerFingerHex(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:6])
}

// CanonicalSignBody returns the bytes ed25519.Sign / ed25519.Verify
// operate on. Delegates to ledger.CanonicalManifestSignBody so the
// migration bundle and tracebundle paths share one canonical form —
// any downstream verifier wired to check tracebundle signatures
// verifies migration manifests with zero new code.
//
// The body INCLUDES the format tag ("r1session") so the same key
// signing both formats can't have one bundle re-interpreted as the
// other (the tracebundle uses "tracebundle" + version 2; migration
// uses "r1session" + version 1 here).
func (m *Manifest) CanonicalSignBody() []byte {
	return ledger.CanonicalManifestSignBody(
		FormatTag,
		m.SchemaVersion,
		m.SourceSessionID,
		m.ChainRootHash,
		m.ExportedAt,
		m.Signer,
	)
}

// SignManifest stamps Signer (fingerprint derived elsewhere by the
// caller — typically the signer fingerprint from
// ledger.LoadOrGenerateSigningKey) and SignatureHex onto m, then
// returns the manifest unchanged. The caller is responsible for
// supplying a matching signer string and an ed25519.PrivateKey whose
// public key the destination's key material agrees with.
func SignManifest(m *Manifest, signer string, priv ed25519.PrivateKey) error {
	if len(priv) != ed25519.PrivateKeySize {
		return errors.New("migration: invalid private key")
	}
	if signer == "" {
		return errors.New("migration: signer fingerprint required")
	}
	m.Signer = signer
	m.SignatureHex = ""
	body := m.CanonicalSignBody()
	sig := ed25519.Sign(priv, body)
	m.SignatureHex = hex.EncodeToString(sig)
	return nil
}

// VerifyManifest checks m.SignatureHex against m's canonical body
// using pub. Returns ErrBundleUnsigned when SignatureHex is empty,
// ErrSignatureMismatch otherwise. Mirrors ledger.VerifyRecord's
// contract so verifier wrappers can pattern-match a single error
// taxonomy.
func VerifyManifest(m *Manifest, pub ed25519.PublicKey) error {
	if m.SignatureHex == "" {
		return ErrBundleUnsigned
	}
	if len(pub) != ed25519.PublicKeySize {
		return errors.New("migration: invalid public key")
	}
	sig, err := hex.DecodeString(m.SignatureHex)
	if err != nil {
		return fmt.Errorf("%w: hex decode: %v", ErrSignatureMismatch, err)
	}
	body := m.CanonicalSignBody()
	if !ed25519.Verify(pub, body, sig) {
		return ErrSignatureMismatch
	}
	return nil
}

// Bundle-level sentinel errors. Callers pattern-match via errors.Is
// to map import failures onto HTTP status codes (400/422/500 per
// spec §6.2).
var (
	// ErrBundleInvalid wraps every "bundle parse / structure / signature"
	// failure that warrants a 400 response. Distinct from
	// ErrChainRootMismatch (a 422) and ErrSchemaVersionUnsupported (also
	// 400 but with a different body shape).
	ErrBundleInvalid = errors.New("migration: bundle invalid")

	// ErrSchemaVersionUnsupported fires when manifest.SchemaVersion is
	// not the build's supported version. Maps to 400 with body
	// {"error":"schema_version_unsupported","got":N,"want":1}.
	ErrSchemaVersionUnsupported = errors.New("migration: schema version unsupported")

	// ErrSignatureMismatch fires when the manifest's signature does not
	// validate against the destination's public key. Includes the case
	// where the destination's key material differs from the source's.
	ErrSignatureMismatch = errors.New("migration: signature mismatch")

	// ErrBundleUnsigned fires when SignatureHex is empty. v1 refuses
	// to import unsigned bundles — the source MUST hold a signing key.
	ErrBundleUnsigned = errors.New("migration: bundle is unsigned")

	// ErrChainRootMismatch fires when the post-replay chain root hash
	// does not match the manifest's. Maps to 422 with
	// {"error":"chain_root_hash_mismatch","expected":...,"actual":...,
	//   "divergent_at_seq":N}.
	ErrChainRootMismatch = errors.New("migration: chain root hash mismatch")

	// ErrMissingSkillPacks fires when the destination is missing any
	// skill pack referenced by the bundle. Maps to 422 with the full
	// missing list so the operator can pre-stage with
	// `r1 skills pack install <pack_id>@<content_hash>`.
	ErrMissingSkillPacks = errors.New("migration: missing skill packs on destination")

	// ErrKeyMaterialMismatch fires when memory / content decryption
	// fails on first read. The destination's master key disagrees with
	// the source's — cross-tenant migration is forbidden in v1.
	ErrKeyMaterialMismatch = errors.New("migration: key material mismatch")

	// ErrSessionBusy fires from BeginMigrateOut when the source session
	// is mid-turn and --force is not set. Maps to 409.
	ErrSessionBusy = errors.New("migration: session busy")

	// ErrUnsignedRedactionsPresent fires from BeginMigrateOut when the
	// source session carries legacy unsigned redactions (spec §8). Maps
	// to 409 with the offending node IDs.
	ErrUnsignedRedactionsPresent = errors.New("migration: unsigned redactions present")

	// ErrCrossTenantForbidden fires when the bearer's tenant claim does
	// not match the manifest's tenant_id. v1 only supports same-tenant
	// migration. Maps to 403.
	ErrCrossTenantForbidden = errors.New("migration: cross-tenant migration forbidden")
)

// BundleSource is the data the export side needs to produce. Defined
// as an interface so the cmd/r1-server handler can supply the live
// daemon's data while unit tests fixture in-memory shapes (matches
// the tracebundle's TracebundleSource pattern).
//
// Each method may be called once during WriteBundle. The interface
// does NOT include a Close — callers manage lifetime of the
// underlying ledger / WAL / memory store.
type BundleSource interface {
	// SourceHost returns the daemon's stable host id (typically
	// hostname or a configured host_id). Embedded in the manifest as
	// source_host.
	SourceHost() string

	// SourceDaemonID returns the daemon's UUID (per-process / per-host
	// stable). Embedded in the manifest as source_daemon_id.
	SourceDaemonID() string

	// SourceSessionID returns the session id being exported.
	SourceSessionID() string

	// TenantID returns the tenant scope of the session. v1 cross-tenant
	// migration is forbidden, so this MUST match the destination's
	// bearer tenant claim on import. Empty string is treated as the
	// default tenant.
	TenantID() string

	// Model returns the provider/model id active on the session at
	// export time. Informational; not used by the import path beyond
	// surfacing in the destination's session metadata.
	Model() string

	// ChainRootHash returns the source's
	// ledger.ChainRootHashForSession(id) value at export quiet-point.
	// Stamped into the manifest and re-verified post-replay.
	ChainRootHash() string

	// ChainNodes returns every chain-tier node belonging to the
	// session, in deterministic ledger order. Each line of
	// ledger/chain.ndjson is the JSON encoding of one element.
	ChainNodes() []ledger.Node

	// Edges returns every edge belonging to the session. Same
	// deterministic ordering.
	Edges() []ledger.Edge

	// Content returns the raw content-tier bytes for one node. The
	// returned bytes are written verbatim into
	// ledger/content/<nodeID>.json — encryption envelopes (if any) are
	// preserved byte-for-byte; the destination's keyring decrypts on
	// read with its locally-held master key.
	Content(nodeID string) ([]byte, error)

	// IsRedacted reports whether a node's content tier has been
	// redacted. Redacted nodes contribute one entry to
	// ledger/content/redacted.json and no content/<id>.json blob.
	IsRedacted(nodeID string) bool

	// RedactionEventIDs returns the IDs of the redaction events that
	// applied to the node (empty if none). Folded into the redacted
	// summary so the destination's audit chain links each redaction
	// back to its signed event.
	RedactionEventIDs(nodeID string) []string

	// BusWAL returns the bytes of the session's bus WAL from session
	// start (seq=0) to the export quiet point. The reader MUST stream
	// in seq order — replay-correctness depends on it.
	BusWAL() ([]byte, error)

	// BusWALSeqRange returns (firstSeq, lastSeq) of the WAL bytes
	// returned by BusWAL. Used to populate manifest.WALFirstSeq /
	// WALLastSeq without re-parsing the WAL.
	BusWALSeqRange() (uint64, uint64)

	// BusWALCheckpoints returns the partial-chain-root checkpoints
	// recorded every N events during the source's WAL emission
	// (spec T7). Empty slice = no checkpoints (the destination then
	// only does the final-state chain-root verify, per T13).
	BusWALCheckpoints() []WALCheckpoint

	// MemoryRows returns the bytes of memory/rows.ndjson — one JSON
	// row per line, scope_target = "session:<id>". Encrypted DEK
	// envelopes preserved byte-for-byte.
	MemoryRows() ([]byte, error)

	// MemoryRowCount returns the row count for the manifest's
	// memory_sha256 sibling field. Computed once by the source to
	// avoid re-parsing.
	MemoryRowCount() int

	// MemoryScopeTargets returns the distinct scope_target values
	// observed across the memory rows. Empty slice = no rows.
	MemoryScopeTargets() []string

	// SkillPackRefs returns the loaded skill packs' (pack_id,
	// content_hash, version) refs. Bodies are NEVER shipped — see
	// package doc and spec §9.
	SkillPackRefs() []SkillPackRef

	// LobeStates returns per-lobe deterministic snapshots. Map key =
	// lobe_id (filename becomes lobe-state/<lobe_id>.json). Empty map
	// = no lobe state to ship (chat-only session).
	LobeStates() (map[string][]byte, error)

	// LanesSnapshot returns the lanes/snapshot.json payload (per
	// spec lanes-protocol.md r1.lanes.get since_seq=0). Empty bytes
	// = no lane state.
	LanesSnapshot() ([]byte, error)

	// CheckpointBytes returns the pre-export checkpoint payload (per
	// internal/checkpoint.Checkpoint marshaled as JSON). Empty bytes
	// = no checkpoint captured (the import side then skips
	// restoration but still proceeds).
	CheckpointBytes() ([]byte, error)
}

// WriteBundle streams a complete .r1session archive into w. The
// writer:
//
//  1. Computes SHA-256 over the bus WAL bytes and memory rows bytes
//     so the manifest's wal_sha256 / memory_sha256 fields are
//     populated before signing.
//  2. Allocates the manifest with every count / hash known.
//  3. Signs the manifest via SignManifest(signer, priv).
//  4. Emits every archive entry in the canonical order — content
//     blobs come AFTER the chain / edges (so a streaming reader has
//     the chain index before any content), and manifest +
//     signature come LAST (so signed-hash computation happens after
//     all data files are committed).
//
// On any write error WriteBundle returns the error verbatim; the
// caller's gzip / tar writer is still flushed (best-effort) via the
// deferred close pattern so partial archives are observable.
func WriteBundle(w io.Writer, src BundleSource, signer string, priv ed25519.PrivateKey, exportedAt time.Time) error {
	if src == nil {
		return errors.New("migration: WriteBundle: nil source")
	}
	if len(priv) != ed25519.PrivateKeySize {
		return errors.New("migration: WriteBundle: invalid private key")
	}

	gz := gzip.NewWriter(w)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	// 1. ledger/chain.ndjson
	chain := src.ChainNodes()
	chainBytes, err := encodeNDJSONNodes(chain)
	if err != nil {
		return fmt.Errorf("migration: encode chain: %w", err)
	}
	if err := writeTarFile(tw, PathLedgerChain, chainBytes); err != nil {
		return err
	}

	// 2. ledger/edges.ndjson
	edges := src.Edges()
	edgeBytes, err := encodeNDJSONEdges(edges)
	if err != nil {
		return fmt.Errorf("migration: encode edges: %w", err)
	}
	if err := writeTarFile(tw, PathLedgerEdges, edgeBytes); err != nil {
		return err
	}

	// 3. ledger/content/<id>.json blobs + redacted.json
	redacted := RedactedSummary{Redacted: []RedactedEntry{}}
	for _, n := range chain {
		if src.IsRedacted(n.ID) {
			redacted.Redacted = append(redacted.Redacted, RedactedEntry{
				NodeID:            n.ID,
				RedactionEventIDs: src.RedactionEventIDs(n.ID),
			})
			continue
		}
		blob, err := src.Content(n.ID)
		if err != nil {
			return fmt.Errorf("migration: content %s: %w", n.ID, err)
		}
		if err := writeTarFile(tw, PathLedgerContentDir+n.ID+".json", blob); err != nil {
			return err
		}
	}
	redactedBytes, err := json.MarshalIndent(redacted, "", "  ")
	if err != nil {
		return fmt.Errorf("migration: encode redacted: %w", err)
	}
	if err := writeTarFile(tw, PathLedgerRedacted, redactedBytes); err != nil {
		return err
	}

	// 4. bus/wal.ndjson + wal.index.json
	walBytes, err := src.BusWAL()
	if err != nil {
		return fmt.Errorf("migration: bus wal: %w", err)
	}
	if err := writeTarFile(tw, PathBusWAL, walBytes); err != nil {
		return err
	}
	walSum := sha256.Sum256(walBytes)
	walSHA := hex.EncodeToString(walSum[:])
	firstSeq, lastSeq := src.BusWALSeqRange()
	walIdx := WALIndex{
		FirstSeq:            firstSeq,
		LastSeq:             lastSeq,
		Count:               countNDJSONLines(walBytes),
		SHA256:              walSHA,
		WALCheckpointHashes: src.BusWALCheckpoints(),
	}
	walIdxBytes, err := json.MarshalIndent(walIdx, "", "  ")
	if err != nil {
		return fmt.Errorf("migration: encode wal index: %w", err)
	}
	if err := writeTarFile(tw, PathBusWALIndex, walIdxBytes); err != nil {
		return err
	}

	// 5. memory/rows.ndjson + index.json
	memBytes, err := src.MemoryRows()
	if err != nil {
		return fmt.Errorf("migration: memory rows: %w", err)
	}
	if err := writeTarFile(tw, PathMemoryRows, memBytes); err != nil {
		return err
	}
	memSum := sha256.Sum256(memBytes)
	memSHA := hex.EncodeToString(memSum[:])
	memIdx := MemoryIndex{
		Count:        src.MemoryRowCount(),
		ScopeTargets: src.MemoryScopeTargets(),
		SHA256:       memSHA,
	}
	memIdxBytes, err := json.MarshalIndent(memIdx, "", "  ")
	if err != nil {
		return fmt.Errorf("migration: encode memory index: %w", err)
	}
	if err := writeTarFile(tw, PathMemoryIndex, memIdxBytes); err != nil {
		return err
	}

	// 6. skills/pack-refs.json
	packRefs := src.SkillPackRefs()
	if packRefs == nil {
		packRefs = []SkillPackRef{}
	}
	// Sort by pack_id so two exports of the same set produce
	// byte-identical bytes (T23 determinism).
	sort.Slice(packRefs, func(i, j int) bool {
		if packRefs[i].PackID == packRefs[j].PackID {
			return packRefs[i].ContentHash < packRefs[j].ContentHash
		}
		return packRefs[i].PackID < packRefs[j].PackID
	})
	packBytes, err := json.MarshalIndent(packRefs, "", "  ")
	if err != nil {
		return fmt.Errorf("migration: encode pack refs: %w", err)
	}
	if err := writeTarFile(tw, PathSkillPackRefs, packBytes); err != nil {
		return err
	}

	// 7. lobe-state/<lobe_id>.json blobs (sorted by lobe_id for
	// determinism).
	lobeStates, err := src.LobeStates()
	if err != nil {
		return fmt.Errorf("migration: lobe states: %w", err)
	}
	lobeIDs := make([]string, 0, len(lobeStates))
	for id := range lobeStates {
		lobeIDs = append(lobeIDs, id)
	}
	sort.Strings(lobeIDs)
	for _, id := range lobeIDs {
		if err := writeTarFile(tw, PathLobeStateDir+id+".json", lobeStates[id]); err != nil {
			return err
		}
	}

	// 8. lanes/snapshot.json
	lanesBytes, err := src.LanesSnapshot()
	if err != nil {
		return fmt.Errorf("migration: lanes snapshot: %w", err)
	}
	if lanesBytes == nil {
		lanesBytes = []byte("{}")
	}
	if err := writeTarFile(tw, PathLanesSnapshot, lanesBytes); err != nil {
		return err
	}

	// 9. checkpoint/pre-export.json
	cpBytes, err := src.CheckpointBytes()
	if err != nil {
		return fmt.Errorf("migration: checkpoint: %w", err)
	}
	if cpBytes == nil {
		cpBytes = []byte("{}")
	}
	if err := writeTarFile(tw, PathCheckpoint, cpBytes); err != nil {
		return err
	}

	// 10. manifest.json + signature.ed25519 (LAST so all hashes are
	// final). ExportedAt is supplied by the caller (rather than
	// time.Now) so tests can pin a deterministic timestamp for
	// byte-for-byte round-trip assertions.
	m := &Manifest{
		Format:          FormatTag,
		SchemaVersion:   SchemaVersion,
		Version:         1,
		SourceHost:      src.SourceHost(),
		SourceDaemonID:  src.SourceDaemonID(),
		SourceSessionID: src.SourceSessionID(),
		TenantID:        src.TenantID(),
		ExportedAt:      exportedAt.UTC().Format(time.RFC3339Nano),
		ChainRootHash:   src.ChainRootHash(),
		WALSHA256:       walSHA,
		MemorySHA256:    memSHA,
		NodeCount:       len(chain),
		EdgeCount:       len(edges),
		WALFirstSeq:     firstSeq,
		WALLastSeq:      lastSeq,
		Model:           src.Model(),
		SkillPackRefs:   packRefs,
	}
	if err := SignManifest(m, signer, priv); err != nil {
		return fmt.Errorf("migration: sign manifest: %w", err)
	}
	manifestBytes, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("migration: encode manifest: %w", err)
	}
	if err := writeTarFile(tw, PathManifest, manifestBytes); err != nil {
		return err
	}
	// signature.ed25519 carries the raw signature bytes (not hex) so
	// downstream tools that don't speak JSON can verify with just the
	// public key + the manifest body.
	sigRaw, err := hex.DecodeString(m.SignatureHex)
	if err != nil {
		return fmt.Errorf("migration: signature hex decode: %w", err)
	}
	return writeTarFile(tw, PathSignature, sigRaw)
}

// ReadBundle streams an entire archive into a ParsedBundle in memory.
// Bundles in v1 are bounded (a 1000-turn session ships ≤100MB per
// spec §12) so the in-memory pattern keeps the import path simple —
// the manifest + signature verification needs the entire archive
// present anyway. Returns ErrBundleInvalid on any parse failure with
// a wrapped detail.
//
// The reader does NOT call VerifyManifest on its own — callers
// (BundleImporter.Import) verify against their locally-derived
// public key as a separate, explicit step so the verification
// failure path is observable in error returns.
func ReadBundle(r io.Reader) (*ParsedBundle, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("%w: gzip: %v", ErrBundleInvalid, err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	pb := &ParsedBundle{
		Content:    make(map[string][]byte),
		LobeStates: make(map[string][]byte),
	}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: tar header: %v", ErrBundleInvalid, err)
		}
		// Skip directory entries; we never write any but be tolerant
		// in case a future writer emits some.
		if hdr.Typeflag == tar.TypeDir {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("%w: read %s: %v", ErrBundleInvalid, hdr.Name, err)
		}
		switch {
		case hdr.Name == PathManifest:
			pb.ManifestBytes = data
		case hdr.Name == PathSignature:
			pb.SignatureRaw = data
		case hdr.Name == PathLedgerChain:
			pb.ChainNDJSON = data
		case hdr.Name == PathLedgerEdges:
			pb.EdgesNDJSON = data
		case hdr.Name == PathLedgerRedacted:
			pb.RedactedJSON = data
		case hasPrefix(hdr.Name, PathLedgerContentDir):
			id := stripPrefix(hdr.Name, PathLedgerContentDir)
			if id == "redacted.json" {
				// Handled by the explicit case above; defensive.
				continue
			}
			id = stripSuffix(id, ".json")
			pb.Content[id] = data
		case hdr.Name == PathBusWAL:
			pb.WALNDJSON = data
		case hdr.Name == PathBusWALIndex:
			pb.WALIndexJSON = data
		case hdr.Name == PathMemoryRows:
			pb.MemoryRowsNDJSON = data
		case hdr.Name == PathMemoryIndex:
			pb.MemoryIndexJSON = data
		case hdr.Name == PathSkillPackRefs:
			pb.SkillPackRefsJSON = data
		case hasPrefix(hdr.Name, PathLobeStateDir):
			id := stripSuffix(stripPrefix(hdr.Name, PathLobeStateDir), ".json")
			pb.LobeStates[id] = data
		case hdr.Name == PathLanesSnapshot:
			pb.LanesSnapshotJSON = data
		case hdr.Name == PathCheckpoint:
			pb.CheckpointJSON = data
		}
	}
	if pb.ManifestBytes == nil {
		return nil, fmt.Errorf("%w: manifest.json missing", ErrBundleInvalid)
	}
	if err := json.Unmarshal(pb.ManifestBytes, &pb.Manifest); err != nil {
		return nil, fmt.Errorf("%w: parse manifest: %v", ErrBundleInvalid, err)
	}
	if pb.Manifest.Format != FormatTag {
		return nil, fmt.Errorf("%w: format=%q (want %q)", ErrBundleInvalid, pb.Manifest.Format, FormatTag)
	}
	if pb.Manifest.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("%w: got=%d want=%d", ErrSchemaVersionUnsupported, pb.Manifest.SchemaVersion, SchemaVersion)
	}
	return pb, nil
}

// ParsedBundle is the in-memory view of a .r1session archive.
// Returned by ReadBundle. All byte-slice fields hold the raw on-disk
// bytes — the importer decodes them as needed and recomputes hashes
// for verification.
type ParsedBundle struct {
	Manifest          Manifest
	ManifestBytes     []byte
	SignatureRaw      []byte
	ChainNDJSON       []byte
	EdgesNDJSON       []byte
	RedactedJSON      []byte
	Content           map[string][]byte
	WALNDJSON         []byte
	WALIndexJSON      []byte
	MemoryRowsNDJSON  []byte
	MemoryIndexJSON   []byte
	SkillPackRefsJSON []byte
	LobeStates        map[string][]byte
	LanesSnapshotJSON []byte
	CheckpointJSON    []byte
}

// ManifestSHA256 returns the canonical SHA-256 of the manifest bytes
// for the idempotency table (T10). The hash is computed over the
// EXACT bytes that appeared in the archive — not a re-marshaled
// canonical form — so the destination's "have I seen this before?"
// check is stable against any round-trip through json.Unmarshal +
// re-marshal that might rewrite whitespace.
func (pb *ParsedBundle) ManifestSHA256() string {
	if len(pb.ManifestBytes) == 0 {
		return ""
	}
	sum := sha256.Sum256(pb.ManifestBytes)
	return hex.EncodeToString(sum[:])
}

// VerifySignature checks the manifest's signature against pub. Mirrors
// VerifyManifest's contract but consumes the manifest fields plus the
// raw signature bytes (which signature.ed25519 carries) for the
// alternate signature path. Either path must succeed — the bundle is
// valid if EITHER the manifest's embedded SignatureHex verifies OR
// signature.ed25519 (raw bytes) verifies the manifest's canonical
// body. In practice the writer produces both with identical content,
// so a normal bundle passes both checks.
func (pb *ParsedBundle) VerifySignature(pub ed25519.PublicKey) error {
	// Try the embedded SignatureHex first.
	manifestVerify := VerifyManifest(&pb.Manifest, pub)
	if manifestVerify == nil {
		return nil
	}
	// Fall back to the raw signature.ed25519 bytes.
	if len(pb.SignatureRaw) == ed25519.SignatureSize {
		body := pb.Manifest.CanonicalSignBody()
		if ed25519.Verify(pub, body, pb.SignatureRaw) {
			return nil
		}
	}
	return manifestVerify
}

// VerifyWALIntegrity recomputes SHA-256 over pb.WALNDJSON and
// compares against pb.Manifest.WALSHA256. Detects on-disk corruption
// of the WAL bytes between export and import. Returns ErrBundleInvalid
// on mismatch.
func (pb *ParsedBundle) VerifyWALIntegrity() error {
	got := sha256.Sum256(pb.WALNDJSON)
	if hex.EncodeToString(got[:]) != pb.Manifest.WALSHA256 {
		return fmt.Errorf("%w: wal sha256 mismatch", ErrBundleInvalid)
	}
	return nil
}

// VerifyMemoryIntegrity recomputes SHA-256 over pb.MemoryRowsNDJSON
// and compares against pb.Manifest.MemorySHA256.
func (pb *ParsedBundle) VerifyMemoryIntegrity() error {
	got := sha256.Sum256(pb.MemoryRowsNDJSON)
	if hex.EncodeToString(got[:]) != pb.Manifest.MemorySHA256 {
		return fmt.Errorf("%w: memory sha256 mismatch", ErrBundleInvalid)
	}
	return nil
}

// SkillPackRefs unmarshals pb.SkillPackRefsJSON. Empty refs slice if
// the file was empty or missing.
func (pb *ParsedBundle) SkillPackRefs() ([]SkillPackRef, error) {
	if len(pb.SkillPackRefsJSON) == 0 {
		return nil, nil
	}
	var refs []SkillPackRef
	if err := json.Unmarshal(pb.SkillPackRefsJSON, &refs); err != nil {
		return nil, fmt.Errorf("%w: parse skill pack refs: %v", ErrBundleInvalid, err)
	}
	return refs, nil
}

// WALIndex unmarshals pb.WALIndexJSON.
func (pb *ParsedBundle) WALIndex() (WALIndex, error) {
	var idx WALIndex
	if len(pb.WALIndexJSON) == 0 {
		return idx, nil
	}
	if err := json.Unmarshal(pb.WALIndexJSON, &idx); err != nil {
		return idx, fmt.Errorf("%w: parse wal index: %v", ErrBundleInvalid, err)
	}
	return idx, nil
}

// ChainNodes iterates each NDJSON line in pb.ChainNDJSON and yields a
// ledger.Node. Malformed lines abort with ErrBundleInvalid.
func (pb *ParsedBundle) ChainNodes() ([]ledger.Node, error) {
	if len(pb.ChainNDJSON) == 0 {
		return nil, nil
	}
	var out []ledger.Node
	for _, line := range bytes.Split(pb.ChainNDJSON, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var n ledger.Node
		if err := json.Unmarshal(line, &n); err != nil {
			return nil, fmt.Errorf("%w: parse chain node: %v", ErrBundleInvalid, err)
		}
		out = append(out, n)
	}
	return out, nil
}

// Edges iterates each NDJSON line in pb.EdgesNDJSON.
func (pb *ParsedBundle) Edges() ([]ledger.Edge, error) {
	if len(pb.EdgesNDJSON) == 0 {
		return nil, nil
	}
	var out []ledger.Edge
	for _, line := range bytes.Split(pb.EdgesNDJSON, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var e ledger.Edge
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("%w: parse edge: %v", ErrBundleInvalid, err)
		}
		out = append(out, e)
	}
	return out, nil
}

// --- internal helpers ---

func encodeNDJSONNodes(nodes []ledger.Node) ([]byte, error) {
	var buf bytes.Buffer
	for _, n := range nodes {
		b, err := json.Marshal(n)
		if err != nil {
			return nil, fmt.Errorf("marshal node %s: %w", n.ID, err)
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}

func encodeNDJSONEdges(edges []ledger.Edge) ([]byte, error) {
	var buf bytes.Buffer
	for _, e := range edges {
		b, err := json.Marshal(e)
		if err != nil {
			return nil, fmt.Errorf("marshal edge: %w", err)
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}

func countNDJSONLines(raw []byte) uint64 {
	var n uint64
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if len(bytes.TrimSpace(line)) > 0 {
			n++
		}
	}
	return n
}

// writeTarFile writes one file entry into tw with a deterministic
// header: mode 0644, mtime zero (so two exports with otherwise
// identical inputs are byte-identical — T23). Zero mtime in tar
// format encodes as 1970-01-01T00:00:00Z which is the standard
// "no mtime preference" convention.
func writeTarFile(tw *tar.Writer, name string, body []byte) error {
	hdr := &tar.Header{
		Name:    name,
		Mode:    0o644,
		Size:    int64(len(body)),
		ModTime: time.Unix(0, 0).UTC(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("tar header %s: %w", name, err)
	}
	if _, err := tw.Write(body); err != nil {
		return fmt.Errorf("tar body %s: %w", name, err)
	}
	return nil
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func stripPrefix(s, prefix string) string {
	if hasPrefix(s, prefix) {
		return s[len(prefix):]
	}
	return s
}

func stripSuffix(s, suffix string) string {
	if len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix {
		return s[:len(s)-len(suffix)]
	}
	return s
}
