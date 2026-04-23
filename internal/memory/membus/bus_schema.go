// Package membus — bus_schema.go
//
// MEMBUS-core: schema + PRAGMA baseline for the scoped live worker-to-worker
// memory bus. This is the minimal core slice of specs/memory-bus.md; full
// tier hierarchy, retention wiring, ledger emission, and cross-session
// queries ship in later passes.
//
// Lives in subpackage `membus` rather than `memory` because internal/memory
// already owns a `type Scope string` (memory-full-stack's hierarchical
// Global/Repo/Task/Auto enum). This spec's `type Scope string` is a
// visibility enum (Session/SessionStep/Worker/AllSessions/Global/Always);
// spec §2 explicitly calls them out as sibling systems sharing a DB handle.
// Subpackage isolation lets both use the clean `Scope` name without collision.
//
// The table is intentionally forward-compatible with the richer columns in
// §3 of the spec (content_encrypted, metadata, read_count, etc.) so the
// follow-up passes don't need a schema migration — they only add writers
// and readers for the already-present columns.
package membus

import (
	"database/sql"
	"fmt"
)

// busSchema is the DDL applied by migrateBus. It is idempotent via
// IF NOT EXISTS and safe to run on every process start.
const busSchema = `
CREATE TABLE IF NOT EXISTS stoke_memory_bus (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at        TEXT    NOT NULL,
    expires_at        TEXT,
    scope             TEXT    NOT NULL,
    scope_target      TEXT    NOT NULL DEFAULT '',
    session_id        TEXT    NOT NULL DEFAULT '',
    step_id           TEXT    NOT NULL DEFAULT '',
    task_id           TEXT    NOT NULL DEFAULT '',
    author            TEXT    NOT NULL DEFAULT '',
    key               TEXT    NOT NULL,
    content           TEXT    NOT NULL,
    content_encrypted BLOB,
    content_hash      TEXT    NOT NULL DEFAULT '',
    tags              TEXT    NOT NULL DEFAULT '[]',
    metadata          TEXT    NOT NULL DEFAULT '{}',
    read_count        INTEGER NOT NULL DEFAULT 0,
    last_read_at      TEXT,
    UNIQUE (scope, scope_target, key)
);

CREATE INDEX IF NOT EXISTS idx_membus_scope     ON stoke_memory_bus(scope, scope_target);
CREATE INDEX IF NOT EXISTS idx_membus_session   ON stoke_memory_bus(session_id);
CREATE INDEX IF NOT EXISTS idx_membus_step      ON stoke_memory_bus(session_id, step_id);
CREATE INDEX IF NOT EXISTS idx_membus_task      ON stoke_memory_bus(task_id);
CREATE INDEX IF NOT EXISTS idx_membus_expires   ON stoke_memory_bus(expires_at);
CREATE INDEX IF NOT EXISTS idx_membus_id_cursor ON stoke_memory_bus(id);
`

// busPragmas is the PRAGMA baseline from §5.5 of the spec. Applied once per
// handle. We do NOT set journal_mode here because the handle may be shared
// with other packages that already selected WAL; re-selecting is a no-op but
// PRAGMA journal_mode returns a row which database/sql would discard
// harmlessly. We keep it explicit for parity with the spec.
var busPragmas = []string{
	`PRAGMA journal_mode       = WAL`,
	`PRAGMA synchronous        = NORMAL`,
	`PRAGMA busy_timeout       = 5000`,
	`PRAGMA temp_store         = MEMORY`,
	`PRAGMA cache_size         = -65536`,
	`PRAGMA mmap_size          = 268435456`,
	`PRAGMA journal_size_limit = 67108864`,
}

// migrateBus applies busSchema to db. Idempotent — safe to call on every
// open. Returns a wrapped error on failure.
func migrateBus(db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("memory: migrateBus: nil db")
	}
	if _, err := db.Exec(busSchema); err != nil {
		return fmt.Errorf("memory: apply bus schema: %w", err)
	}
	return nil
}

// applyBusPragmas runs the baseline PRAGMAs on db. Errors on the first
// failing statement. journal_mode is a query statement that returns a row;
// db.Exec on it is valid and returns no error on most drivers.
func applyBusPragmas(db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("memory: applyBusPragmas: nil db")
	}
	for _, p := range busPragmas {
		if _, err := db.Exec(p); err != nil {
			return fmt.Errorf("memory: pragma %q: %w", p, err)
		}
	}
	return nil
}
