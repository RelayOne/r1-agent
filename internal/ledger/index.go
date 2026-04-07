package ledger

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"
)

// Index is a disposable SQLite acceleration index over the ledger graph.
// It can be rebuilt from the filesystem store at any time.
type Index struct {
	db   *sql.DB
	path string
}

// NewIndex opens or creates the SQLite index at {rootDir}/.index.db.
func NewIndex(rootDir string) (*Index, error) {
	dbPath := filepath.Join(rootDir, ".index.db")
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL")
	if err != nil {
		return nil, fmt.Errorf("open index db: %w", err)
	}
	idx := &Index{db: db, path: dbPath}
	if err := idx.CreateTables(); err != nil {
		db.Close()
		return nil, err
	}
	return idx, nil
}

// CreateTables ensures the schema exists.
func (idx *Index) CreateTables() error {
	const schema = `
	CREATE TABLE IF NOT EXISTS nodes (
		id            TEXT PRIMARY KEY,
		type          TEXT NOT NULL,
		schema_version INTEGER NOT NULL,
		created_at    TEXT NOT NULL,
		created_by    TEXT NOT NULL,
		mission_id    TEXT
	);
	CREATE TABLE IF NOT EXISTS edges (
		from_id  TEXT NOT NULL,
		to_id    TEXT NOT NULL,
		type     TEXT NOT NULL,
		PRIMARY KEY (from_id, to_id, type)
	);
	CREATE INDEX IF NOT EXISTS idx_nodes_type ON nodes(type);
	CREATE INDEX IF NOT EXISTS idx_nodes_mission ON nodes(mission_id);
	CREATE INDEX IF NOT EXISTS idx_nodes_created_by ON nodes(created_by);
	CREATE INDEX IF NOT EXISTS idx_nodes_created_at ON nodes(created_at);
	CREATE INDEX IF NOT EXISTS idx_edges_from ON edges(from_id);
	CREATE INDEX IF NOT EXISTS idx_edges_to ON edges(to_id);
	`
	_, err := idx.db.Exec(schema)
	if err != nil {
		return fmt.Errorf("create index tables: %w", err)
	}
	return nil
}

// Drop removes all data from the index tables.
func (idx *Index) Drop() error {
	_, err := idx.db.Exec("DROP TABLE IF EXISTS nodes; DROP TABLE IF EXISTS edges;")
	return err
}

// Close closes the underlying database.
func (idx *Index) Close() error {
	if idx.db != nil {
		return idx.db.Close()
	}
	return nil
}

// DeleteDB closes the database and removes the file.
func (idx *Index) DeleteDB() error {
	if err := idx.Close(); err != nil {
		return err
	}
	return os.Remove(idx.path)
}

// InsertNode adds a node record to the index.
func (idx *Index) InsertNode(n Node) error {
	_, err := idx.db.Exec(
		`INSERT OR IGNORE INTO nodes (id, type, schema_version, created_at, created_by, mission_id)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		n.ID, n.Type, n.SchemaVersion,
		n.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z"),
		n.CreatedBy, n.MissionID,
	)
	return err
}

// InsertEdge adds an edge record to the index.
func (idx *Index) InsertEdge(e Edge) error {
	_, err := idx.db.Exec(
		`INSERT OR IGNORE INTO edges (from_id, to_id, type) VALUES (?, ?, ?)`,
		e.From, e.To, string(e.Type),
	)
	return err
}

// QueryNodes returns node IDs matching the given filter.
func (idx *Index) QueryNodes(f QueryFilter) ([]NodeID, error) {
	query := "SELECT id FROM nodes WHERE 1=1"
	var args []interface{}

	if f.Type != "" {
		query += " AND type = ?"
		args = append(args, f.Type)
	}
	if f.MissionID != "" {
		query += " AND mission_id = ?"
		args = append(args, f.MissionID)
	}
	if f.CreatedBy != "" {
		query += " AND created_by = ?"
		args = append(args, f.CreatedBy)
	}
	if f.Since != nil {
		query += " AND created_at >= ?"
		args = append(args, f.Since.UTC().Format("2006-01-02T15:04:05.999999999Z"))
	}
	if f.Until != nil {
		query += " AND created_at <= ?"
		args = append(args, f.Until.UTC().Format("2006-01-02T15:04:05.999999999Z"))
	}
	query += " ORDER BY created_at ASC"
	if f.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", f.Limit)
	}

	rows, err := idx.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []NodeID
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// EdgesFrom returns target node IDs for edges originating from the given node
// with the specified edge type.
func (idx *Index) EdgesFrom(nodeID NodeID, et EdgeType) ([]NodeID, error) {
	rows, err := idx.db.Query(
		"SELECT to_id FROM edges WHERE from_id = ? AND type = ?",
		nodeID, string(et),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []NodeID
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// EdgesTo returns source node IDs for edges pointing to the given node
// with the specified edge type. For supersedes resolution, this finds
// nodes that supersede the given node.
func (idx *Index) EdgesTo(nodeID NodeID, et EdgeType) ([]NodeID, error) {
	rows, err := idx.db.Query(
		"SELECT from_id FROM edges WHERE to_id = ? AND type = ?",
		nodeID, string(et),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []NodeID
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
