package session

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/ericmacdougall/stoke/internal/plan"
	"github.com/ericmacdougall/stoke/internal/r1dir"
)

// SQLStore is a SQLite-backed session store for crash recovery and learning.
type SQLStore struct {
	db   *sql.DB
	root string
}

// NewSQLStore opens (or creates) a SQLite database at
// `<projectRoot>/<resolved-root>/session.db`, where the resolved root is
// `.r1/` when that directory already exists, `.stoke/` when only the
// legacy layout exists, and `.r1/` by default for brand-new projects (so
// fresh sessions land on the post-rename layout per
// work-r1-rename.md §S1-5).
//
// The SQLite WAL / DB files are not dual-written: SQLite owns one
// authoritative file tree per store, and the operator-driven
// `r1 migrate-session` helper handles the one-time copy from the
// legacy path when required. Readers that still scan `.stoke/` can
// continue to do so until they migrate; they just see a stale DB.
func NewSQLStore(projectRoot string) (*SQLStore, error) {
	canonical := filepath.Join(projectRoot, r1dir.Canonical)
	legacy := filepath.Join(projectRoot, r1dir.Legacy)

	var root string
	switch {
	case dirExists(canonical):
		root = canonical
	case dirExists(legacy):
		root = legacy
	default:
		root = canonical
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, fmt.Errorf("create session dir: %w", err)
	}
	dbPath := filepath.Join(root, "session.db")

	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open session.db: %w", err)
	}
	s := &SQLStore{db: db, root: root}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *SQLStore) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS session_state (
			id         INTEGER PRIMARY KEY CHECK (id = 1),
			plan_id    TEXT NOT NULL,
			tasks_json TEXT NOT NULL,
			total_cost REAL DEFAULT 0,
			started_at TEXT NOT NULL,
			saved_at   TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS attempts (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id      TEXT NOT NULL,
			number       INTEGER NOT NULL,
			success      INTEGER NOT NULL,
			cost_usd     REAL DEFAULT 0,
			duration_ns  INTEGER DEFAULT 0,
			error        TEXT,
			fail_class   TEXT,
			fail_summary TEXT,
			root_cause   TEXT,
			diff_summary TEXT,
			learned_fix  TEXT,
			created_at   TEXT NOT NULL DEFAULT (datetime('now'))
		);
		CREATE INDEX IF NOT EXISTS idx_attempts_task ON attempts(task_id);
		CREATE TABLE IF NOT EXISTS patterns (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			issue       TEXT NOT NULL UNIQUE,
			fix         TEXT,
			occurrences INTEGER DEFAULT 1
		);
	`)
	return err
}

// Close closes the database.
func (s *SQLStore) Close() error { return s.db.Close() }

// --- Session state ---

func (s *SQLStore) SaveState(state *State) error {
	state.SavedAt = time.Now()
	tasksJSON, err := json.Marshal(state.Tasks)
	if err != nil {
		return fmt.Errorf("marshal tasks: %w", err)
	}
	_, err = s.db.Exec(`
		INSERT OR REPLACE INTO session_state (id, plan_id, tasks_json, total_cost, started_at, saved_at)
		VALUES (1, ?, ?, ?, ?, ?)`,
		state.PlanID, string(tasksJSON), state.TotalCostUSD,
		state.StartedAt.Format(time.RFC3339), state.SavedAt.Format(time.RFC3339))
	return err
}

func (s *SQLStore) LoadState() (*State, error) {
	row := s.db.QueryRow("SELECT plan_id, tasks_json, total_cost, started_at, saved_at FROM session_state WHERE id=1")
	var planID, tasksJSON, startedStr, savedStr string
	var cost float64
	if err := row.Scan(&planID, &tasksJSON, &cost, &startedStr, &savedStr); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	var tasks []plan.Task
	if err := json.Unmarshal([]byte(tasksJSON), &tasks); err != nil {
		return nil, fmt.Errorf("unmarshal tasks: %w", err)
	}
	started, err := time.Parse(time.RFC3339, startedStr)
	if err != nil {
		return nil, fmt.Errorf("parse started_at: %w", err)
	}
	saved, err := time.Parse(time.RFC3339, savedStr)
	if err != nil {
		return nil, fmt.Errorf("parse saved_at: %w", err)
	}
	return &State{PlanID: planID, Tasks: tasks, TotalCostUSD: cost, StartedAt: started, SavedAt: saved}, nil
}

func (s *SQLStore) ClearState() error {
	_, err := s.db.Exec("DELETE FROM session_state WHERE id=1")
	return err
}

// --- Attempts ---

func (s *SQLStore) SaveAttempt(a Attempt) error {
	_, err := s.db.Exec(`
		INSERT INTO attempts (task_id, number, success, cost_usd, duration_ns, error, fail_class, fail_summary, root_cause, diff_summary, learned_fix)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.TaskID, a.Number, boolToInt(a.Success), a.CostUSD, int64(a.Duration),
		a.Error, a.FailClass, a.FailSummary, a.RootCause, a.DiffSummary, a.LearnedFix)
	if err != nil {
		return err
	}

	// Auto-learn: if this attempt succeeded and the previous one failed, record the pattern
	if a.Success && a.Number > 1 {
		var prevSummary string
		s.db.QueryRow("SELECT fail_summary FROM attempts WHERE task_id=? AND number=? AND success=0",
			a.TaskID, a.Number-1).Scan(&prevSummary)
		if prevSummary != "" {
			s.addPattern(prevSummary, fmt.Sprintf("resolved on attempt %d", a.Number))
		}
	}
	return nil
}

func (s *SQLStore) LoadAttempts(taskID string) ([]Attempt, error) {
	rows, err := s.db.Query("SELECT task_id, number, success, cost_usd, duration_ns, error, fail_class, fail_summary, root_cause, diff_summary, learned_fix FROM attempts WHERE task_id=? ORDER BY number", taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Attempt
	for rows.Next() {
		var a Attempt
		var successInt int
		var durNs int64
		if err := rows.Scan(&a.TaskID, &a.Number, &successInt, &a.CostUSD, &durNs, &a.Error, &a.FailClass, &a.FailSummary, &a.RootCause, &a.DiffSummary, &a.LearnedFix); err != nil {
			return nil, fmt.Errorf("scan attempt: %w", err)
		}
		a.Success = successInt != 0
		a.Duration = time.Duration(durNs)
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate attempts: %w", err)
	}
	return out, nil
}

// --- Learned patterns ---

func (s *SQLStore) addPattern(issue, fix string) {
	s.db.Exec("INSERT INTO patterns (issue, fix) VALUES (?, ?) ON CONFLICT(issue) DO UPDATE SET occurrences=occurrences+1, fix=?", issue, fix, fix)
}

func (s *SQLStore) SaveLearning(l *Learning) error {
	if _, err := s.db.Exec("DELETE FROM patterns"); err != nil {
		return fmt.Errorf("delete patterns: %w", err)
	}
	for _, p := range l.Patterns {
		if _, err := s.db.Exec("INSERT INTO patterns (issue, fix, occurrences) VALUES (?, ?, ?)", p.Issue, p.Fix, p.Occurrences); err != nil {
			return fmt.Errorf("insert pattern: %w", err)
		}
	}
	return nil
}

func (s *SQLStore) LoadLearning() (*Learning, error) {
	rows, err := s.db.Query("SELECT issue, fix, occurrences FROM patterns ORDER BY occurrences DESC")
	if err != nil {
		return &Learning{}, nil
	}
	defer rows.Close()
	l := &Learning{}
	for rows.Next() {
		var p Pattern
		if err := rows.Scan(&p.Issue, &p.Fix, &p.Occurrences); err != nil {
			return nil, fmt.Errorf("scan pattern: %w", err)
		}
		l.Patterns = append(l.Patterns, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate patterns: %w", err)
	}
	return l, nil
}

// --- Stats ---

// Stats returns aggregate metrics across all attempts.
func (s *SQLStore) Stats() (totalAttempts, successes, failures int, totalCost float64) {
	s.db.QueryRow("SELECT COUNT(*), COALESCE(SUM(CASE WHEN success=1 THEN 1 ELSE 0 END),0), COALESCE(SUM(CASE WHEN success=0 THEN 1 ELSE 0 END),0), COALESCE(SUM(cost_usd),0) FROM attempts").
		Scan(&totalAttempts, &successes, &failures, &totalCost)
	return
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
