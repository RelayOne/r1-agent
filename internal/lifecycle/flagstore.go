package lifecycle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// FlagStore is the SQLite-backed first-time guard described in §5.2 of
// the spec. It holds one row per (tenant_id, event, user_id) triple
// recording the timestamp at which the milestone first fired. A
// second attempt for the same triple is a no-op: the INSERT OR IGNORE
// statement collides on the primary key and reports zero rows
// affected, which the IsFirstTime helper translates to "already
// fired, do not refire".
//
// The store is intentionally a separate SQLite file at
// ~/.r1/lifecycle.db so wiping lifecycle state never touches wisdom or
// mission rows. The driver is github.com/mattn/go-sqlite3 (matching
// internal/wisdom/sqlite.go) so we share a single CGo build flag
// across the repo. The spec mentions modernc.org/sqlite as a
// reference shape; the project convention is the mattn driver, and
// every other R1 SQLite store (wisdom, mission, eventlog) uses it —
// the FlagStore follows that convention rather than introducing a
// second driver into the build.
//
// All public methods are safe to call on a nil *FlagStore: they
// return (zero-value, errStoreClosed). Callers that opt out of
// lifecycle entirely (no env credentials) skip the FlagStore by
// construction.
type FlagStore struct {
	db   *sql.DB
	path string

	mu sync.Mutex
}

// Recognised first-time milestone names. The spec §5.2 defines the
// closed set; new milestones require both a constant here AND a
// matching mapping in the bus subscriber. The Validate helper checks
// inputs at the API boundary so a typo in a caller cannot poison the
// store with rows the subscriber will never look up.
const (
	EventSignup          = "signup"
	EventActivation      = "activation"
	EventFirstMission    = "first_mission"
	EventFirstCompletion = "first_completion"
)

// firstTimeEvents lists every milestone for which the FlagStore
// guards first-time delivery. Anti-trunc and budget-alert events are
// recurring engagement signals (per §6.5) and are intentionally NOT
// part of this list.
var firstTimeEvents = []string{
	EventSignup,
	EventActivation,
	EventFirstMission,
	EventFirstCompletion,
}

// errStoreClosed is returned by every method when called on a nil
// receiver. Callers treat this as "no first-time guard available —
// drop the milestone rather than risk a double-fire".
var errStoreClosed = errors.New("lifecycle: FlagStore closed")

// schemaSQL is the DDL run on every Open. CREATE TABLE IF NOT EXISTS
// means reopening an existing store is a no-op; adding a column is a
// follow-up migration step.
const schemaSQL = `
CREATE TABLE IF NOT EXISTS lifecycle_first_flags (
    tenant_id      TEXT    NOT NULL,
    event          TEXT    NOT NULL,
    user_id        TEXT    NOT NULL,
    first_fired_at INTEGER NOT NULL,
    PRIMARY KEY (tenant_id, event, user_id)
);

CREATE INDEX IF NOT EXISTS idx_lifecycle_user ON lifecycle_first_flags(user_id);
`

// DefaultPath returns the default on-disk location for the lifecycle
// flagstore. Mirrors the convention used by internal/wisdom and the
// other SQLite stores: a per-user file under ~/.r1/.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("lifecycle: resolve home dir: %w", err)
	}
	return filepath.Join(home, ".r1", "lifecycle.db"), nil
}

// Open opens or creates a FlagStore at the given path. The parent
// directory is created with 0o700 permissions if missing. WAL mode is
// enabled so concurrent reads from the bus subscriber's worker
// goroutines do not block the DSAR CLI's writes.
func Open(path string) (*FlagStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("lifecycle: empty store path")
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("lifecycle: ensure dir %q: %w", dir, err)
		}
	}
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("lifecycle: open %q: %w", path, err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("lifecycle: apply schema: %w", err)
	}
	return &FlagStore{db: db, path: path}, nil
}

// Path returns the on-disk path of the underlying SQLite file.
func (f *FlagStore) Path() string {
	if f == nil {
		return ""
	}
	return f.path
}

// IsFirstTime atomically checks-and-sets the (tenant, event, user)
// row in the first-flags table. Returns true exactly once across all
// replicas: the second caller observes a unique-constraint collision
// on INSERT OR IGNORE, ScannedRows reports zero affected rows, and
// the function returns false.
//
// On store error the function returns (false, err); the caller is
// expected to log + skip the milestone rather than risk a double-
// fire if the row already exists but the read failed.
func (f *FlagStore) IsFirstTime(ctx context.Context, tenantID, event, userID string) (bool, error) {
	if f == nil || f.db == nil {
		return false, errStoreClosed
	}
	if err := validateEvent(event); err != nil {
		return false, err
	}
	if strings.TrimSpace(userID) == "" {
		return false, errors.New("lifecycle: empty user_id")
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	res, err := f.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO lifecycle_first_flags
		    (tenant_id, event, user_id, first_fired_at)
		 VALUES (?, ?, ?, ?)`,
		tenantID, event, userID, time.Now().UTC().UnixMilli(),
	)
	if err != nil {
		return false, fmt.Errorf("lifecycle: IsFirstTime insert: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("lifecycle: IsFirstTime rows-affected: %w", err)
	}
	return n == 1, nil
}

// HasFired reports whether a milestone row exists for the triple, without
// inserting one. Used by tests and by the audit-log surface in the DSAR
// CLI to confirm pre-existing state. A read-only call: it never mutates
// the table.
func (f *FlagStore) HasFired(ctx context.Context, tenantID, event, userID string) (bool, error) {
	if f == nil || f.db == nil {
		return false, errStoreClosed
	}
	if err := validateEvent(event); err != nil {
		return false, err
	}
	var ts int64
	row := f.db.QueryRowContext(ctx,
		`SELECT first_fired_at FROM lifecycle_first_flags
		 WHERE tenant_id = ? AND event = ? AND user_id = ?`,
		tenantID, event, userID,
	)
	if err := row.Scan(&ts); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("lifecycle: HasFired query: %w", err)
	}
	return ts > 0, nil
}

// DeleteUser removes every milestone row for the given user across
// every tenant + event. Used by the DSAR flow: after a delete, a
// subsequent re-signup re-fires the signup milestone because no row
// exists to block it.
//
// Returns the number of rows removed. A user with no prior milestones
// returns (0, nil) — not an error.
func (f *FlagStore) DeleteUser(ctx context.Context, userID string) (int64, error) {
	if f == nil || f.db == nil {
		return 0, errStoreClosed
	}
	if strings.TrimSpace(userID) == "" {
		return 0, errors.New("lifecycle: empty user_id")
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	res, err := f.db.ExecContext(ctx,
		`DELETE FROM lifecycle_first_flags WHERE user_id = ?`,
		userID,
	)
	if err != nil {
		return 0, fmt.Errorf("lifecycle: DeleteUser: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("lifecycle: DeleteUser rows-affected: %w", err)
	}
	return n, nil
}

// CountAll returns the total number of milestone rows in the store.
// Used by the legacy-backfill migration in SeedLegacyUsers to decide
// whether the store is "fresh enough" to populate.
func (f *FlagStore) CountAll(ctx context.Context) (int64, error) {
	if f == nil || f.db == nil {
		return 0, errStoreClosed
	}
	var n int64
	row := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM lifecycle_first_flags`)
	if err := row.Scan(&n); err != nil {
		return 0, fmt.Errorf("lifecycle: CountAll: %w", err)
	}
	return n, nil
}

// SeedLegacyUsers writes a signup row for each user in legacyUsers so
// legacy users never get a backfilled welcome email when they next log
// in. The seed is idempotent — re-running it on a populated store is
// a no-op.
//
// Called by the operator once, after deploying the lifecycle
// subscriber, with the list of pre-existing user IDs from
// ~/.r1/users.db (or equivalent existing user store from A4). Spec T3.
func (f *FlagStore) SeedLegacyUsers(ctx context.Context, tenantID string, legacyUsers []string) (int64, error) {
	if f == nil || f.db == nil {
		return 0, errStoreClosed
	}
	if len(legacyUsers) == 0 {
		return 0, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("lifecycle: SeedLegacyUsers begin: %w", err)
	}
	stmt, err := tx.PrepareContext(ctx,
		`INSERT OR IGNORE INTO lifecycle_first_flags
		    (tenant_id, event, user_id, first_fired_at)
		 VALUES (?, ?, ?, ?)`,
	)
	if err != nil {
		_ = tx.Rollback()
		return 0, fmt.Errorf("lifecycle: SeedLegacyUsers prepare: %w", err)
	}
	defer stmt.Close()

	now := time.Now().UTC().UnixMilli()
	var inserted int64
	for _, userID := range legacyUsers {
		userID = strings.TrimSpace(userID)
		if userID == "" {
			continue
		}
		res, err := stmt.ExecContext(ctx, tenantID, EventSignup, userID, now)
		if err != nil {
			_ = tx.Rollback()
			return inserted, fmt.Errorf("lifecycle: SeedLegacyUsers insert %q: %w", userID, err)
		}
		n, _ := res.RowsAffected()
		inserted += n
	}
	if err := tx.Commit(); err != nil {
		return inserted, fmt.Errorf("lifecycle: SeedLegacyUsers commit: %w", err)
	}
	return inserted, nil
}

// Close releases the underlying SQLite handle. Safe to call multiple
// times; subsequent calls are no-ops.
func (f *FlagStore) Close() error {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.db == nil {
		return nil
	}
	err := f.db.Close()
	f.db = nil
	return err
}

// validateEvent enforces the closed event-name set. A typo in a
// caller (e.g. "first-mission" vs "first_mission") returns an error
// at the API boundary rather than persisting a row the subscriber
// will never read back.
func validateEvent(event string) error {
	for _, e := range firstTimeEvents {
		if e == event {
			return nil
		}
	}
	return fmt.Errorf("lifecycle: unknown first-time event %q (allowed: %s)",
		event, strings.Join(firstTimeEvents, ", "))
}

// FirstTimeEvents returns a defensive copy of the closed event-name
// set. Used by tests and by the bus subscriber's milestone wiring.
func FirstTimeEvents() []string {
	out := make([]string, len(firstTimeEvents))
	copy(out, firstTimeEvents)
	return out
}
