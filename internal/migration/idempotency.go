// idempotency.go — implementations of IdempotencyStore. Spec T10.
//
// Two implementations:
//
//   - MemoryIdempotencyStore — in-memory, lifetime-of-process. Used
//     by unit tests + by daemons that prefer to refuse re-imports
//     over restarts (rare; the persistent variant is the production
//     default).
//   - SQLiteIdempotencyStore — backed by a SQLite table.
//     `migration_imports (manifest_sha256 TEXT PRIMARY KEY,
//      new_session_id TEXT NOT NULL, imported_at TEXT NOT NULL,
//      source_session_id TEXT NOT NULL, source_host TEXT NOT NULL)`.
//     The destination daemon supplies a *sql.DB; we own the schema
//     migration (idempotent CREATE TABLE IF NOT EXISTS).
//
// Both satisfy IdempotencyStore. Wiring picks one based on whether
// the destination has a SQLite handle to share.

package migration

import (
	"database/sql"
	"errors"
	"sync"
	"time"
)

// MemoryIdempotencyStore is a sync.Mutex-guarded map of
// manifest_sha256 -> new_session_id. Process-lifetime only.
type MemoryIdempotencyStore struct {
	mu   sync.Mutex
	rows map[string]string
}

// NewMemoryIdempotencyStore returns a fresh in-memory store.
func NewMemoryIdempotencyStore() *MemoryIdempotencyStore {
	return &MemoryIdempotencyStore{rows: make(map[string]string)}
}

// Lookup implements IdempotencyStore.
func (m *MemoryIdempotencyStore) Lookup(manifestSHA256 string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, ok := m.rows[manifestSHA256]; ok {
		return id, nil
	}
	return "", nil
}

// Record implements IdempotencyStore.
func (m *MemoryIdempotencyStore) Record(manifestSHA256, newSessionID, sourceSessionID, sourceHost string) error {
	if manifestSHA256 == "" || newSessionID == "" {
		return errors.New("migration: idempotency Record: empty key or value")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[manifestSHA256] = newSessionID
	return nil
}

// SQLiteIdempotencyStore persists the idempotency table to a shared
// *sql.DB. The schema is created on first call via EnsureSchema; the
// caller (typically the daemon's SQLite migration runner) MAY call
// EnsureSchema explicitly at startup to surface migration errors at
// boot rather than on the first import.
type SQLiteIdempotencyStore struct {
	db *sql.DB
}

// NewSQLiteIdempotencyStore builds a store against an open SQLite
// connection. The schema is created lazily on first read/write —
// callers that want to surface migration errors early should invoke
// EnsureSchema themselves.
func NewSQLiteIdempotencyStore(db *sql.DB) *SQLiteIdempotencyStore {
	return &SQLiteIdempotencyStore{db: db}
}

// EnsureSchema runs the CREATE TABLE IF NOT EXISTS statement. Safe
// to call repeatedly. The idempotency table mirrors spec §T10:
// manifest_sha256 is the PK; new_session_id + imported_at +
// source_session_id + source_host are the recorded import metadata.
func (s *SQLiteIdempotencyStore) EnsureSchema() error {
	const ddl = `CREATE TABLE IF NOT EXISTS migration_imports (
		manifest_sha256    TEXT PRIMARY KEY,
		new_session_id     TEXT NOT NULL,
		imported_at        TEXT NOT NULL,
		source_session_id  TEXT NOT NULL,
		source_host        TEXT NOT NULL
	)`
	_, err := s.db.Exec(ddl)
	return err
}

// Lookup implements IdempotencyStore.
func (s *SQLiteIdempotencyStore) Lookup(manifestSHA256 string) (string, error) {
	if err := s.EnsureSchema(); err != nil {
		return "", err
	}
	row := s.db.QueryRow("SELECT new_session_id FROM migration_imports WHERE manifest_sha256 = ?", manifestSHA256)
	var id string
	if err := row.Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return id, nil
}

// Record implements IdempotencyStore. Uses INSERT OR IGNORE so a
// concurrent re-import (two daemons racing on the same bundle, which
// shouldn't happen in v1 but the safety belt is free) doesn't error.
func (s *SQLiteIdempotencyStore) Record(manifestSHA256, newSessionID, sourceSessionID, sourceHost string) error {
	if err := s.EnsureSchema(); err != nil {
		return err
	}
	const q = `INSERT OR IGNORE INTO migration_imports
		(manifest_sha256, new_session_id, imported_at, source_session_id, source_host)
		VALUES (?, ?, ?, ?, ?)`
	_, err := s.db.Exec(q, manifestSHA256, newSessionID, time.Now().UTC().Format(time.RFC3339Nano), sourceSessionID, sourceHost)
	return err
}
