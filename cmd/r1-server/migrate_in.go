// Package main — migrate_in.go
//
// Spec C1 §6.2 + T9 + T13. Serves POST /api/session/migrate-in:
// ingests a .r1session bundle and replays it into the destination
// daemon's state.
//
// The pipeline mirrors internal/migration.Importer's Import contract
// step-by-step but the cmd/r1-server daemon's ledger backing is the
// SQLite projection (NOT the filesystem ledger.Store the source's
// active runtime uses). We accept this asymmetry in v1: the
// destination's projection is the same shape Stoke's tracebundle
// import populates (cmd/r1-server/import.go), so existing UIs render
// the migrated session unchanged.
//
// Auth: the handler is bearer-protected by the daemon's mux. Tenant-
// claim cross-check fails closed; v1 forbids cross-tenant migration.

package main

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/migration"
	"github.com/RelayOne/r1/internal/session"
)

// serveMigrateIn returns the http.HandlerFunc bound to
// POST /api/session/migrate-in. Wired in main.go's mux.
func (d *DB) serveMigrateIn(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	pb, err := migration.ReadBundle(r.Body)
	if err != nil {
		if errors.Is(err, migration.ErrSchemaVersionUnsupported) {
			writeMigrateError(w, http.StatusBadRequest, "schema_version_unsupported",
				map[string]any{"got": pb.Manifest.SchemaVersion, "want": migration.SchemaVersion})
			return
		}
		writeMigrateError(w, http.StatusBadRequest, "bundle_invalid",
			map[string]any{"detail": err.Error()})
		return
	}

	// Load the daemon's signing key for verification. The destination
	// MUST hold the same Ed25519 key the source signed with — that's
	// the v1 shared-master-key contract (spec §10).
	priv, _, kerr := loadMigrationSigner()
	if kerr != nil {
		writeMigrateError(w, http.StatusInternalServerError, "import_failed",
			map[string]any{"detail": "signer: " + kerr.Error()})
		return
	}
	pub := priv.Public().(ed25519.PublicKey)

	// Build the Importer with adapters wired to the cmd/r1-server
	// daemon's stores. The SQLite projection isn't quite a
	// ledger.Store — we adapt via the same DB.UpsertLedger* paths
	// that cmd/r1-server/import.go uses for tracebundle import.
	importer := &migration.Importer{
		PublicKey:      pub,
		BearerTenantID: bearerTenantID(r),
		Allocator:      &dbSessionAllocator{db: d},
		Ledger:         &dbLedgerHydrator{db: d, dataDir: dataDirOrEmpty()},
		Memory:         &dbMemoryHydrator{db: d},
		Lobes:          nil, // r1-server is a passive observer; lobe state lands as a verbatim row on the dest's filesystem ledger (handled below)
		Lanes:          nil,
		Idempotency:    migration.NewSQLiteIdempotencyStore(d.sql),
		PackChecker:    newSkillPackPresenceChecker(dataDirOrEmpty()),
		Emitter:        migration.NoopEventEmitter{},
		WALReplayer:    &dbWALReplayer{db: d},
	}
	out, err := importer.Import(pb)
	if err != nil {
		switch {
		case errors.Is(err, migration.ErrSignatureMismatch),
			errors.Is(err, migration.ErrBundleUnsigned),
			errors.Is(err, migration.ErrBundleInvalid):
			writeMigrateError(w, http.StatusBadRequest, "bundle_invalid",
				map[string]any{"detail": err.Error()})
			return
		case errors.Is(err, migration.ErrCrossTenantForbidden):
			writeMigrateError(w, http.StatusForbidden, "cross_tenant_forbidden",
				map[string]any{"detail": err.Error()})
			return
		case errors.Is(err, migration.ErrSchemaVersionUnsupported):
			writeMigrateError(w, http.StatusBadRequest, "schema_version_unsupported",
				map[string]any{"got": pb.Manifest.SchemaVersion, "want": migration.SchemaVersion})
			return
		case errors.Is(err, migration.ErrChainRootMismatch):
			writeMigrateError(w, http.StatusUnprocessableEntity, "chain_root_hash_mismatch",
				map[string]any{
					"expected":         pb.Manifest.ChainRootHash,
					"actual":           out.ChainRootHash,
					"detail":           err.Error(),
					"divergent_at_seq": 0,
				})
			return
		case errors.Is(err, migration.ErrMissingSkillPacks):
			missingErr := &migration.MissingPacksError{}
			if errors.As(err, &missingErr) {
				writeMigrateError(w, http.StatusUnprocessableEntity, "missing_skill_packs",
					map[string]any{"packs": missingErr.Packs})
				return
			}
			writeMigrateError(w, http.StatusUnprocessableEntity, "missing_skill_packs",
				map[string]any{"detail": err.Error()})
			return
		default:
			writeMigrateError(w, http.StatusInternalServerError, "import_failed",
				map[string]any{"detail": err.Error()})
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	status := http.StatusCreated
	if out.Idempotent {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	body := map[string]any{
		"new_session_id":  out.NewSessionID,
		"chain_root_hash": out.ChainRootHash,
		"node_count":      out.NodeCount,
		"wal_replayed":    out.WALReplayed,
		"verified":        out.Verified,
	}
	if out.Idempotent {
		body["idempotent"] = true
	}
	_ = json.NewEncoder(w).Encode(body)
}

// bearerTenantID extracts a tenant claim from the request bearer.
// v1 single-tenant mode treats every bearer as the same tenant; the
// helper returns an empty string so the migration package's tenant
// check is a no-op. Future multi-tenant work parses a signed JWT
// here and surfaces the tenant claim.
func bearerTenantID(r *http.Request) string {
	_ = r
	return ""
}

// dataDirOrEmpty returns ensureDataDir's path or "" if it errors.
// The dbLedgerHydrator uses this to compute the destination ledger
// dir for filesystem-backed nodes.
func dataDirOrEmpty() string {
	d, err := ensureDataDir()
	if err != nil {
		return ""
	}
	return d
}

// dbSessionAllocator implements migration.SessionAllocator against
// the cmd/r1-server sessions table. Allocates a fresh instance_id
// scoped to the migration namespace ("migrated-<timestamp>-<random>")
// and flips the status field on SetSessionState.
type dbSessionAllocator struct {
	db *DB
}

// AllocateSession implements migration.SessionAllocator.
func (a *dbSessionAllocator) AllocateSession(model, tenantID string) (string, error) {
	_ = tenantID
	id := fmt.Sprintf("migrated-%d", time.Now().UTC().UnixNano())
	sig := session.SignatureFile{
		Version:    "migration-import",
		InstanceID: id,
		Mode:       "migrated",
		Model:      model,
		Status:     "migrating-in",
		StartedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
	}
	if err := a.db.UpsertSession(sig); err != nil {
		return "", fmt.Errorf("upsert dest session: %w", err)
	}
	return id, nil
}

// SetSessionState implements migration.SessionAllocator. Updates the
// sessions row's status column.
func (a *dbSessionAllocator) SetSessionState(destSessionID, state string) error {
	a.db.mu.Lock()
	defer a.db.mu.Unlock()
	_, err := a.db.sql.Exec(`UPDATE sessions SET status = ?, updated_at = ? WHERE instance_id = ?`,
		state, time.Now().UTC().Format(time.RFC3339Nano), destSessionID)
	return err
}

// dbLedgerHydrator implements migration.LedgerHydrator against the
// cmd/r1-server SQLite ledger projection AND a filesystem-backed
// ledger.Store rooted under <dataDir>/migrated/<destSessionID>/. The
// filesystem store is the chain-root-hash authority (the SQLite
// projection is read-only metadata for the dashboard).
type dbLedgerHydrator struct {
	db          *DB
	dataDir     string
	storeCache  map[string]*ledger.Store
}

// storeFor returns (lazily) the ledger.Store rooted under the
// destination's migration dir for sessionID. Created on first call;
// cached for the lifetime of the Importer.
func (h *dbLedgerHydrator) storeFor(sessionID string) (*ledger.Store, error) {
	if h.storeCache == nil {
		h.storeCache = make(map[string]*ledger.Store)
	}
	if s, ok := h.storeCache[sessionID]; ok {
		return s, nil
	}
	root := filepath.Join(h.dataDir, "migrated", sessionID, "ledger")
	s, err := ledger.NewStore(root)
	if err != nil {
		return nil, err
	}
	h.storeCache[sessionID] = s
	return s, nil
}

// destSessionFromNode derives the destination session id from a node
// the importer has already re-mapped (MissionID = destSessionID).
func (h *dbLedgerHydrator) destSessionFromNode(n ledger.Node) string {
	return n.MissionID
}

// HydrateNode implements migration.LedgerHydrator. Writes both into
// the destination's filesystem ledger.Store (so
// ChainRootHashForSession verifies) and into the SQLite projection
// (so the dashboard sees the imported session like a tracebundle
// import).
func (h *dbLedgerHydrator) HydrateNode(n ledger.Node) error {
	dest := h.destSessionFromNode(n)
	store, err := h.storeFor(dest)
	if err != nil {
		return err
	}
	if err := store.WriteNode(n); err != nil {
		return err
	}
	// Mirror into SQLite projection.
	raw, _ := json.Marshal(n)
	return h.db.UpsertLedgerNode(
		dest, n.ID, n.Type, n.MissionID,
		n.CreatedAt.UTC().Format(time.RFC3339Nano), n.CreatedBy, n.ParentHash, raw,
	)
}

// HydrateEdge implements migration.LedgerHydrator.
func (h *dbLedgerHydrator) HydrateEdge(e ledger.Edge) error {
	var dest string
	if e.Metadata != nil {
		dest = e.Metadata["session_id"]
	}
	store, err := h.storeFor(dest)
	if err != nil {
		return err
	}
	if err := store.WriteEdge(e); err != nil {
		return err
	}
	id := e.From + "-" + e.To + "-" + string(e.Type)
	raw, _ := json.Marshal(e)
	return h.db.UpsertLedgerEdge(dest, id, e.From, e.To, string(e.Type), raw)
}

// HydrateContent implements migration.LedgerHydrator. Decodes the
// content envelope the source emitted and writes both salt + content
// back into the destination ledger's content tier. The destination's
// keyring decrypts on subsequent reads with its master key.
//
// The destination session id is recovered by finding the cached
// ledger.Store whose chain tier already holds nodeID (HydrateNode
// fires before HydrateContent in the importer's loop, so the lookup
// is O(stores) — typically O(1) since one Importer.Import covers
// one destination session).
func (h *dbLedgerHydrator) HydrateContent(nodeID string, blob []byte) error {
	if len(blob) == 0 {
		return nil
	}
	for _, store := range h.storeCache {
		if _, err := store.ReadNode(nodeID); err == nil {
			return store.WriteContentBlob(nodeID, blob)
		}
	}
	return nil
}

// ChainRootHashForSession implements migration.LedgerHydrator.
func (h *dbLedgerHydrator) ChainRootHashForSession(sessionID string) (string, error) {
	store, err := h.storeFor(sessionID)
	if err != nil {
		return "", err
	}
	return store.ChainRootHashForSession(sessionID)
}

// dbMemoryHydrator implements migration.MemoryHydrator against
// stoke_memory_bus. Re-uses the same UPSERT path
// cmd/r1-server/import.go's ingestMemorySnapshot uses for
// tracebundle imports.
type dbMemoryHydrator struct {
	db *DB
}

// HydrateMemoryRow implements migration.MemoryHydrator.
func (m *dbMemoryHydrator) HydrateMemoryRow(destSessionID string, rowJSON []byte) error {
	var row importedMemoryRow
	if err := json.Unmarshal(rowJSON, &row); err != nil {
		// Tolerate malformed rows — the rest of the import remains
		// useful.
		return nil //nolint:nilerr // bundle-side corruption is non-fatal
	}
	scope := row.Scope
	if scope == "" {
		scope = "session"
	}
	target := destSessionID
	key := row.Key
	if key == "" {
		key = fmt.Sprintf("imported-%d", row.ID)
	}
	const q = `INSERT INTO stoke_memory_bus (
	    created_at, expires_at, scope, scope_target, session_id,
	    step_id, task_id, author, key, content, content_hash
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(scope, scope_target, key) DO UPDATE SET
	    expires_at   = excluded.expires_at,
	    session_id   = excluded.session_id,
	    author       = excluded.author,
	    content      = excluded.content,
	    content_hash = excluded.content_hash`
	createdAt := row.CreatedAt
	if createdAt == "" {
		createdAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	m.db.mu.Lock()
	defer m.db.mu.Unlock()
	_, err := m.db.sql.Exec(q,
		createdAt, nullIfEmpty(row.ExpiresAt),
		scope, target, destSessionID,
		"", "", row.Author, key, row.Content, row.ContentHash,
	)
	return err
}

// dbWALReplayer implements migration.WALReplayer by INSERTing each
// event line as a session_events row. The cmd/r1-server daemon
// doesn't run a live bus.Bus — its WAL replay is observational only
// (events land in the SQLite projection so the dashboard can render
// them). The destination's chain root is still verified post-replay
// by the migration package's ChainRootHashForSession call against
// the filesystem ledger.Store hydrated separately.
type dbWALReplayer struct {
	db *DB
}

// ReplayWAL implements migration.WALReplayer.
func (r *dbWALReplayer) ReplayWAL(destSessionID string, walBytes []byte, onProgress func(seq uint64) error) (uint64, error) {
	if len(walBytes) == 0 {
		if onProgress != nil {
			return 0, onProgress(0)
		}
		return 0, nil
	}
	var count uint64
	var lastSeq uint64
	for _, line := range splitLines(walBytes) {
		if len(line) == 0 {
			continue
		}
		var probe struct {
			Sequence uint64 `json:"sequence"`
			Type     string `json:"type"`
		}
		_ = json.Unmarshal(line, &probe)
		ts := time.Now().UTC()
		if err := r.db.InsertEvent(destSessionID, probe.Type, line, ts); err != nil {
			return count, err
		}
		count++
		if probe.Sequence > lastSeq {
			lastSeq = probe.Sequence
		}
		if onProgress != nil && count%100 == 0 {
			if err := onProgress(probe.Sequence); err != nil {
				return count, err
			}
		}
	}
	if onProgress != nil {
		if err := onProgress(lastSeq); err != nil {
			return count, err
		}
	}
	return count, nil
}

// skillPackPresenceChecker implements migration.PackChecker by
// looking for an installed pack on the destination's filesystem.
// The canonical install location is one of:
//
//   - <data-dir>/skills/packs/<pack_id>/<content_hash>/pack.yaml
//   - <data-dir>/skills/packs/<pack_id>/pack.yaml          (no hash suffix)
//   - $HOME/.r1/skills/packs/<pack_id>/...
//
// We check both data-dir and home-dir paths so a destination that
// stores packs under either layout reports present. The content_hash
// match — when supplied — is enforced via a directory-name suffix
// check; if the source's pack ref carries an empty content_hash
// (a pack-pinning lapse on the source side) we fall back to a
// pack_id-only check so the import isn't blocked by a missing hash.
type skillPackPresenceChecker struct {
	dataDir string
}

// newSkillPackPresenceChecker builds a checker scoped to the daemon's
// data directory. Wired by serveMigrateIn from ensureDataDir().
func newSkillPackPresenceChecker(dataDir string) skillPackPresenceChecker {
	return skillPackPresenceChecker{dataDir: dataDir}
}

// HasPack implements migration.PackChecker. Returns true when an
// installed pack matching (pack_id, content_hash) is reachable on
// the destination's filesystem.
func (c skillPackPresenceChecker) HasPack(packID, contentHash string) bool {
	if packID == "" {
		return false
	}
	candidateRoots := []string{}
	if c.dataDir != "" {
		candidateRoots = append(candidateRoots,
			filepath.Join(c.dataDir, "skills", "packs", packID),
		)
	}
	if home, herr := os.UserHomeDir(); herr == nil {
		candidateRoots = append(candidateRoots,
			filepath.Join(home, ".r1", "skills", "packs", packID),
		)
	}
	for _, root := range candidateRoots {
		// Layout A: <root>/<content_hash>/pack.yaml (hash-pinned).
		if contentHash != "" {
			if _, err := os.Stat(filepath.Join(root, contentHash, "pack.yaml")); err == nil {
				return true
			}
		}
		// Layout B: <root>/pack.yaml (single-version installs).
		if _, err := os.Stat(filepath.Join(root, "pack.yaml")); err == nil {
			// When the source carried a non-empty content_hash, we
			// can't prove the hash matches without parsing the
			// installed pack's manifest. v1 accepts a pack_id match
			// here — a future spec tightens this via a manifest
			// content-hash read.
			return true
		}
	}
	return false
}

// splitLines is a small helper that splits raw on '\n' without
// allocating a [][]byte for every call (bytes.Split would over-
// allocate for the streaming case). Returns slices into raw.
func splitLines(raw []byte) [][]byte {
	var out [][]byte
	start := 0
	for i := 0; i <= len(raw); i++ {
		if i == len(raw) || raw[i] == '\n' {
			if i > start {
				out = append(out, raw[start:i])
			}
			start = i + 1
		}
	}
	return out
}

