package wisdom

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/ericmacdougall/stoke/internal/encryption"
)

// dsnEncryptionEnv is the feature flag gating at-rest encryption for the
// wisdom store. Setting it to "1" before the first NewSQLiteStore call
// loads the master key via the default keyring and opens SQLite through
// the sqlite3mc ChaCha20 DSN produced by encryption.BuildEncryptedDSN.
// Any other value (unset, "0", "false") leaves the plaintext DSN path
// intact so existing deployments keep working without migration. The
// env is read ONCE per open; rotating it mid-process has no effect.
const dsnEncryptionEnv = "STOKE_DB_ENCRYPTION"

// openDSN returns the DSN to hand to sql.Open("sqlite3", ...). When
// STOKE_DB_ENCRYPTION=1 it fetches the 32-byte master key from the
// keyring, runs it through encryption.BuildEncryptedDSN, and returns
// the sqlite3mc-flavoured URI. Otherwise it returns the plaintext
// `<path>?_journal_mode=WAL` form the store has always used. Errors
// are fail-closed: a missing keyring, unreadable key, or DSN build
// failure bubbles up rather than silently downgrading to plaintext —
// operators who opt in must never get an un-encrypted DB by accident.
func openDSN(path string) (string, error) {
	if os.Getenv(dsnEncryptionEnv) != "1" {
		return path + "?_journal_mode=WAL", nil
	}
	key, err := encryption.LoadOrGenerateMaster()
	if err != nil {
		return "", fmt.Errorf("wisdom: load master key: %w", err)
	}
	dsn, err := encryption.BuildEncryptedDSN(path, key)
	if err != nil {
		return "", fmt.Errorf("wisdom: build encrypted DSN: %w", err)
	}
	return dsn, nil
}

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

// sqliteMemorySchema defines the persistent memory store (S-9). It lives in
// the same database as wisdom_learnings but is fully isolated: separate table,
// its own FTS5 virtual table, and its own CRUD surface. Three memory types are
// supported:
//   - episodic: per-session outcomes + notable events
//   - semantic: repo-level facts (conventions, topology)
//   - procedural: how-to / prevention rules emitted by meta-reasoners
const sqliteMemorySchema = `
CREATE TABLE IF NOT EXISTS stoke_memories (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    type        TEXT NOT NULL,
    key         TEXT NOT NULL,
    content     TEXT NOT NULL,
    repo        TEXT,
    metadata    TEXT,
    created_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_mem_type ON stoke_memories(type);
CREATE INDEX IF NOT EXISTS idx_mem_repo ON stoke_memories(repo);
CREATE INDEX IF NOT EXISTS idx_mem_type_repo ON stoke_memories(type, repo);
`

// sqliteMemoryFTSSchema attaches an external-content FTS5 virtual table plus
// insert/delete/update triggers that keep it in sync with stoke_memories. This
// is applied opportunistically: if the active go-sqlite3 build was compiled
// without FTS5 (the default), the CREATE VIRTUAL TABLE fails, the store falls
// back to LIKE search in SearchMemories, and ListMemories/StoreMemory still
// work identically. To enable FTS5 rankings, build with `-tags sqlite_fts5`.
const sqliteMemoryFTSSchema = `
CREATE VIRTUAL TABLE IF NOT EXISTS stoke_memories_fts
    USING fts5(content, key, content=stoke_memories, content_rowid=id);
CREATE TRIGGER IF NOT EXISTS stoke_memories_ai AFTER INSERT ON stoke_memories BEGIN
  INSERT INTO stoke_memories_fts(rowid, content, key) VALUES (new.id, new.content, new.key);
END;
CREATE TRIGGER IF NOT EXISTS stoke_memories_ad AFTER DELETE ON stoke_memories BEGIN
  INSERT INTO stoke_memories_fts(stoke_memories_fts, rowid, content, key) VALUES('delete', old.id, old.content, old.key);
END;
CREATE TRIGGER IF NOT EXISTS stoke_memories_au AFTER UPDATE ON stoke_memories BEGIN
  INSERT INTO stoke_memories_fts(stoke_memories_fts, rowid, content, key) VALUES('delete', old.id, old.content, old.key);
  INSERT INTO stoke_memories_fts(rowid, content, key) VALUES (new.id, new.content, new.key);
END;
`

// Memory types. Callers should use these constants rather than raw strings.
const (
	MemoryTypeEpisodic   = "episodic"
	MemoryTypeSemantic   = "semantic"
	MemoryTypeProcedural = "procedural"
)

// Memory is a single row from the stoke_memories table.
type Memory struct {
	ID        int64
	Type      string
	Key       string
	Content   string
	Repo      string
	Metadata  map[string]string
	CreatedAt time.Time
}

// SQLiteStore is the persistent wisdom store backed by SQLite with WAL mode.
// It satisfies the same API as Store for drop-in replacement.
type SQLiteStore struct {
	db     *sql.DB
	mu     sync.Mutex
	hasFTS bool // whether stoke_memories_fts is attached (FTS5 compiled into the driver)
}

// NewSQLiteStore creates a persistent wisdom store at the given path.
// When STOKE_DB_ENCRYPTION=1 the underlying SQLite file is opened via
// the sqlite3mc ChaCha20 cipher, keyed by the 32-byte master key stored
// in the OS keyring (see encryption.LoadOrGenerateMaster). Otherwise the
// historical plaintext DSN is used. See openDSN for the gating logic.
func NewSQLiteStore(path string) (*SQLiteStore, error) {
	dsn, err := openDSN(path)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(sqliteSchema); err != nil {
		db.Close()
		return nil, err
	}
	// Memory base schema is applied sequentially and is idempotent; all CREATE
	// statements use IF NOT EXISTS so reopening an existing DB (even one that
	// predates S-9 and already has wisdom_learnings rows) is a no-op.
	if _, err := db.Exec(sqliteMemorySchema); err != nil {
		db.Close()
		return nil, err
	}
	s := &SQLiteStore{db: db}
	// FTS5 is opportunistic. The default go-sqlite3 build omits the fts5
	// module, so CREATE VIRTUAL TABLE ... USING fts5(...) errors with
	// "no such module: fts5". We catch that and fall back to LIKE search.
	// Build with `-tags sqlite_fts5` to enable FTS5 rankings.
	if _, err := db.Exec(sqliteMemoryFTSSchema); err == nil {
		s.hasFTS = true
	}
	return s, nil
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

// defaultMemoryLimit is used when callers pass limit=0.
const defaultMemoryLimit = 20

// StoreMemory persists a memory row and returns its new auto-increment ID.
// Metadata is serialized as JSON; a nil map is stored as NULL. The mtype
// should be one of MemoryTypeEpisodic/Semantic/Procedural, but this layer
// does not enforce the enum — callers and higher-level validators decide.
func (s *SQLiteStore) StoreMemory(mtype, key, content, repo string, meta map[string]string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var metaJSON sql.NullString
	if meta != nil {
		b, err := json.Marshal(meta)
		if err != nil {
			return 0, fmt.Errorf("marshal metadata: %w", err)
		}
		metaJSON = sql.NullString{String: string(b), Valid: true}
	}
	var repoVal sql.NullString
	if repo != "" {
		repoVal = sql.NullString{String: repo, Valid: true}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.Exec(
		`INSERT INTO stoke_memories (type, key, content, repo, metadata, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		mtype, key, content, repoVal, metaJSON, now,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SearchMemories runs an FTS5 MATCH against stoke_memories_fts. The query is
// passed through verbatim, so callers may use FTS5 operators (prefix `foo*`,
// phrase `"a b"`, boolean AND/OR, etc.). Results can additionally be filtered
// by a set of memory types and/or a specific repo. An empty types slice means
// "all types"; an empty repo means "all repos". limit<=0 falls back to
// defaultMemoryLimit.
//
// When FTS5 is not available (go-sqlite3 built without the sqlite_fts5 tag),
// SearchMemories transparently falls back to a LIKE scan over content+key.
// Prefix queries `foo*` are rewritten to `foo%`, bare queries are wrapped in
// `%...%`. The result set is the same shape; only ranking differs.
func (s *SQLiteStore) SearchMemories(query string, types []string, repo string, limit int) ([]Memory, error) {
	if query == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = defaultMemoryLimit
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.hasFTS {
		return s.searchFTS(query, types, repo, limit)
	}
	return s.searchLike(query, types, repo, limit)
}

// searchFTS implements the FTS5 MATCH path. Caller holds s.mu.
func (s *SQLiteStore) searchFTS(query string, types []string, repo string, limit int) ([]Memory, error) {
	var (
		conds []string
		args  []interface{}
	)
	conds = append(conds, "stoke_memories_fts MATCH ?")
	args = append(args, query)
	if len(types) > 0 {
		marks := make([]string, len(types))
		for i, t := range types {
			marks[i] = "?"
			args = append(args, t)
		}
		conds = append(conds, fmt.Sprintf("m.type IN (%s)", strings.Join(marks, ",")))
	}
	if repo != "" {
		conds = append(conds, "m.repo = ?")
		args = append(args, repo)
	}
	args = append(args, limit)

	sqlStr := fmt.Sprintf(`
		SELECT m.id, m.type, m.key, m.content, COALESCE(m.repo, ''), COALESCE(m.metadata, ''), m.created_at
		FROM stoke_memories m
		JOIN stoke_memories_fts f ON f.rowid = m.id
		WHERE %s
		ORDER BY rank
		LIMIT ?`, strings.Join(conds, " AND "))

	rows, err := s.db.Query(sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMemories(rows)
}

// searchLike is the non-FTS fallback. It translates FTS5-flavored queries
// into LIKE patterns: `foo*` (prefix) becomes `foo%`, a bare token becomes
// `%token%` (substring). Double-quoted phrases have their quotes stripped
// and are treated as literal substrings. Caller holds s.mu.
func (s *SQLiteStore) searchLike(query string, types []string, repo string, limit int) ([]Memory, error) {
	pat := likePatternFromFTSQuery(query)

	var (
		conds []string
		args  []interface{}
	)
	conds = append(conds, "(content LIKE ? OR key LIKE ?)")
	args = append(args, pat, pat)
	if len(types) > 0 {
		marks := make([]string, len(types))
		for i, t := range types {
			marks[i] = "?"
			args = append(args, t)
		}
		conds = append(conds, fmt.Sprintf("type IN (%s)", strings.Join(marks, ",")))
	}
	if repo != "" {
		conds = append(conds, "repo = ?")
		args = append(args, repo)
	}
	args = append(args, limit)

	sqlStr := fmt.Sprintf(`
		SELECT id, type, key, content, COALESCE(repo, ''), COALESCE(metadata, ''), created_at
		FROM stoke_memories
		WHERE %s
		ORDER BY id DESC
		LIMIT ?`, strings.Join(conds, " AND "))

	rows, err := s.db.Query(sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMemories(rows)
}

// likePatternFromFTSQuery converts a subset of FTS5 query syntax into a
// LIKE-compatible pattern. This is a best-effort shim for the fallback
// path; full FTS5 operators (AND/OR/NEAR) degrade to a single substring.
//
// FTS5 `foo*` means "any token starting with foo anywhere in the document".
// LIKE has no word boundary, so we approximate with `%foo%` — which is a
// superset: it also matches "unfoo" mid-word. Good enough for a dev fallback.
func likePatternFromFTSQuery(q string) string {
	q = strings.TrimSpace(q)
	// Strip wrapping double quotes from phrase queries.
	if len(q) >= 2 && q[0] == '"' && q[len(q)-1] == '"' {
		q = q[1 : len(q)-1]
	}
	// Strip LIKE wildcards the caller may have supplied to keep the
	// pattern meaningful (we wrap our own `%` below).
	q = strings.NewReplacer("%", "", "_", "").Replace(q)
	// Drop the FTS5 prefix marker and substring-match.
	q = strings.TrimSuffix(q, "*")
	return "%" + q + "%"
}

// ListMemories returns rows filtered only by type and repo, newest first.
// Useful for diagnostic dumps ("what's in memory for this repo?") where FTS
// ranking is not needed.
func (s *SQLiteStore) ListMemories(types []string, repo string, limit int) ([]Memory, error) {
	if limit <= 0 {
		limit = defaultMemoryLimit
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var (
		conds []string
		args  []interface{}
	)
	if len(types) > 0 {
		marks := make([]string, len(types))
		for i, t := range types {
			marks[i] = "?"
			args = append(args, t)
		}
		conds = append(conds, fmt.Sprintf("type IN (%s)", strings.Join(marks, ",")))
	}
	if repo != "" {
		conds = append(conds, "repo = ?")
		args = append(args, repo)
	}
	args = append(args, limit)

	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	sqlStr := fmt.Sprintf(`
		SELECT id, type, key, content, COALESCE(repo, ''), COALESCE(metadata, ''), created_at
		FROM stoke_memories
		%s
		ORDER BY id DESC
		LIMIT ?`, where)

	rows, err := s.db.Query(sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMemories(rows)
}

// DeleteMemory removes a single memory row by ID. The AFTER DELETE trigger
// tombstones the matching FTS row, so subsequent SearchMemories will not
// return a stale hit.
func (s *SQLiteStore) DeleteMemory(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`DELETE FROM stoke_memories WHERE id = ?`, id)
	return err
}

// scanMemories decodes rows in the canonical
// (id, type, key, content, repo, metadata, created_at) column order.
func scanMemories(rows *sql.Rows) ([]Memory, error) {
	var out []Memory
	for rows.Next() {
		var m Memory
		var metaRaw string
		var createdAt string
		if err := rows.Scan(&m.ID, &m.Type, &m.Key, &m.Content, &m.Repo, &metaRaw, &createdAt); err != nil {
			return nil, err
		}
		if metaRaw != "" {
			// Best-effort decode: malformed metadata is skipped, not fatal.
			_ = json.Unmarshal([]byte(metaRaw), &m.Metadata)
		}
		if t, err := time.Parse(time.RFC3339, createdAt); err == nil {
			m.CreatedAt = t
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
