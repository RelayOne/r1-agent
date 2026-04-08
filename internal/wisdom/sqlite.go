package wisdom

import (
	"database/sql"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

const sqliteSchema = `
CREATE TABLE IF NOT EXISTS wisdom_learnings (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id         TEXT,
    mission_id      TEXT,
    category        TEXT NOT NULL,
    description     TEXT NOT NULL,
    file_path       TEXT,
    failure_pattern TEXT,
    skill_match     TEXT,
    use_count       INTEGER NOT NULL DEFAULT 0,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_wisdom_category ON wisdom_learnings(category);
CREATE INDEX IF NOT EXISTS idx_wisdom_failure_pattern ON wisdom_learnings(failure_pattern);
CREATE INDEX IF NOT EXISTS idx_wisdom_skill_match ON wisdom_learnings(skill_match);
CREATE INDEX IF NOT EXISTS idx_wisdom_task ON wisdom_learnings(task_id);
CREATE INDEX IF NOT EXISTS idx_wisdom_mission ON wisdom_learnings(mission_id);
`

// SQLiteStore is the persistent wisdom store backed by SQLite with WAL mode.
// It satisfies the same API as Store for drop-in replacement.
type SQLiteStore struct {
	db *sql.DB
	mu sync.Mutex
}

// NewSQLiteStore creates a persistent wisdom store at the given path.
func NewSQLiteStore(path string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(sqliteSchema); err != nil {
		db.Close()
		return nil, err
	}
	return &SQLiteStore{db: db}, nil
}

// Record persists a learning to SQLite.
func (s *SQLiteStore) Record(taskID string, l Learning) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC().Format(time.RFC3339)
	s.db.Exec(
		`INSERT INTO wisdom_learnings (task_id, category, description, file_path, failure_pattern, use_count, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, 0, ?, ?)`,
		taskID, l.Category.String(), l.Description, l.File, l.FailurePattern, now, now,
	)
}

// Learnings returns all stored learnings.
func (s *SQLiteStore) Learnings() []Learning {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(
		`SELECT task_id, category, description, file_path, failure_pattern FROM wisdom_learnings ORDER BY id`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []Learning
	for rows.Next() {
		var l Learning
		var cat, file, fp string
		if err := rows.Scan(&l.TaskID, &cat, &l.Description, &file, &fp); err != nil {
			continue
		}
		l.Category = ParseCategory(cat)
		l.File = file
		l.FailurePattern = fp
		out = append(out, l)
	}
	return out
}

// ForPrompt formats learnings as markdown for prompt injection.
// Delegates to the same formatting logic as the in-memory store.
func (s *SQLiteStore) ForPrompt() string {
	learnings := s.Learnings()
	if len(learnings) == 0 {
		return ""
	}

	// Use the same partitioning as the in-memory store
	tmpStore := &Store{learnings: learnings}
	return tmpStore.ForPrompt()
}

// FindByPattern returns the first learning matching a failure pattern hash.
func (s *SQLiteStore) FindByPattern(hash string) *Learning {
	if hash == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	row := s.db.QueryRow(
		`SELECT task_id, category, description, file_path, failure_pattern
		 FROM wisdom_learnings WHERE failure_pattern = ? LIMIT 1`, hash)

	var l Learning
	var cat, file, fp string
	if err := row.Scan(&l.TaskID, &cat, &l.Description, &file, &fp); err != nil {
		return nil
	}
	l.Category = ParseCategory(cat)
	l.File = file
	l.FailurePattern = fp
	return &l
}

// Close closes the underlying database connection.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// Count returns the total number of learnings.
func (s *SQLiteStore) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	var count int
	s.db.QueryRow(`SELECT COUNT(*) FROM wisdom_learnings`).Scan(&count)
	return count
}
