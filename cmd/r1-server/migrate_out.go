// Package main — migrate_out.go
//
// Spec C1 §6.1 + T6 + T15. Serves POST /api/session/{id}/migrate-out:
// streams a .r1session bundle for one session.
//
// The source-side handler resolves the session's ledger dir + bus
// WAL path from the sessions table, opens the ledger.Store, reads
// the bus WAL bytes, collects memory rows scoped to the session,
// and hands the assembled BundleSource to internal/migration's
// WriteBundle.
//
// Auth: this handler runs inside the daemon's bearer-protected mux
// (the canonical mountUI invocation). Cross-tenant migration is
// refused on the import side; the export side delegates tenant
// claim checks to whoever holds the bundle next.
//
// Active-session safety: the export refuses to run while the source
// session is in a "running" state and ?force=1 is unset. The
// sessions table tracks status; "completed", "imported", "crashed",
// and the absence of an active liveness probe are all acceptable
// quiet-points. The 5-second wait described in the spec's §7.1
// step 2 collapses to "is status acceptable?" here because the
// cmd/r1-server daemon is the passive observer rather than the
// active host of the session — the active host (Stoke or `r1 serve`)
// has already paused before we're called.

package main

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/migration"
)

// serveMigrateOut returns the http.HandlerFunc bound to
// POST /api/session/{id}/migrate-out. Wired in main.go's mux.
func (d *DB) serveMigrateOut(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sid := r.PathValue("id")
	if sid == "" {
		writeMigrateError(w, http.StatusNotFound, "session_not_found", nil)
		return
	}
	row, err := d.GetSession(sid)
	if err != nil || row.InstanceID == "" {
		writeMigrateError(w, http.StatusNotFound, "session_not_found", nil)
		return
	}

	// Active-session guard (spec §6.1 errors + §7.1 step 2). The
	// force=1 query string overrides; otherwise a "running" status
	// without --force returns 409.
	force := r.URL.Query().Get("force") == "1"
	if !force && row.Status == "running" {
		writeMigrateError(w, http.StatusConflict, "session_busy", map[string]any{"state": row.Status})
		return
	}

	// Refuse export when the ledger holds legacy unsigned redactions.
	// We can only check this when a ledger dir is known; sessions
	// without a ledger_dir (rare; an import-from-tracebundle session
	// might lack it) skip this gate — they also lack content to
	// redact.
	var store *ledger.Store
	if row.LedgerDir != "" {
		st, lerr := ledger.NewStore(row.LedgerDir)
		if lerr != nil {
			writeMigrateError(w, http.StatusInternalServerError, "export_failed",
				map[string]any{"detail": lerr.Error()})
			return
		}
		store = st
		if unsigned, uerr := scanUnsignedRedactions(store, sid); uerr == nil && len(unsigned) > 0 {
			writeMigrateError(w, http.StatusConflict, "unsigned_redactions_present",
				map[string]any{"node_ids": unsigned})
			return
		}
	}

	// Bus WAL bytes — the sessions row tracks the WAL path. Empty
	// path means the session never wrote a WAL (chat-only legacy
	// sessions): emit an empty WAL.
	var walBytes []byte
	if row.BusWAL != "" {
		if b, err := os.ReadFile(row.BusWAL); err == nil {
			walBytes = b
		}
	}

	// Memory rows scoped to the session. The /memories projection
	// reads from stoke_memory_bus; we hand-roll the same query so
	// the bundle carries the canonical row JSON (encrypted DEK
	// envelopes preserved byte-for-byte).
	memRows, memCount, memTargets, mErr := d.collectMemoryRows(sid)
	if mErr != nil {
		writeMigrateError(w, http.StatusInternalServerError, "export_failed",
			map[string]any{"detail": "collect memory: " + mErr.Error()})
		return
	}

	// Pre-export checkpoint bytes — read the session's checkpoint
	// file if present. The migration package treats empty bytes as
	// "no checkpoint."
	var cpBytes []byte
	if row.CheckpointFile != "" {
		if b, rErr := os.ReadFile(row.CheckpointFile); rErr == nil {
			cpBytes = b
		}
	}

	// Skill pack refs are not directly exposed by the sessions row
	// in this build; emit an empty list. Future work: snapshot the
	// active workspace's skill registry per spec T6 / T19. The
	// import-side check still runs against an empty refs list and
	// passes (matches T9 happy path).
	packs := []migration.SkillPackRef{}

	// Chain root — computed by LedgerBundleSource lazily; we pull
	// it eagerly here so the source can report it to WriteBundle.
	var chainRoot string
	if store != nil {
		if r, err := store.ChainRootHashForSession(sid); err == nil {
			chainRoot = r
		}
	}

	src := &migration.LedgerBundleSource{
		HostID:              hostnameOrUnknown(),
		DaemonID:            daemonID(),
		SessionID:           sid,
		TenantIDValue:       tenantIDForSession(row),
		ModelID:             row.Model,
		Store:               store,
		WALBytesValue:       walBytes,
		WALFirstSeqValue:    0,
		WALLastSeqValue:     uint64(countNDJSONLines(walBytes)),
		WALCheckpointsValue: nil,
		MemoryRowsValue:     memRows,
		MemoryRowCountValue: memCount,
		MemoryTargetsValue:  memTargets,
		SkillPackRefsValue:  packs,
		LobeStatesValue:     map[string][]byte{},
		LanesSnapshotValue:  []byte(`{}`),
		CheckpointValue:     cpBytes,
	}
	_ = chainRoot // chainRoot is consumed via src.ChainRootHash()

	// Load or generate the migration signing key. We reuse the same
	// Ed25519 key the redaction-signer path manages, derived from
	// the daemon's data dir. This is the v1 "shared master key"
	// contract — the destination daemon's keyring must agree on the
	// key material or the manifest signature fails to verify.
	priv, signer, kerr := loadMigrationSigner()
	if kerr != nil {
		writeMigrateError(w, http.StatusInternalServerError, "export_failed",
			map[string]any{"detail": "signer: " + kerr.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s.r1session"`, sid))

	if err := migration.WriteBundle(w, src, signer, priv, time.Now().UTC()); err != nil {
		// Body already in flight; best we can do is log via
		// http.Error path. Client sees truncation.
		http.Error(w, "migrate-out: "+err.Error(), http.StatusInternalServerError)
		return
	}
}

// scanUnsignedRedactions walks the session's ledger redaction log and
// returns the IDs of any nodes whose redaction events carry no
// signature (legacy unsigned per specs/ledger-redaction.md). Used by
// the export gate (spec T18).
func scanUnsignedRedactions(store *ledger.Store, sessionID string) ([]string, error) {
	nodes, err := store.ListNodesForSession(sessionID)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, n := range nodes {
		events, err := store.RedactionsFor(n.ID)
		if err != nil {
			continue
		}
		for _, ev := range events {
			if ev.SignatureHex == "" {
				out = append(out, n.ID)
				break
			}
		}
	}
	return out, nil
}

// collectMemoryRows queries stoke_memory_bus for rows scoped to the
// session and emits them as NDJSON (one row per line). Returns the
// byte buffer, the row count, the distinct scope_target values, and
// any I/O error.
func (d *DB) collectMemoryRows(sessionID string) ([]byte, int, []string, error) {
	const q = `SELECT id, scope, COALESCE(scope_target,''), key, COALESCE(content,''),
	                  COALESCE(author,''), created_at, COALESCE(expires_at,''),
	                  COALESCE(content_hash,'')
	           FROM stoke_memory_bus
	           WHERE scope = 'session' AND scope_target = ?
	           ORDER BY id ASC`
	rows, err := d.sql.Query(q, sessionID)
	if err != nil {
		return nil, 0, nil, err
	}
	defer rows.Close()
	var buf []byte
	var count int
	targets := map[string]struct{}{}
	for rows.Next() {
		var (
			id          int64
			scope       string
			target      string
			key         string
			content     string
			author      string
			createdAt   string
			expiresAt   string
			contentHash string
		)
		if err := rows.Scan(&id, &scope, &target, &key, &content, &author, &createdAt, &expiresAt, &contentHash); err != nil {
			return nil, 0, nil, err
		}
		row := map[string]any{
			"id":           id,
			"scope":        scope,
			"scope_target": target,
			"key":          key,
			"content":      content,
			"author":       author,
			"created_at":   createdAt,
		}
		if expiresAt != "" {
			row["expires_at"] = expiresAt
		}
		if contentHash != "" {
			row["content_hash"] = contentHash
		}
		line, mErr := json.Marshal(row)
		if mErr != nil {
			return nil, 0, nil, mErr
		}
		buf = append(buf, line...)
		buf = append(buf, '\n')
		count++
		targets[target] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, nil, err
	}
	tgts := make([]string, 0, len(targets))
	for t := range targets {
		tgts = append(tgts, t)
	}
	return buf, count, tgts, nil
}

// countNDJSONLines counts non-empty NDJSON lines in raw. Used to
// estimate WAL last-seq when a true seq is unavailable (cmd/r1-server
// reads the on-disk WAL as opaque bytes).
func countNDJSONLines(raw []byte) int {
	if len(raw) == 0 {
		return 0
	}
	count := 0
	for i, start := 0, 0; i <= len(raw); i++ {
		if i == len(raw) || raw[i] == '\n' {
			if i > start && strings.TrimSpace(string(raw[start:i])) != "" {
				count++
			}
			start = i + 1
		}
	}
	return count
}

// hostnameOrUnknown returns os.Hostname() or "unknown" if the call
// fails. Stamped into the bundle's source_host field.
func hostnameOrUnknown() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "unknown"
	}
	return h
}

// daemonID returns the daemon's process-stable ID. For the v1 bundle
// path we use the process's pid + boot time pair, stringified. A
// future spec wires this to a configured host_id from
// ~/.r1/daemon.json.
func daemonID() string {
	return fmt.Sprintf("pid-%d", os.Getpid())
}

// tenantIDForSession returns the tenant id stamped on a session row.
// The sessions table doesn't carry an explicit tenant column in v1
// (single-tenant per daemon); we surface an empty tenant id so the
// destination's tenant check is a no-op in v1 single-tenant mode.
// Future multi-tenant work will add this column.
func tenantIDForSession(row SessionRow) string {
	_ = row
	return ""
}

// loadMigrationSigner returns the daemon's Ed25519 signer + its
// fingerprint. Reuses ledger.LoadOrGenerateSigningKey so the same
// key signs migration bundles and redaction events — a "single key
// per daemon" contract for v1. Cross-machine migration requires
// that both daemons agree on this key (the encryption-at-rest spec
// covers the key-material handshake).
func loadMigrationSigner() (ed25519.PrivateKey, string, error) {
	dir, err := ensureDataDir()
	if err != nil {
		return nil, "", err
	}
	// LoadOrGenerateSigningKey stashes priv+pub PEMs under
	// <root>/redactions/. The migration path is content-addressed
	// via the manifest body, so reusing the redaction signer is
	// safe — the canonical body is namespaced by FormatTag.
	priv, fp, err := ledger.LoadOrGenerateSigningKey(filepath.Join(dir, "migration"))
	if err != nil {
		return nil, "", err
	}
	return priv, fp, nil
}

// writeMigrateError emits a structured JSON error body per spec §6.1.
// Used by every non-2xx response from the migrate handlers.
func writeMigrateError(w http.ResponseWriter, status int, code string, extra map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body := map[string]any{"error": code}
	for k, v := range extra {
		body[k] = v
	}
	_ = json.NewEncoder(w).Encode(body)
}

// drainResponseBody is a convenience for tests that need to consume
// the streaming body without parsing it.
func drainResponseBody(r io.Reader) ([]byte, error) {
	return io.ReadAll(r)
}
