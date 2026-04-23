// Package research provides persistent, indexed research storage.
//
// When an agent researches something (reads documentation, searches code,
// fetches web resources), the findings are stored here so they persist across
// context windows, agent handoffs, and sessions. This prevents re-researching
// known information and enables recall by topic, keyword, or file.
//
// Storage is SQLite-backed with full-text search via FTS5. Each research entry
// is tagged with topics for structured retrieval and has full-text content
// searchable via SQL MATCH queries.
//
// Relevance scoring accounts for recency, use count, and text match quality.
// Entries can decay over time and be pruned when stale.
package research

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/ericmacdougall/stoke/internal/vecindex"
)

// Entry is a single piece of stored research.
type Entry struct {
	ID        string    `json:"id"`
	MissionID string    `json:"mission_id,omitempty"` // which mission this relates to (empty = global)
	Topic     string    `json:"topic"`                // primary topic/category
	Query     string    `json:"query"`                // the original question/search
	Content   string    `json:"content"`              // the research finding
	Source    string    `json:"source,omitempty"`      // where the info came from (url, file, etc.)
	Tags      []string  `json:"tags"`                 // for structured retrieval
	UseCount  int       `json:"use_count"`            // how many times recalled
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SearchResult is an entry with a relevance score.
type SearchResult struct {
	Entry Entry   `json:"entry"`
	Score float64 `json:"score"` // relevance score, higher is better
}

// Store is the SQLite-backed research persistence layer with FTS5 search
// and optional vector-based semantic search via vecindex.
type Store struct {
	db     *sql.DB
	dbPath string
	hasFTS bool // true if FTS5 is available

	// Vector index for semantic search — built from entry content on open,
	// updated incrementally on Add. Uses bag-of-words embedding as fallback
	// when no external embedding service is configured.
	vecIdx *vecindex.Index
	vocab  []string // vocabulary for bag-of-words embedding
	vecMu  sync.RWMutex // protects vecIdx and vocab from concurrent access
}

// NewStore opens or creates a research database at the given directory.
func NewStore(dir string) (*Store, error) {
	if dir == "" {
		return nil, fmt.Errorf("research store directory must not be empty")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create research store directory: %w", err)
	}

	dbPath := filepath.Join(dir, "research.db")
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open research.db: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping research.db: %w", err)
	}

	s := &Store{db: db, dbPath: dbPath}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate research.db: %w", err)
	}

	// Build vector index from existing entries for semantic search
	s.buildVectorIndex()

	log.Printf("[research] store opened at %s", dbPath)
	return s, nil
}

// Close closes the database connection.
func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) migrate() error {
	coreMigrations := []string{
		`CREATE TABLE IF NOT EXISTS entries (
			id         TEXT PRIMARY KEY NOT NULL,
			mission_id TEXT NOT NULL DEFAULT '',
			topic      TEXT NOT NULL,
			query      TEXT NOT NULL,
			content    TEXT NOT NULL,
			source     TEXT NOT NULL DEFAULT '',
			tags       TEXT NOT NULL DEFAULT '',
			use_count  INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_entries_topic ON entries(topic)`,
		`CREATE INDEX IF NOT EXISTS idx_entries_mission ON entries(mission_id)`,
	}
	for _, m := range coreMigrations {
		if _, err := s.db.Exec(m); err != nil {
			return fmt.Errorf("migration failed: %w\nSQL: %s", err, m)
		}
	}

	// FTS5 for full-text search — optional, degrades gracefully to LIKE search
	s.hasFTS = s.tryFTS5()
	return nil
}

// tryFTS5 attempts to create FTS5 virtual table and sync triggers.
// Returns true if FTS5 is available, false otherwise (falls back to LIKE).
func (s *Store) tryFTS5() bool {
	ftsMigrations := []string{
		`CREATE VIRTUAL TABLE IF NOT EXISTS entries_fts USING fts5(
			id UNINDEXED, topic, query, content, tags,
			content=entries, content_rowid=rowid
		)`,
		`CREATE TRIGGER IF NOT EXISTS entries_ai AFTER INSERT ON entries BEGIN
			INSERT INTO entries_fts(rowid, id, topic, query, content, tags)
			VALUES (new.rowid, new.id, new.topic, new.query, new.content, new.tags);
		END`,
		`CREATE TRIGGER IF NOT EXISTS entries_ad AFTER DELETE ON entries BEGIN
			INSERT INTO entries_fts(entries_fts, rowid, id, topic, query, content, tags)
			VALUES ('delete', old.rowid, old.id, old.topic, old.query, old.content, old.tags);
		END`,
		`CREATE TRIGGER IF NOT EXISTS entries_au AFTER UPDATE ON entries BEGIN
			INSERT INTO entries_fts(entries_fts, rowid, id, topic, query, content, tags)
			VALUES ('delete', old.rowid, old.id, old.topic, old.query, old.content, old.tags);
			INSERT INTO entries_fts(rowid, id, topic, query, content, tags)
			VALUES (new.rowid, new.id, new.topic, new.query, new.content, new.tags);
		END`,
	}
	for _, m := range ftsMigrations {
		if _, err := s.db.Exec(m); err != nil {
			log.Printf("[research] FTS5 not available, falling back to LIKE search: %v", err)
			return false
		}
	}
	return true
}

// --- CRUD ---

// Add stores a new research entry. If an entry with the same ID exists,
// it updates the content and increments the use count.
func (s *Store) Add(e *Entry) error {
	if e.ID == "" {
		return fmt.Errorf("entry ID must not be empty")
	}
	if e.Topic == "" {
		return fmt.Errorf("entry topic must not be empty")
	}
	if e.Content == "" {
		return fmt.Errorf("entry content must not be empty")
	}

	now := time.Now().UTC()
	if e.CreatedAt.IsZero() {
		e.CreatedAt = now
	}
	e.UpdatedAt = now
	if e.Tags == nil {
		e.Tags = []string{}
	}

	tagsStr := strings.Join(e.Tags, ",")
	_, err := s.db.Exec(`
		INSERT INTO entries (id, mission_id, topic, query, content, source, tags, use_count, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			content=excluded.content, source=excluded.source, tags=excluded.tags,
			use_count=use_count+1, updated_at=excluded.updated_at`,
		e.ID, e.MissionID, e.Topic, e.Query, e.Content, e.Source, tagsStr,
		e.UseCount, e.CreatedAt.Format(time.RFC3339Nano), e.UpdatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("add entry: %w", err)
	}

	// Update vector index for semantic search
	s.indexEntry(e.ID, e.Topic, e.Query, e.Content)

	return nil
}

// Get retrieves an entry by ID and increments its use count.
func (s *Store) Get(id string) (*Entry, error) {
	if id == "" {
		return nil, fmt.Errorf("entry ID must not be empty")
	}

	row := s.db.QueryRow(`
		SELECT id, mission_id, topic, query, content, source, tags, use_count, created_at, updated_at
		FROM entries WHERE id = ?`, id)

	e, err := s.scanEntry(row)
	if err != nil || e == nil {
		return e, err
	}

	// Increment use count
	s.db.Exec("UPDATE entries SET use_count=use_count+1 WHERE id=?", id)
	e.UseCount++
	return e, nil
}

// Delete removes an entry by ID.
func (s *Store) Delete(id string) error {
	result, err := s.db.Exec("DELETE FROM entries WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete entry: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("entry %q not found", id)
	}
	return nil
}

// --- Search ---

// Search performs full-text search across topic, query, content, and tags.
// Returns results ordered by relevance score (FTS5 rank).
func (s *Store) Search(query string, limit int) ([]SearchResult, error) {
	if query == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}

	if !s.hasFTS {
		return s.searchFallback(query, limit)
	}

	// Sanitize query for FTS5: escape quotes, add prefix matching
	ftsQuery := sanitizeFTSQuery(query)

	rows, err := s.db.Query(`
		SELECT e.id, e.mission_id, e.topic, e.query, e.content, e.source, e.tags,
		       e.use_count, e.created_at, e.updated_at,
		       rank * -1.0 AS score
		FROM entries_fts f
		JOIN entries e ON e.id = f.id
		WHERE entries_fts MATCH ?
		ORDER BY score DESC
		LIMIT ?`, ftsQuery, limit)
	if err != nil {
		// If FTS query fails (bad syntax), fall back to LIKE search
		return s.searchFallback(query, limit)
	}
	defer rows.Close()

	return s.scanSearchResults(rows)
}

// searchFallback uses LIKE when FTS5 is unavailable.
// Splits query into words and matches entries containing ANY word.
// Score is based on how many words match.
func (s *Store) searchFallback(query string, limit int) ([]SearchResult, error) {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return nil, nil
	}

	// Build WHERE clause: (content LIKE '%word1%' OR topic LIKE '%word1%' ...) for each word
	// Score = count of matching words
	var conditions []string
	var scoreExprs []string
	var args []interface{}
	for _, w := range words {
		like := "%" + w + "%"
		cond := "(LOWER(content) LIKE ? OR LOWER(topic) LIKE ? OR LOWER(query) LIKE ? OR LOWER(tags) LIKE ?)"
		conditions = append(conditions, cond)
		scoreExprs = append(scoreExprs, fmt.Sprintf("(CASE WHEN %s THEN 1 ELSE 0 END)", cond))
		args = append(args, like, like, like, like)
	}

	// At least one word must match
	whereClause := strings.Join(conditions, " OR ")

	// Build score expression (sum of matching words)
	scoreClause := strings.Join(scoreExprs, " + ")
	// Double the args for score expression
	allArgs := make([]interface{}, 0, len(args)*2+1)
	allArgs = append(allArgs, args...) // for WHERE
	allArgs = append(allArgs, args...) // for score
	allArgs = append(allArgs, limit)

	sql := fmt.Sprintf(`
		SELECT id, mission_id, topic, query, content, source, tags, use_count, created_at, updated_at,
		       (%s) AS score
		FROM entries
		WHERE %s
		ORDER BY score DESC, use_count DESC, updated_at DESC
		LIMIT ?`, scoreClause, whereClause)

	rows, err := s.db.Query(sql, allArgs...)
	if err != nil {
		return nil, fmt.Errorf("fallback search: %w", err)
	}
	defer rows.Close()
	return s.scanSearchResults(rows)
}

// ByTopic returns all entries for a given topic, ordered by recency.
func (s *Store) ByTopic(topic string) ([]Entry, error) {
	rows, err := s.db.Query(`
		SELECT id, mission_id, topic, query, content, source, tags, use_count, created_at, updated_at
		FROM entries WHERE topic = ? ORDER BY updated_at DESC`, topic)
	if err != nil {
		return nil, fmt.Errorf("query by topic: %w", err)
	}
	defer rows.Close()
	return s.scanEntries(rows)
}

// ByMission returns all research for a specific mission.
func (s *Store) ByMission(missionID string) ([]Entry, error) {
	rows, err := s.db.Query(`
		SELECT id, mission_id, topic, query, content, source, tags, use_count, created_at, updated_at
		FROM entries WHERE mission_id = ? ORDER BY updated_at DESC`, missionID)
	if err != nil {
		return nil, fmt.Errorf("query by mission: %w", err)
	}
	defer rows.Close()
	return s.scanEntries(rows)
}

// Topics returns all distinct topics with entry counts.
func (s *Store) Topics() (map[string]int, error) {
	rows, err := s.db.Query("SELECT topic, COUNT(*) FROM entries GROUP BY topic ORDER BY COUNT(*) DESC")
	if err != nil {
		return nil, fmt.Errorf("query topics: %w", err)
	}
	defer rows.Close()

	topics := make(map[string]int)
	for rows.Next() {
		var topic string
		var count int
		if err := rows.Scan(&topic, &count); err != nil {
			return nil, err
		}
		topics[topic] = count
	}
	return topics, rows.Err()
}

// Count returns the total number of entries.
func (s *Store) Count() (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM entries").Scan(&count)
	return count, err
}

// --- Deduplication ---

// HasResearch checks if research already exists for a query+topic combination.
// Useful to prevent duplicate research.
func (s *Store) HasResearch(topic, query string) (bool, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM entries WHERE topic=? AND query=?", topic, query).Scan(&count)
	return count > 0, err
}

// --- Maintenance ---

// Prune removes entries not used since the cutoff time.
// Returns the number of entries removed.
func (s *Store) Prune(olderThan time.Time) (int, error) {
	result, err := s.db.Exec("DELETE FROM entries WHERE updated_at < ? AND use_count < 2",
		olderThan.Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("prune: %w", err)
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

// --- Scan Helpers ---

func (s *Store) scanEntry(row *sql.Row) (*Entry, error) {
	var e Entry
	var tagsStr, createdStr, updatedStr string
	err := row.Scan(&e.ID, &e.MissionID, &e.Topic, &e.Query, &e.Content,
		&e.Source, &tagsStr, &e.UseCount, &createdStr, &updatedStr)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan entry: %w", err)
	}
	e.Tags = splitTags(tagsStr)
	e.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdStr)
	e.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedStr)
	return &e, nil
}

func (s *Store) scanEntries(rows *sql.Rows) ([]Entry, error) {
	var entries []Entry
	for rows.Next() {
		var e Entry
		var tagsStr, createdStr, updatedStr string
		if err := rows.Scan(&e.ID, &e.MissionID, &e.Topic, &e.Query, &e.Content,
			&e.Source, &tagsStr, &e.UseCount, &createdStr, &updatedStr); err != nil {
			return nil, fmt.Errorf("scan entry: %w", err)
		}
		e.Tags = splitTags(tagsStr)
		e.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdStr)
		e.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedStr)
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func (s *Store) scanSearchResults(rows *sql.Rows) ([]SearchResult, error) {
	var results []SearchResult
	for rows.Next() {
		var e Entry
		var score float64
		var tagsStr, createdStr, updatedStr string
		if err := rows.Scan(&e.ID, &e.MissionID, &e.Topic, &e.Query, &e.Content,
			&e.Source, &tagsStr, &e.UseCount, &createdStr, &updatedStr, &score); err != nil {
			return nil, fmt.Errorf("scan search result: %w", err)
		}
		e.Tags = splitTags(tagsStr)
		e.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdStr)
		e.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedStr)
		results = append(results, SearchResult{Entry: e, Score: score})
	}
	return results, rows.Err()
}

func splitTags(s string) []string {
	if s == "" {
		return []string{}
	}
	return strings.Split(s, ",")
}

func sanitizeFTSQuery(query string) string {
	// Escape double quotes
	query = strings.ReplaceAll(query, "\"", "\"\"")
	// Split into words and add prefix matching
	words := strings.Fields(query)
	var parts []string
	for _, w := range words {
		// Wrap each word in quotes for exact matching, add * for prefix
		parts = append(parts, "\""+w+"\"*")
	}
	return strings.Join(parts, " ")
}

// --- Vector-based Semantic Search ---

// buildVectorIndex creates an in-memory vector index from all existing entries.
// Uses bag-of-words embedding over vocabulary extracted from entry content.
func (s *Store) buildVectorIndex() {
	rows, err := s.db.Query(`SELECT id, content, topic, query FROM entries`)
	if err != nil {
		return
	}
	defer rows.Close()

	// Collect all content to build vocabulary
	type doc struct {
		id, content, topic, query string
	}
	var docs []doc
	vocabSet := make(map[string]bool)

	for rows.Next() {
		var d doc
		if err := rows.Scan(&d.id, &d.content, &d.topic, &d.query); err != nil {
			continue
		}
		docs = append(docs, d)
		// Extract vocabulary from all text fields
		for _, text := range []string{d.content, d.topic, d.query} {
			for _, w := range strings.Fields(strings.ToLower(text)) {
				w = strings.Trim(w, ".,;:!?\"'()[]{}#*-_/\\")
				if len(w) >= 3 {
					vocabSet[w] = true
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return
	}

	if len(docs) == 0 {
		return
	}

	// Cap vocabulary at 2000 dimensions
	vocab := make([]string, 0, len(vocabSet))
	for w := range vocabSet {
		vocab = append(vocab, w)
	}
	if len(vocab) > 2000 {
		vocab = vocab[:2000]
	}
	s.vocab = vocab

	embedFn := vecindex.BagOfWordsEmbed(vocab)
	idx := vecindex.New(vecindex.Config{
		Dimension: len(vocab),
		EmbedFunc: embedFn,
	})

	// Index each entry
	for _, d := range docs {
		text := d.topic + " " + d.query + " " + d.content
		idx.AddText(d.id, text, "")
	}

	s.vecIdx = idx
}

// indexEntry adds or updates an entry in the vector index.
// If the entry introduces new vocabulary terms, rebuilds the entire index
// to ensure all entries are embedded in the same vector space.
func (s *Store) indexEntry(id, topic, query, content string) {
	s.vecMu.Lock()
	defer s.vecMu.Unlock()

	// Check if new vocabulary terms exist
	hasNew := false
	vocabSet := make(map[string]bool, len(s.vocab))
	for _, w := range s.vocab {
		vocabSet[w] = true
	}
	for _, text := range []string{content, topic, query} {
		for _, w := range strings.Fields(strings.ToLower(text)) {
			w = strings.Trim(w, ".,;:!?\"'()[]{}#*-_/\\")
			if len(w) >= 3 && !vocabSet[w] {
				hasNew = true
				break
			}
		}
		if hasNew {
			break
		}
	}

	if s.vecIdx == nil || hasNew {
		// Rebuild with updated vocabulary so all entries share the same vector space
		s.buildVectorIndex()
		return
	}

	text := topic + " " + query + " " + content
	s.vecIdx.AddText(id, text, "")
}

// SemanticSearch performs vector-based similarity search across research entries.
// Returns results ranked by cosine similarity of bag-of-words embeddings.
// Falls back to FTS5/LIKE search when the vector index is empty.
func (s *Store) SemanticSearch(query string, limit int) ([]SearchResult, error) {
	if query == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}

	s.vecMu.RLock()
	vecAvailable := s.vecIdx != nil && s.vecIdx.Count() > 0
	s.vecMu.RUnlock()

	if !vecAvailable {
		return s.Search(query, limit)
	}

	s.vecMu.RLock()
	results, err := s.vecIdx.SearchText(query, limit)
	s.vecMu.RUnlock()
	if err != nil {
		return s.Search(query, limit)
	}

	// Convert vecindex results to research SearchResults
	var out []SearchResult
	for _, r := range results {
		if r.Score < 0.01 {
			continue // skip near-zero similarity
		}
		entry, err := s.getWithoutIncrement(r.Document.ID)
		if err != nil || entry == nil {
			continue
		}
		out = append(out, SearchResult{Entry: *entry, Score: r.Score})
	}

	return out, nil
}

// getWithoutIncrement retrieves an entry without incrementing use_count.
func (s *Store) getWithoutIncrement(id string) (*Entry, error) {
	row := s.db.QueryRow(`
		SELECT id, mission_id, topic, query, content, source, tags, use_count, created_at, updated_at
		FROM entries WHERE id = ?`, id)
	return s.scanEntry(row)
}
