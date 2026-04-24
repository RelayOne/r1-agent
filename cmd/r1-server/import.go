// Package main — import.go
//
// work-stoke TASK 16 (consumer side): ingest `.tracebundle` archives
// produced by `stoke export` into the r1-server SQLite database.
//
//	r1-server import <bundle.tracebundle> [--data-dir <dir>]
//
// The import is idempotent: re-importing the same bundle is a no-op
// at the row level (session upsert + INSERT OR IGNORE on events + ON
// CONFLICT DO UPDATE on ledger nodes/edges). Before any row is
// touched we recompute the Merkle root over the archive's files and
// fail if it disagrees with the manifest's recorded root — a defence
// against corruption, tampering, or format drift between versions.
package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/session"
)

// importedManifest mirrors cmd/stoke.TraceBundleManifest — we avoid
// importing main from main to sidestep the go build graph and because
// the consumer should tolerate future additive fields without pulling
// the producer's header file in.
type importedManifest struct {
	Format     string `json:"format"`
	Version    string `json:"version"`
	SessionID  string `json:"session_id"`
	RepoRoot   string `json:"repo_root,omitempty"`
	Mode       string `json:"mode,omitempty"`
	Model      string `json:"model,omitempty"`
	SowName    string `json:"sow_name,omitempty"`
	Status     string `json:"status,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	MerkleRoot string `json:"merkle_root"`
	Files      []struct {
		Path   string `json:"path"`
		Size   int64  `json:"size"`
		Digest string `json:"digest"`
	} `json:"files"`
}

// importedMemoryRow is the portable membus row shape the exporter
// writes into memory.json.
type importedMemoryRow struct {
	ID          int64  `json:"id"`
	Scope       string `json:"scope"`
	ScopeTarget string `json:"scope_target,omitempty"`
	Key         string `json:"key"`
	Content     string `json:"content"`
	Author      string `json:"author,omitempty"`
	CreatedAt   string `json:"created_at"`
	ExpiresAt   string `json:"expires_at,omitempty"`
	ContentHash string `json:"content_hash,omitempty"`
}

// importedMemorySnapshot matches the exporter's sessionMemorySnapshot.
type importedMemorySnapshot struct {
	SessionID string              `json:"session_id"`
	Rows      []importedMemoryRow `json:"rows"`
	Count     int                 `json:"count"`
}

// ImportSummary is returned to the caller (and printed by runImportCmd)
// so operators can see exactly what landed in the DB.
type ImportSummary struct {
	BundlePath string `json:"bundle_path"`
	SessionID  string `json:"session_id"`
	MerkleRoot string `json:"merkle_root"`
	Events     int    `json:"events"`
	Nodes      int    `json:"nodes"`
	Edges      int    `json:"edges"`
	Memories   int    `json:"memories"`
}

// runImportCmd is the entry point for `r1-server import`. Returns a
// UNIX-style exit code (0 ok / 1 runtime / 2 usage).
func runImportCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", "", "override data directory (default: ensureDataDir)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(stderr, "usage: r1-server import <bundle.tracebundle> [--data-dir DIR]")
		return 2
	}
	bundlePath := rest[0]

	// Resolve data dir — flag wins, else the usual ensureDataDir path
	// so import shares the serve-mode DB without any extra plumbing.
	dir := *dataDir
	if dir == "" {
		d, err := ensureDataDir()
		if err != nil {
			fmt.Fprintf(stderr, "import: data dir: %v\n", err)
			return 1
		}
		dir = d
	} else if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(stderr, "import: mkdir data-dir: %v\n", err)
		return 1
	}

	db, err := OpenDB(dir)
	if err != nil {
		fmt.Fprintf(stderr, "import: open db: %v\n", err)
		return 1
	}
	defer db.Close()

	summary, err := ImportTraceBundle(db, bundlePath)
	if err != nil {
		fmt.Fprintf(stderr, "import: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "imported %s\n  session_id=%s merkle_root=%s\n  events=%d nodes=%d edges=%d memories=%d\n",
		summary.BundlePath, summary.SessionID, summary.MerkleRoot,
		summary.Events, summary.Nodes, summary.Edges, summary.Memories)
	return 0
}

// ImportTraceBundle unzips bundlePath, validates its Merkle root
// against the manifest, and writes every artefact into db. Safe to
// call repeatedly — schema-level upserts make re-runs a no-op.
func ImportTraceBundle(db *DB, bundlePath string) (ImportSummary, error) {
	summary := ImportSummary{BundlePath: bundlePath}

	zr, err := zip.OpenReader(bundlePath)
	if err != nil {
		return summary, fmt.Errorf("open bundle %s: %w", bundlePath, err)
	}
	defer zr.Close()

	// Slurp every archived file into memory keyed by path. Bundles
	// are small (a long-running session ships kilobytes, not MB) so
	// this trades memory for one-pass digest + ingest.
	payload := make(map[string][]byte)
	digests := make(map[string]string)
	for _, f := range zr.File {
		var rc io.ReadCloser
		rc, err = f.Open()
		if err != nil {
			return summary, fmt.Errorf("open %s in bundle: %w", f.Name, err)
		}
		var buf []byte
		buf, err = io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return summary, fmt.Errorf("read %s in bundle: %w", f.Name, err)
		}
		payload[f.Name] = buf
		sum := sha256.Sum256(buf)
		digests[f.Name] = hex.EncodeToString(sum[:])
	}

	manifestBytes, ok := payload["manifest.json"]
	if !ok {
		return summary, fmt.Errorf("bundle missing manifest.json")
	}
	var manifest importedManifest
	if err = json.Unmarshal(manifestBytes, &manifest); err != nil {
		return summary, fmt.Errorf("parse manifest.json: %w", err)
	}
	if manifest.Format != "tracebundle" {
		return summary, fmt.Errorf("unsupported manifest format %q", manifest.Format)
	}
	if manifest.SessionID == "" {
		return summary, fmt.Errorf("manifest missing session_id")
	}

	// Merkle verification: recompute the root from the manifest's
	// declared files + their on-disk digests, and compare against
	// manifest.MerkleRoot. Any drift here means the zip has been
	// modified since export — hard fail rather than silently ingest
	// something that does not match its advertised identity.
	if err = verifyMerkleRoot(manifest, digests); err != nil {
		return summary, err
	}
	summary.SessionID = manifest.SessionID
	summary.MerkleRoot = manifest.MerkleRoot

	// Upsert the session row first so every downstream FK (events in
	// particular) has a valid target. ended_at is inferred from the
	// manifest's snapshotted status.
	sig := session.SignatureFile{
		Version:    "tracebundle-import",
		InstanceID: manifest.SessionID,
		RepoRoot:   manifest.RepoRoot,
		Mode:       manifest.Mode,
		Model:      manifest.Model,
		SowName:    manifest.SowName,
		Status:     statusForImport(manifest.Status),
		StartedAt:  manifest.CreatedAt,
		UpdatedAt:  manifest.CreatedAt,
	}
	if err = db.UpsertSession(sig); err != nil {
		return summary, fmt.Errorf("upsert session: %w", err)
	}

	// --- stream.jsonl → session_events ---
	if stream, ok := payload["stream.jsonl"]; ok {
		var n int
		n, err = ingestStreamJSONL(db, manifest.SessionID, stream)
		if err != nil {
			return summary, fmt.Errorf("ingest stream: %w", err)
		}
		summary.Events = n
	}

	// --- ledger/nodes/*.json + ledger/edges/*.json → ledger_* tables ---
	var nodes, edges int
	nodes, edges, err = ingestLedgerPayload(db, manifest.SessionID, payload)
	if err != nil {
		return summary, err
	}
	summary.Nodes = nodes
	summary.Edges = edges

	// --- memory.json → stoke_memory_bus rows. The r1-server schema
	// already exposes this table via ensureMemoryBusSchema (called
	// from OpenDB) so the /memories handler can query imported rows
	// just like live ones. UPSERT on (scope, scope_target, key) is
	// the spec's uniqueness contract — a re-import of the same
	// bundle overwrites in place rather than duplicating rows.
	if mem, ok := payload["memory.json"]; ok {
		n, err := ingestMemorySnapshot(db, manifest.SessionID, mem)
		if err != nil {
			return summary, fmt.Errorf("ingest memory: %w", err)
		}
		summary.Memories = n
	}

	return summary, nil
}

// ingestMemorySnapshot parses memory.json and writes each row into
// stoke_memory_bus. Malformed snapshot → zero rows + nil error (the
// session + events + ledger are still valuable even if memory.json
// is corrupt). Per-row UPSERT keeps re-imports idempotent.
func ingestMemorySnapshot(db *DB, sessionID string, raw []byte) (int, error) {
	var snap importedMemorySnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return 0, nil //nolint:nilerr // tolerate bundle-side corruption
	}
	if len(snap.Rows) == 0 {
		return 0, nil
	}
	db.mu.Lock()
	defer db.mu.Unlock()

	// Schema-matching insert. content_encrypted / tags / metadata /
	// read_count are set to their DDL defaults because the bundle
	// format does not carry them (privacy: encrypted bytes never
	// leave the source host). scope_target falls back to sessionID
	// when the bundle omitted it so the row is still query-able.
	const q = `
INSERT INTO stoke_memory_bus (
    created_at, expires_at, scope, scope_target, session_id,
    step_id, task_id, author, key, content, content_hash
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(scope, scope_target, key) DO UPDATE SET
    expires_at   = excluded.expires_at,
    session_id   = excluded.session_id,
    author       = excluded.author,
    content      = excluded.content,
    content_hash = excluded.content_hash`

	tx, err := db.sql.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	stmt, err := tx.Prepare(q)
	if err != nil {
		_ = tx.Rollback()
		return 0, fmt.Errorf("prepare membus insert: %w", err)
	}
	defer stmt.Close()

	createdFallback := time.Now().UTC().Format(time.RFC3339Nano)
	written := 0
	for _, r := range snap.Rows {
		scope := r.Scope
		if scope == "" {
			scope = "session"
		}
		target := r.ScopeTarget
		if target == "" {
			target = sessionID
		}
		key := r.Key
		if key == "" {
			// Enforce the NOT NULL key constraint even when the
			// bundle has a blank: synthesise from row id so the
			// insert still lands rather than aborting mid-batch.
			key = fmt.Sprintf("imported-%d", r.ID)
		}
		createdAt := r.CreatedAt
		if createdAt == "" {
			createdAt = createdFallback
		}
		if _, err := stmt.Exec(
			createdAt, nullIfEmpty(r.ExpiresAt),
			scope, target, sessionID,
			"", "", // step_id / task_id — not carried in the bundle
			r.Author, key, r.Content, r.ContentHash,
		); err != nil {
			_ = tx.Rollback()
			return written, fmt.Errorf("insert membus row: %w", err)
		}
		written++
	}
	if err := tx.Commit(); err != nil {
		return written, fmt.Errorf("commit membus tx: %w", err)
	}
	return written, nil
}

// nullIfEmpty returns a *string nil sentinel when s is "", used so
// optional TEXT columns (expires_at) land as SQL NULL rather than
// empty-string — matches the writer-side contract in memory-bus spec.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// verifyMerkleRoot rehashes the canonical "<path>\t<digest>\n" list
// from the manifest and compares against manifest.MerkleRoot. This
// mirrors the producer's computeMerkleRoot so both sides stay in
// lockstep — any divergence here is an ingestion-layer bug, not a
// data-layer one.
func verifyMerkleRoot(manifest importedManifest, digests map[string]string) error {
	// Cross-check each file listed in the manifest against the
	// digest we computed over the archived bytes. A missing file or
	// digest mismatch is fatal.
	type pair struct{ path, digest string }
	pairs := make([]pair, 0, len(manifest.Files))
	for _, f := range manifest.Files {
		got, ok := digests[f.Path]
		if !ok {
			return fmt.Errorf("manifest lists %q but bundle has no such file", f.Path)
		}
		if got != f.Digest {
			return fmt.Errorf("digest mismatch for %q: manifest=%s archive=%s",
				f.Path, f.Digest, got)
		}
		pairs = append(pairs, pair{path: f.Path, digest: f.Digest})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].path < pairs[j].path })

	h := sha256.New()
	for _, p := range pairs {
		fmt.Fprintf(h, "%s\t%s\n", p.path, p.digest)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, manifest.MerkleRoot) {
		return fmt.Errorf("merkle root mismatch: manifest=%s recomputed=%s",
			manifest.MerkleRoot, got)
	}
	return nil
}

// statusForImport normalises the session status field. A running
// bundle almost certainly means the source process died before
// writing a terminal status; we mark it "imported" so dashboards can
// tell at a glance that the row came from a tracebundle rather than
// a live sidecar.
func statusForImport(s string) string {
	if s == "" || s == "running" {
		return "imported"
	}
	return s
}

// ingestStreamJSONL writes each non-empty NDJSON line into
// session_events. The event "type" is extracted heuristically from
// the line's `type` field; lines lacking a type land with event_type=""
// so they're still queryable by instance.
func ingestStreamJSONL(db *DB, sessionID string, raw []byte) (int, error) {
	var count int
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		etype, ts := extractEventType(line)
		if err := db.InsertEvent(sessionID, etype, line, ts); err != nil {
			return count, fmt.Errorf("insert event: %w", err)
		}
		count++
	}
	return count, nil
}

// extractEventType peeks at the NDJSON line to pull its `type` and
// `ts` fields. Malformed JSON is tolerated — the full line is still
// stored, just with blank type and now() as the timestamp.
func extractEventType(line []byte) (string, time.Time) {
	var probe struct {
		Type string `json:"type"`
		TS   string `json:"ts"`
	}
	if err := json.Unmarshal(line, &probe); err != nil {
		return "", time.Now().UTC()
	}
	ts, err := time.Parse(time.RFC3339Nano, probe.TS)
	if err != nil {
		ts = time.Now().UTC()
	}
	return probe.Type, ts
}

// ingestLedgerPayload walks every "ledger/…" entry in the payload map
// and upserts nodes into ledger_nodes or edges into ledger_edges
// based on their relative path under ledger/. Unknown subdirs are
// ignored — we'd rather import a shallow subset than fail on a new
// ledger layout we don't yet know about.
func ingestLedgerPayload(db *DB, sessionID string, payload map[string][]byte) (int, int, error) {
	var (
		nodes, edges int
	)
	for name, data := range payload {
		if !strings.HasPrefix(name, "ledger/") {
			continue
		}
		rel := strings.TrimPrefix(name, "ledger/")
		switch {
		case strings.HasPrefix(rel, "nodes/"):
			if err := ingestLedgerNode(db, sessionID, name, data); err != nil {
				return nodes, edges, err
			}
			nodes++
		case strings.HasPrefix(rel, "edges/"):
			if err := ingestLedgerEdge(db, sessionID, name, data); err != nil {
				return nodes, edges, err
			}
			edges++
		default:
			// Non-node/edge files (chain.txt, index.json, etc.) live
			// in the bundle's ledger/ tree but do not belong in the
			// r1-server graph projection. The zip still carries them
			// verbatim; archival consumers can recover the full
			// filesystem layout from the zip, while the DB only
			// projects nodes + edges.
		}
	}
	return nodes, edges, nil
}

// ingestLedgerNode parses one ledger/nodes/*.json payload and upserts
// it into ledger_nodes. Missing / schema-drifted fields default to
// empty strings — a node with no mission_id still belongs in the DB.
func ingestLedgerNode(db *DB, sessionID, path string, data []byte) error {
	var probe struct {
		ID         string `json:"id"`
		NodeID     string `json:"node_id"`
		Type       string `json:"type"`
		NodeType   string `json:"node_type"`
		MissionID  string `json:"mission_id"`
		CreatedAt  string `json:"created_at"`
		CreatedBy  string `json:"created_by"`
		ParentHash string `json:"parent_hash"`
	}
	_ = json.Unmarshal(data, &probe) // best-effort

	id := firstNonEmpty(probe.ID, probe.NodeID, pathStem(path))
	nodeType := firstNonEmpty(probe.Type, probe.NodeType)
	return db.UpsertLedgerNode(
		sessionID, id, nodeType, probe.MissionID,
		probe.CreatedAt, probe.CreatedBy, probe.ParentHash, data,
	)
}

// ingestLedgerEdge is the edge counterpart to ingestLedgerNode.
func ingestLedgerEdge(db *DB, sessionID, path string, data []byte) error {
	var probe struct {
		ID       string `json:"id"`
		EdgeID   string `json:"edge_id"`
		From     string `json:"from"`
		FromNode string `json:"from_node"`
		To       string `json:"to"`
		ToNode   string `json:"to_node"`
		Type     string `json:"type"`
		EdgeType string `json:"edge_type"`
	}
	_ = json.Unmarshal(data, &probe)

	id := firstNonEmpty(probe.ID, probe.EdgeID, pathStem(path))
	from := firstNonEmpty(probe.From, probe.FromNode)
	to := firstNonEmpty(probe.To, probe.ToNode)
	etype := firstNonEmpty(probe.Type, probe.EdgeType)
	return db.UpsertLedgerEdge(sessionID, id, from, to, etype, data)
}

// firstNonEmpty is defined in trace.go and shared across the r1-server
// package.

// pathStem strips the final extension and directory prefix so
// "ledger/nodes/abc123.json" becomes "abc123" — used as a fallback
// node/edge id when the on-disk JSON has no explicit id field.
func pathStem(p string) string {
	// Drop everything before the last "/".
	if idx := strings.LastIndex(p, "/"); idx >= 0 {
		p = p[idx+1:]
	}
	if idx := strings.LastIndex(p, "."); idx > 0 {
		p = p[:idx]
	}
	return p
}
