// Package main — db.go
//
// SQLite persistence for r1-server. Schema per spec RS-2:
//
//	sessions        — one row per discovered Stoke instance
//	session_events  — append-only event stream per session
//	ledger_nodes    — copy of <ledger_dir>/nodes/*.json for fast query
//	ledger_edges    — copy of <ledger_dir>/edges/*.json for fast query
//
// WAL mode is on. All writes go through the same *DB handle
// serialized by its internal mutex for deterministic ordering.
// Reads are via sql.DB's pooled connections.
package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/RelayOne/r1/internal/encryption"
	"github.com/RelayOne/r1/internal/session"
)

// dsnEncryptionEnv is the feature flag gating at-rest encryption for
// the r1-server tracking DB. Setting it to "1" before OpenDB loads the
// master key via encryption.LoadOrGenerateMaster and opens SQLite
// through the sqlite3mc ChaCha20 DSN produced by
// encryption.BuildEncryptedDSN. Any other value leaves the plaintext
// path intact. The env is read ONCE per open; mutating it afterwards
// has no effect on an already-open handle.
const dsnEncryptionEnv = "STOKE_DB_ENCRYPTION"

// buildServerDSN returns the DSN to pass to sql.Open for r1-server's
// server.db. When STOKE_DB_ENCRYPTION=1 it derives the 32-byte master
// key from the default keyring and asks encryption.BuildEncryptedDSN
// for a sqlite3mc-flavoured URI that carries the ChaCha20 parameters.
// Otherwise it hands back the historic plaintext URI (journal=WAL,
// busy_timeout=5000). Errors are fail-closed: a missing keyring or
// failed DSN build surfaces here rather than silently downgrading to
// plaintext, so an operator who opts in can never end up with an
// un-encrypted DB by accident.
func buildServerDSN(path string) (string, error) {
	if os.Getenv(dsnEncryptionEnv) != "1" {
		return fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000", path), nil
	}
	key, err := encryption.LoadOrGenerateMaster()
	if err != nil {
		return "", fmt.Errorf("r1-server: load master key: %w", err)
	}
	dsn, err := encryption.BuildEncryptedDSN(path, key)
	if err != nil {
		return "", fmt.Errorf("r1-server: build encrypted DSN: %w", err)
	}
	return dsn, nil
}

const schemaDDL = `
CREATE TABLE IF NOT EXISTS sessions (
    instance_id       TEXT PRIMARY KEY,
    pid               INTEGER,
    repo_root         TEXT,
    mode              TEXT,
    sow_name          TEXT,
    model             TEXT,
    status            TEXT,
    stream_file       TEXT,
    ledger_dir        TEXT,
    checkpoint_file   TEXT,
    bus_wal           TEXT,
    started_at        TEXT,
    updated_at        TEXT,
    ended_at          TEXT
);

CREATE TABLE IF NOT EXISTS session_events (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    instance_id  TEXT NOT NULL REFERENCES sessions(instance_id),
    event_type   TEXT,
    data         TEXT,
    timestamp    TEXT
);

CREATE INDEX IF NOT EXISTS idx_events_instance ON session_events(instance_id);
CREATE INDEX IF NOT EXISTS idx_events_type     ON session_events(event_type);

CREATE TABLE IF NOT EXISTS ledger_nodes (
    instance_id  TEXT NOT NULL,
    node_id      TEXT NOT NULL,
    node_type    TEXT,
    mission_id   TEXT,
    created_at   TEXT,
    created_by   TEXT,
    parent_hash  TEXT,
    raw          TEXT,
    PRIMARY KEY (instance_id, node_id)
);

CREATE INDEX IF NOT EXISTS idx_ledger_nodes_instance ON ledger_nodes(instance_id);
CREATE INDEX IF NOT EXISTS idx_ledger_nodes_type     ON ledger_nodes(node_type);

CREATE TABLE IF NOT EXISTS ledger_edges (
    instance_id  TEXT NOT NULL,
    edge_id      TEXT NOT NULL,
    from_node    TEXT,
    to_node      TEXT,
    edge_type    TEXT,
    raw          TEXT,
    PRIMARY KEY (instance_id, edge_id)
);

CREATE INDEX IF NOT EXISTS idx_ledger_edges_instance ON ledger_edges(instance_id);
`

// DB wraps a *sql.DB with the application's write mutex so concurrent
// scanner + HTTP writers can't deadlock on the single-writer SQLite
// semantics.
type DB struct {
	sql *sql.DB
	mu  sync.Mutex
}

// OpenDB opens the server.db file under dataDir and applies schema.
// When STOKE_DB_ENCRYPTION=1 the underlying file is opened through the
// sqlite3mc ChaCha20 cipher, keyed by the master key in the OS keyring
// (see encryption.LoadOrGenerateMaster + BuildEncryptedDSN). Otherwise
// the historic plaintext DSN is used. See buildServerDSN for the gate.
func OpenDB(dataDir string) (*DB, error) {
	path := filepath.Join(dataDir, "server.db")
	dsn, err := buildServerDSN(path)
	if err != nil {
		return nil, fmt.Errorf("build DSN for %s: %w", path, err)
	}
	conn, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	conn.SetMaxOpenConns(1) // SQLite: serialize writes via single conn
	if _, err := conn.Exec(schemaDDL); err != nil {
		conn.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	// Spec 27 §6: apply the memory-bus read projection DDL so the
	// /memories handler can query stoke_memory_bus even when no
	// Stoke writer has run yet. Idempotent (IF NOT EXISTS).
	if err := ensureMemoryBusSchema(conn); err != nil {
		conn.Close()
		return nil, err
	}
	return &DB{sql: conn}, nil
}

// Close closes the underlying DB handle.
func (d *DB) Close() error {
	if d == nil || d.sql == nil {
		return nil
	}
	return d.sql.Close()
}

// UpsertSession inserts or replaces a session row from a discovered
// signature file. Missing fields are preserved if the row already
// exists and the incoming signature has them empty.
func (d *DB) UpsertSession(sig session.SignatureFile) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	const q = `
INSERT INTO sessions (
    instance_id, pid, repo_root, mode, sow_name, model, status,
    stream_file, ledger_dir, checkpoint_file, bus_wal,
    started_at, updated_at, ended_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(instance_id) DO UPDATE SET
    pid             = excluded.pid,
    repo_root       = excluded.repo_root,
    mode            = excluded.mode,
    sow_name        = COALESCE(NULLIF(excluded.sow_name, ''), sessions.sow_name),
    model           = COALESCE(NULLIF(excluded.model, ''), sessions.model),
    status          = excluded.status,
    stream_file     = COALESCE(NULLIF(excluded.stream_file, ''), sessions.stream_file),
    ledger_dir      = COALESCE(NULLIF(excluded.ledger_dir, ''), sessions.ledger_dir),
    checkpoint_file = COALESCE(NULLIF(excluded.checkpoint_file, ''), sessions.checkpoint_file),
    bus_wal         = COALESCE(NULLIF(excluded.bus_wal, ''), sessions.bus_wal),
    updated_at      = excluded.updated_at,
    ended_at        = CASE WHEN excluded.status IN ('completed','failed','crashed') AND sessions.ended_at IS NULL
                           THEN excluded.updated_at ELSE sessions.ended_at END
`
	endedAt := ""
	if sig.Status == "completed" || sig.Status == "failed" || sig.Status == "crashed" {
		endedAt = sig.UpdatedAt.Format(time.RFC3339Nano)
	}
	_, err := d.sql.Exec(q,
		sig.InstanceID,
		sig.PID,
		sig.RepoRoot,
		sig.Mode,
		sig.SowName,
		sig.Model,
		statusOrDefault(sig.Status),
		sig.StreamFile,
		sig.LedgerDir,
		sig.CheckpointFile,
		sig.BusWAL,
		sig.StartedAt.Format(time.RFC3339Nano),
		sig.UpdatedAt.Format(time.RFC3339Nano),
		endedAt,
	)
	if err != nil {
		return fmt.Errorf("upsert session: %w", err)
	}
	return nil
}

// MarkSessionCrashed flips a session's status to "crashed" — used
// when liveness probe says the PID is gone but the sidecar still
// says running.
func (d *DB) MarkSessionCrashed(instanceID string, at time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.sql.Exec(
		`UPDATE sessions SET status = 'crashed', updated_at = ?, ended_at = COALESCE(ended_at, ?) WHERE instance_id = ?`,
		at.Format(time.RFC3339Nano), at.Format(time.RFC3339Nano), instanceID,
	)
	return err
}

func statusOrDefault(s string) string {
	if s == "" {
		return "running"
	}
	return s
}

// SessionRow is the DB projection of a sessions row returned by list
// + detail endpoints.
type SessionRow struct {
	InstanceID     string `json:"instance_id"`
	PID            int    `json:"pid"`
	RepoRoot       string `json:"repo_root"`
	Mode           string `json:"mode"`
	SowName        string `json:"sow_name,omitempty"`
	Model          string `json:"model,omitempty"`
	Status         string `json:"status"`
	StreamFile     string `json:"stream_file,omitempty"`
	LedgerDir      string `json:"ledger_dir,omitempty"`
	CheckpointFile string `json:"checkpoint_file,omitempty"`
	BusWAL         string `json:"bus_wal,omitempty"`
	StartedAt      string `json:"started_at"`
	UpdatedAt      string `json:"updated_at"`
	EndedAt        string `json:"ended_at,omitempty"`
}

// ListSessions returns all session rows, optionally filtered by
// status. Empty status string returns all. Ordered by started_at
// desc so the freshest session is first.
func (d *DB) ListSessions(status string) ([]SessionRow, error) {
	q := `SELECT instance_id, pid, repo_root, mode, COALESCE(sow_name,''),
	             COALESCE(model,''), status, COALESCE(stream_file,''),
	             COALESCE(ledger_dir,''), COALESCE(checkpoint_file,''),
	             COALESCE(bus_wal,''), started_at, updated_at, COALESCE(ended_at,'')
	      FROM sessions`
	args := []any{}
	if status != "" {
		q += ` WHERE status = ?`
		args = append(args, status)
	}
	q += ` ORDER BY started_at DESC`

	rows, err := d.sql.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionRow
	for rows.Next() {
		var r SessionRow
		if err := rows.Scan(
			&r.InstanceID, &r.PID, &r.RepoRoot, &r.Mode, &r.SowName,
			&r.Model, &r.Status, &r.StreamFile, &r.LedgerDir, &r.CheckpointFile,
			&r.BusWAL, &r.StartedAt, &r.UpdatedAt, &r.EndedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetSession returns the row for a specific instance.
func (d *DB) GetSession(instanceID string) (SessionRow, error) {
	rows, err := d.ListSessions("")
	if err != nil {
		return SessionRow{}, err
	}
	for _, r := range rows {
		if r.InstanceID == instanceID {
			return r, nil
		}
	}
	return SessionRow{}, sql.ErrNoRows
}

// InsertEvent appends one NDJSON line to session_events.
func (d *DB) InsertEvent(instanceID, eventType string, raw []byte, ts time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.sql.Exec(
		`INSERT INTO session_events(instance_id, event_type, data, timestamp) VALUES (?, ?, ?, ?)`,
		instanceID, eventType, string(raw), ts.Format(time.RFC3339Nano),
	)
	return err
}

// EventRow is the DB projection of a session_events row.
type EventRow struct {
	ID         int64           `json:"id"`
	InstanceID string          `json:"instance_id"`
	EventType  string          `json:"event_type"`
	Data       json.RawMessage `json:"data"`
	Timestamp  string          `json:"timestamp"`
}

// ListEvents returns events for one session, with cursor pagination.
// afterID: return events with id > afterID (0 means from start).
// limit: cap rows returned (<=0 means no cap — caller passes 1k default).
func (d *DB) ListEvents(instanceID string, afterID int64, limit int) ([]EventRow, error) {
	if limit <= 0 {
		limit = 1000
	}
	q := `SELECT id, instance_id, event_type, data, timestamp
	      FROM session_events
	      WHERE instance_id = ? AND id > ?
	      ORDER BY id ASC
	      LIMIT ?`
	rows, err := d.sql.Query(q, instanceID, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventRow
	for rows.Next() {
		var r EventRow
		var rawData string
		if err := rows.Scan(&r.ID, &r.InstanceID, &r.EventType, &rawData, &r.Timestamp); err != nil {
			return nil, err
		}
		r.Data = json.RawMessage(rawData)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListAllEvents returns events across every session past afterID
// (0 means from start). limit<=0 applies the same 1000-row default
// as ListEvents. Used by the cross-session SSE firehose at
// /api/events (work-stoke TASK 12) that the htmx index dashboard
// subscribes to.
func (d *DB) ListAllEvents(afterID int64, limit int) ([]EventRow, error) {
	if limit <= 0 {
		limit = 1000
	}
	q := `SELECT id, instance_id, event_type, data, timestamp
	      FROM session_events
	      WHERE id > ?
	      ORDER BY id ASC
	      LIMIT ?`
	rows, err := d.sql.Query(q, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventRow
	for rows.Next() {
		var r EventRow
		var rawData string
		if err := rows.Scan(&r.ID, &r.InstanceID, &r.EventType, &rawData, &r.Timestamp); err != nil {
			return nil, err
		}
		r.Data = json.RawMessage(rawData)
		out = append(out, r)
	}
	return out, rows.Err()
}

// MaxEventID returns the highest event id stored for this instance,
// or 0 if none. Used by SSE streams to establish a cursor.
func (d *DB) MaxEventID(instanceID string) (int64, error) {
	var id sql.NullInt64
	err := d.sql.QueryRow(
		`SELECT MAX(id) FROM session_events WHERE instance_id = ?`,
		instanceID,
	).Scan(&id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if !id.Valid {
		return 0, nil
	}
	return id.Int64, nil
}

// UpsertLedgerNode adds/replaces a ledger node row.
func (d *DB) UpsertLedgerNode(instanceID, nodeID, nodeType, missionID, createdAt, createdBy, parentHash string, raw []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.sql.Exec(
		`INSERT INTO ledger_nodes(instance_id, node_id, node_type, mission_id, created_at, created_by, parent_hash, raw)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(instance_id, node_id) DO UPDATE SET
		    node_type=excluded.node_type,
		    mission_id=excluded.mission_id,
		    created_at=excluded.created_at,
		    created_by=excluded.created_by,
		    parent_hash=excluded.parent_hash,
		    raw=excluded.raw`,
		instanceID, nodeID, nodeType, missionID, createdAt, createdBy, parentHash, string(raw),
	)
	return err
}

// UpsertLedgerEdge adds/replaces a ledger edge row.
func (d *DB) UpsertLedgerEdge(instanceID, edgeID, fromNode, toNode, edgeType string, raw []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.sql.Exec(
		`INSERT INTO ledger_edges(instance_id, edge_id, from_node, to_node, edge_type, raw)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(instance_id, edge_id) DO UPDATE SET
		    from_node=excluded.from_node,
		    to_node=excluded.to_node,
		    edge_type=excluded.edge_type,
		    raw=excluded.raw`,
		instanceID, edgeID, fromNode, toNode, edgeType, string(raw),
	)
	return err
}

// LedgerNode is the DB projection of a ledger_nodes row — reads the
// raw JSON into Raw so consumers can re-emit it verbatim.
type LedgerNode struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	MissionID  string          `json:"mission_id,omitempty"`
	CreatedAt  string          `json:"created_at"`
	CreatedBy  string          `json:"created_by,omitempty"`
	ParentHash string          `json:"parent_hash,omitempty"`
	Raw        json.RawMessage `json:"raw"`
}

// LedgerEdge is the DB projection of a ledger_edges row.
type LedgerEdge struct {
	ID       string          `json:"id"`
	From     string          `json:"from"`
	To       string          `json:"to"`
	Type     string          `json:"type"`
	Raw      json.RawMessage `json:"raw"`
}

// LedgerSnapshot is the payload of /api/session/:id/ledger.
type LedgerSnapshot struct {
	InstanceID string       `json:"instance_id"`
	Nodes      []LedgerNode `json:"nodes"`
	Edges      []LedgerEdge `json:"edges"`
}

// GetLedger returns all nodes + edges for a given session, in
// creation-order so the front-end can replay construction.
func (d *DB) GetLedger(instanceID string) (LedgerSnapshot, error) {
	snap := LedgerSnapshot{InstanceID: instanceID}
	nrows, err := d.sql.Query(
		`SELECT node_id, COALESCE(node_type,''), COALESCE(mission_id,''),
		        COALESCE(created_at,''), COALESCE(created_by,''),
		        COALESCE(parent_hash,''), raw
		   FROM ledger_nodes
		  WHERE instance_id = ?
		  ORDER BY created_at ASC, node_id ASC`,
		instanceID,
	)
	if err != nil {
		return snap, err
	}
	defer nrows.Close()
	for nrows.Next() {
		var n LedgerNode
		var raw string
		if err = nrows.Scan(&n.ID, &n.Type, &n.MissionID, &n.CreatedAt, &n.CreatedBy, &n.ParentHash, &raw); err != nil {
			return snap, err
		}
		n.Raw = json.RawMessage(raw)
		snap.Nodes = append(snap.Nodes, n)
	}
	if err = nrows.Err(); err != nil {
		return snap, err
	}

	erows, err := d.sql.Query(
		`SELECT edge_id, COALESCE(from_node,''), COALESCE(to_node,''),
		        COALESCE(edge_type,''), raw
		   FROM ledger_edges
		  WHERE instance_id = ?
		  ORDER BY edge_id ASC`,
		instanceID,
	)
	if err != nil {
		return snap, err
	}
	defer erows.Close()
	for erows.Next() {
		var e LedgerEdge
		var raw string
		if err := erows.Scan(&e.ID, &e.From, &e.To, &e.Type, &raw); err != nil {
			return snap, err
		}
		e.Raw = json.RawMessage(raw)
		snap.Edges = append(snap.Edges, e)
	}
	return snap, erows.Err()
}
