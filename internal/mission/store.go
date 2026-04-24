// Package mission provides persistent mission lifecycle management.
//
// A Mission is the top-level unit of work in Stoke. Unlike a "task" which is
// a single unit of execution, a Mission represents the user's complete intent:
// "make authentication work" not "edit auth.go". Missions persist across agent
// invocations, context window boundaries, and crashes.
//
// The mission store is SQLite-backed with WAL mode for concurrent read safety.
// All public methods are thread-safe via database-level locking.
//
// State machine:
//
//	Created → Researching → Planning → Executing → Validating → Converged → Completed
//	                                                    ↑            |
//	                                                    └────────────┘ (gaps found)
//	Any state → Failed (terminal)
//	Any state → Paused (resumable)
//
// Evidence-based completion: a mission is only Completed when:
//   - All acceptance criteria have passing evidence
//   - The convergence validator reports zero gaps
//   - Two-model consensus confirms completion
package mission

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Phase represents the mission lifecycle phase. Transitions are enforced.
type Phase string

const (
	PhaseCreated     Phase = "created"     // mission defined, not started
	PhaseResearching Phase = "researching" // gathering context and information
	PhasePlanning    Phase = "planning"    // breaking down into tasks
	PhaseExecuting   Phase = "executing"   // tasks being worked on
	PhaseValidating  Phase = "validating"  // checking completeness
	PhaseConverged   Phase = "converged"   // all gaps closed, awaiting consensus
	PhaseCompleted   Phase = "completed"   // terminal: provably done
	PhaseFailed      Phase = "failed"      // terminal: cannot proceed
	PhasePaused      Phase = "paused"      // suspended, resumable
)

// validTransitions defines the allowed state transitions.
// The convergence loop is Validating → Executing (gaps found) and
// Validating → Converged (gaps closed).
var validTransitions = map[Phase][]Phase{
	PhaseCreated:     {PhaseResearching, PhasePlanning, PhaseFailed, PhasePaused},
	PhaseResearching: {PhasePlanning, PhaseFailed, PhasePaused},
	PhasePlanning:    {PhaseExecuting, PhaseFailed, PhasePaused},
	PhaseExecuting:   {PhaseValidating, PhaseFailed, PhasePaused},
	PhaseValidating:  {PhaseConverged, PhaseExecuting, PhaseFailed, PhasePaused}, // loop back on gaps
	PhaseConverged:   {PhaseCompleted, PhaseExecuting, PhaseFailed, PhasePaused}, // consensus reject → re-execute
	PhasePaused:      {PhaseCreated, PhaseResearching, PhasePlanning, PhaseExecuting, PhaseValidating},
	// PhaseCompleted and PhaseFailed are terminal — no outgoing transitions
}

// IsValidTransition checks whether a phase transition is allowed.
func IsValidTransition(from, to Phase) bool {
	allowed, ok := validTransitions[from]
	if !ok {
		return false
	}
	for _, a := range allowed {
		if a == to {
			return true
		}
	}
	return false
}

// Mission is the complete representation of a user's intent.
type Mission struct {
	ID          string            `json:"id"`
	Title       string            `json:"title"`       // short human-readable name
	Intent      string            `json:"intent"`      // full user intent description
	Phase       Phase             `json:"phase"`       // current lifecycle phase
	Criteria    []Criterion       `json:"criteria"`    // acceptance criteria
	Tags        []string          `json:"tags"`        // for search/filtering
	Metadata    map[string]string `json:"metadata"`    // extensible key-value pairs
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	CompletedAt *time.Time        `json:"completed_at,omitempty"`
}

// Criterion is a single acceptance criterion with evidence tracking.
type Criterion struct {
	ID          string     `json:"id"`
	Description string     `json:"description"` // what must be true
	Satisfied   bool       `json:"satisfied"`   // has passing evidence
	Evidence    string     `json:"evidence"`    // proof it's met (test output, file path, etc.)
	VerifiedAt  *time.Time `json:"verified_at,omitempty"`
	VerifiedBy  string     `json:"verified_by,omitempty"` // which model/tool verified
}

// Gap is a structured deficiency found during convergence validation.
type Gap struct {
	ID          string   `json:"id"`
	MissionID   string   `json:"mission_id"`
	Category    string   `json:"category"`    // "test", "code", "docs", "security", "completeness"
	Severity    string   `json:"severity"`    // "blocking", "major", "minor", "info"
	Description string   `json:"description"` // what's missing or wrong
	File        string   `json:"file"`        // affected file, if applicable
	Line        int      `json:"line"`        // affected line, if applicable
	Suggestion  string   `json:"suggestion"`  // recommended fix
	Resolved    bool     `json:"resolved"`
	ResolvedAt  *time.Time `json:"resolved_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

// Transition records a state change for audit trail.
type Transition struct {
	ID        int64     `json:"id"`
	MissionID string    `json:"mission_id"`
	FromPhase Phase     `json:"from_phase"`
	ToPhase   Phase     `json:"to_phase"`
	Reason    string    `json:"reason"`
	Agent     string    `json:"agent"`     // which agent/model triggered
	Timestamp time.Time `json:"timestamp"`
}

// HandoffRecord captures context transfer between agent invocations.
type HandoffRecord struct {
	ID           int64     `json:"id"`
	MissionID    string    `json:"mission_id"`
	FromAgent    string    `json:"from_agent"`    // agent handing off
	ToAgent      string    `json:"to_agent"`      // agent receiving
	Summary      string    `json:"summary"`       // compacted context
	PendingWork  string    `json:"pending_work"`  // what remains to be done
	KeyDecisions string    `json:"key_decisions"` // important choices made
	Timestamp    time.Time `json:"timestamp"`
}

// ConsensusRecord captures a model's judgment on mission completion.
type ConsensusRecord struct {
	ID        int64     `json:"id"`
	MissionID string    `json:"mission_id"`
	Model     string    `json:"model"`   // which model judged
	Verdict   string    `json:"verdict"` // "complete", "incomplete", "reject"
	Reasoning string    `json:"reasoning"`
	GapsFound []string  `json:"gaps_found"` // IDs of gaps identified
	Timestamp time.Time `json:"timestamp"`
}

// Store is the SQLite-backed mission persistence layer.
// All methods are safe for concurrent use via SQLite WAL mode
// and busy timeout configuration.
type Store struct {
	db     *sql.DB
	dbPath string
}

// NewStore opens or creates a mission database at the given directory.
// The database uses WAL journal mode for concurrent read access and
// a 5-second busy timeout to handle write contention.
func NewStore(dir string) (*Store, error) {
	if dir == "" {
		return nil, fmt.Errorf("mission store directory must not be empty")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create mission store directory %q: %w", dir, err)
	}

	dbPath := filepath.Join(dir, "missions.db")
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=ON")
	if err != nil {
		return nil, fmt.Errorf("open missions.db: %w", err)
	}

	// Verify connection
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping missions.db: %w", err)
	}

	s := &Store{db: db, dbPath: dbPath}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate missions.db: %w", err)
	}

	log.Printf("[mission] store opened at %s", dbPath)
	return s, nil
}

// Close closes the database connection. Must be called on shutdown.
func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

// migrate creates or updates the schema. Each migration is idempotent.
func (s *Store) migrate() error {
	migrations := []string{
		`CREATE TABLE IF NOT EXISTS missions (
			id          TEXT PRIMARY KEY NOT NULL,
			title       TEXT NOT NULL,
			intent      TEXT NOT NULL,
			phase       TEXT NOT NULL DEFAULT 'created',
			criteria    TEXT NOT NULL DEFAULT '[]',
			tags        TEXT NOT NULL DEFAULT '[]',
			metadata    TEXT NOT NULL DEFAULT '{}',
			created_at  TEXT NOT NULL,
			updated_at  TEXT NOT NULL,
			completed_at TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS gaps (
			id          TEXT NOT NULL,
			mission_id  TEXT NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
			category    TEXT NOT NULL,
			severity    TEXT NOT NULL,
			description TEXT NOT NULL,
			file        TEXT,
			line        INTEGER DEFAULT 0,
			suggestion  TEXT,
			resolved    INTEGER NOT NULL DEFAULT 0,
			resolved_at TEXT,
			created_at  TEXT NOT NULL,
			PRIMARY KEY (id, mission_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_gaps_mission ON gaps(mission_id)`,
		`CREATE INDEX IF NOT EXISTS idx_gaps_unresolved ON gaps(mission_id, resolved) WHERE resolved = 0`,
		`CREATE TABLE IF NOT EXISTS transitions (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			mission_id TEXT NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
			from_phase TEXT NOT NULL,
			to_phase   TEXT NOT NULL,
			reason     TEXT,
			agent      TEXT,
			timestamp  TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_transitions_mission ON transitions(mission_id)`,
		`CREATE TABLE IF NOT EXISTS handoffs (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			mission_id    TEXT NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
			from_agent    TEXT,
			to_agent      TEXT,
			summary       TEXT NOT NULL,
			pending_work  TEXT,
			key_decisions TEXT,
			timestamp     TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_handoffs_mission ON handoffs(mission_id)`,
		`CREATE TABLE IF NOT EXISTS consensus (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			mission_id TEXT NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
			model      TEXT NOT NULL,
			verdict    TEXT NOT NULL,
			reasoning  TEXT,
			gaps_found TEXT NOT NULL DEFAULT '[]',
			timestamp  TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_consensus_mission ON consensus(mission_id)`,
	}

	for _, m := range migrations {
		if _, err := s.db.Exec(m); err != nil {
			return fmt.Errorf("migration failed: %w\nSQL: %s", err, m)
		}
	}
	return nil
}

// --- Mission CRUD ---

// Create persists a new mission in Created phase.
// Returns an error if a mission with the same ID already exists.
func (s *Store) Create(m *Mission) error {
	if m.ID == "" {
		return fmt.Errorf("mission ID must not be empty")
	}
	if m.Title == "" {
		return fmt.Errorf("mission title must not be empty")
	}
	if m.Intent == "" {
		return fmt.Errorf("mission intent must not be empty")
	}

	now := time.Now().UTC()
	m.Phase = PhaseCreated
	m.CreatedAt = now
	m.UpdatedAt = now
	if m.Criteria == nil {
		m.Criteria = []Criterion{}
	}
	if m.Tags == nil {
		m.Tags = []string{}
	}
	if m.Metadata == nil {
		m.Metadata = map[string]string{}
	}

	criteriaJSON, err := json.Marshal(m.Criteria)
	if err != nil {
		return fmt.Errorf("marshal criteria: %w", err)
	}
	tagsJSON, err := json.Marshal(m.Tags)
	if err != nil {
		return fmt.Errorf("marshal tags: %w", err)
	}
	metaJSON, err := json.Marshal(m.Metadata)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}

	_, err = s.db.Exec(`
		INSERT INTO missions (id, title, intent, phase, criteria, tags, metadata, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.Title, m.Intent, string(m.Phase),
		string(criteriaJSON), string(tagsJSON), string(metaJSON),
		m.CreatedAt.Format(time.RFC3339Nano), m.UpdatedAt.Format(time.RFC3339Nano))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			return fmt.Errorf("mission %q already exists", m.ID)
		}
		return fmt.Errorf("insert mission: %w", err)
	}
	return nil
}

// Get retrieves a mission by ID. Returns nil, nil if not found.
func (s *Store) Get(id string) (*Mission, error) {
	if id == "" {
		return nil, fmt.Errorf("mission ID must not be empty")
	}

	row := s.db.QueryRow(`
		SELECT id, title, intent, phase, criteria, tags, metadata, created_at, updated_at, completed_at
		FROM missions WHERE id = ?`, id)

	return s.scanMission(row)
}

// List returns all missions, optionally filtered by phase.
// Pass empty phase to list all missions.
func (s *Store) List(phase Phase) ([]*Mission, error) {
	var rows *sql.Rows
	var err error
	if phase == "" {
		rows, err = s.db.Query(`
			SELECT id, title, intent, phase, criteria, tags, metadata, created_at, updated_at, completed_at
			FROM missions ORDER BY updated_at DESC`)
	} else {
		rows, err = s.db.Query(`
			SELECT id, title, intent, phase, criteria, tags, metadata, created_at, updated_at, completed_at
			FROM missions WHERE phase = ? ORDER BY updated_at DESC`, string(phase))
	}
	if err != nil {
		return nil, fmt.Errorf("query missions: %w", err)
	}
	defer rows.Close()

	var missions []*Mission
	for rows.Next() {
		m, err := s.scanMissionRow(rows)
		if err != nil {
			return nil, err
		}
		missions = append(missions, m)
	}
	return missions, rows.Err()
}

// Update persists changes to a mission's mutable fields (title, intent, criteria, tags, metadata).
// Phase changes must go through Advance() to enforce the state machine.
func (s *Store) Update(m *Mission) error {
	if m.ID == "" {
		return fmt.Errorf("mission ID must not be empty")
	}

	m.UpdatedAt = time.Now().UTC()
	criteriaJSON, err := json.Marshal(m.Criteria)
	if err != nil {
		return fmt.Errorf("marshal criteria: %w", err)
	}
	tagsJSON, err := json.Marshal(m.Tags)
	if err != nil {
		return fmt.Errorf("marshal tags: %w", err)
	}
	metaJSON, err := json.Marshal(m.Metadata)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}

	result, err := s.db.Exec(`
		UPDATE missions SET title=?, intent=?, criteria=?, tags=?, metadata=?, updated_at=?
		WHERE id=?`,
		m.Title, m.Intent, string(criteriaJSON), string(tagsJSON), string(metaJSON),
		m.UpdatedAt.Format(time.RFC3339Nano), m.ID)
	if err != nil {
		return fmt.Errorf("update mission: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("mission %q not found", m.ID)
	}
	return nil
}

// Delete removes a mission and all associated data (gaps, transitions, handoffs, consensus).
// Foreign key cascades handle child table cleanup.
func (s *Store) Delete(id string) error {
	if id == "" {
		return fmt.Errorf("mission ID must not be empty")
	}
	result, err := s.db.Exec("DELETE FROM missions WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete mission: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("mission %q not found", id)
	}
	return nil
}

// --- State Machine ---

// Advance transitions a mission to a new phase, enforcing the state machine.
// Records a Transition for the audit trail. Returns an error if the
// transition is not valid per validTransitions.
func (s *Store) Advance(missionID string, to Phase, reason, agent string) error {
	if missionID == "" {
		return fmt.Errorf("mission ID must not be empty")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Read current phase under transaction
	var currentPhase string
	err = tx.QueryRow("SELECT phase FROM missions WHERE id = ?", missionID).Scan(&currentPhase)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("mission %q not found", missionID)
	}
	if err != nil {
		return fmt.Errorf("read mission phase: %w", err)
	}

	from := Phase(currentPhase)
	if !IsValidTransition(from, to) {
		return fmt.Errorf("invalid transition: %s → %s (mission %q)", from, to, missionID)
	}

	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339Nano)

	// Update phase
	updateSQL := "UPDATE missions SET phase=?, updated_at=? WHERE id=?"
	args := []interface{}{string(to), nowStr, missionID}

	// Set completed_at for terminal success
	if to == PhaseCompleted {
		updateSQL = "UPDATE missions SET phase=?, updated_at=?, completed_at=? WHERE id=?"
		args = []interface{}{string(to), nowStr, nowStr, missionID}
	}

	if _, err := tx.Exec(updateSQL, args...); err != nil {
		return fmt.Errorf("update phase: %w", err)
	}

	// Record transition
	if _, err := tx.Exec(`
		INSERT INTO transitions (mission_id, from_phase, to_phase, reason, agent, timestamp)
		VALUES (?, ?, ?, ?, ?, ?)`,
		missionID, string(from), string(to), reason, agent, nowStr); err != nil {
		return fmt.Errorf("record transition: %w", err)
	}

	return tx.Commit()
}

// --- Acceptance Criteria ---

// SetCriteriaSatisfied marks a criterion as satisfied with evidence.
// Returns an error if the criterion is not found.
func (s *Store) SetCriteriaSatisfied(missionID, criterionID, evidence, verifiedBy string) error {
	m, err := s.Get(missionID)
	if err != nil {
		return err
	}
	if m == nil {
		return fmt.Errorf("mission %q not found", missionID)
	}

	found := false
	now := time.Now().UTC()
	for i := range m.Criteria {
		if m.Criteria[i].ID == criterionID {
			m.Criteria[i].Satisfied = true
			m.Criteria[i].Evidence = evidence
			m.Criteria[i].VerifiedAt = &now
			m.Criteria[i].VerifiedBy = verifiedBy
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("criterion %q not found in mission %q", criterionID, missionID)
	}

	return s.Update(m)
}

// UnsatisfiedCriteria returns criteria that lack passing evidence.
func (s *Store) UnsatisfiedCriteria(missionID string) ([]Criterion, error) {
	m, err := s.Get(missionID)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, fmt.Errorf("mission %q not found", missionID)
	}

	var unsatisfied []Criterion
	for _, c := range m.Criteria {
		if !c.Satisfied {
			unsatisfied = append(unsatisfied, c)
		}
	}
	return unsatisfied, nil
}

// AllCriteriaMet returns true if every criterion has passing evidence.
func (s *Store) AllCriteriaMet(missionID string) (bool, error) {
	unsatisfied, err := s.UnsatisfiedCriteria(missionID)
	if err != nil {
		return false, err
	}
	return len(unsatisfied) == 0, nil
}

// --- Gaps ---

// AddGap records a deficiency found during convergence validation.
func (s *Store) AddGap(g *Gap) error {
	if g.ID == "" || g.MissionID == "" {
		return fmt.Errorf("gap ID and mission ID must not be empty")
	}
	if g.Category == "" || g.Severity == "" || g.Description == "" {
		return fmt.Errorf("gap category, severity, and description must not be empty")
	}

	g.CreatedAt = time.Now().UTC()
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO gaps (id, mission_id, category, severity, description, file, line, suggestion, resolved, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, ?)`,
		g.ID, g.MissionID, g.Category, g.Severity, g.Description,
		g.File, g.Line, g.Suggestion, g.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("insert gap: %w", err)
	}
	return nil
}

// ResolveGap marks a gap as resolved.
func (s *Store) ResolveGap(missionID, gapID string) error {
	now := time.Now().UTC()
	result, err := s.db.Exec(`
		UPDATE gaps SET resolved=1, resolved_at=? WHERE id=? AND mission_id=?`,
		now.Format(time.RFC3339Nano), gapID, missionID)
	if err != nil {
		return fmt.Errorf("resolve gap: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("gap %q not found in mission %q", gapID, missionID)
	}
	return nil
}

// OpenGaps returns all unresolved gaps for a mission.
func (s *Store) OpenGaps(missionID string) ([]Gap, error) {
	rows, err := s.db.Query(`
		SELECT id, mission_id, category, severity, description, file, line, suggestion, resolved, resolved_at, created_at
		FROM gaps WHERE mission_id=? AND resolved=0 ORDER BY
			CASE severity WHEN 'blocking' THEN 0 WHEN 'major' THEN 1 WHEN 'minor' THEN 2 ELSE 3 END`,
		missionID)
	if err != nil {
		return nil, fmt.Errorf("query gaps: %w", err)
	}
	defer rows.Close()
	return s.scanGaps(rows)
}

// AllGaps returns all gaps (resolved and unresolved) for a mission.
func (s *Store) AllGaps(missionID string) ([]Gap, error) {
	rows, err := s.db.Query(`
		SELECT id, mission_id, category, severity, description, file, line, suggestion, resolved, resolved_at, created_at
		FROM gaps WHERE mission_id=? ORDER BY created_at DESC`, missionID)
	if err != nil {
		return nil, fmt.Errorf("query all gaps: %w", err)
	}
	defer rows.Close()
	return s.scanGaps(rows)
}

// HasBlockingGaps returns true if the mission has unresolved blocking gaps.
func (s *Store) HasBlockingGaps(missionID string) (bool, error) {
	var count int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM gaps WHERE mission_id=? AND resolved=0 AND severity='blocking'`,
		missionID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("count blocking gaps: %w", err)
	}
	return count > 0, nil
}

// --- Handoffs ---

// RecordHandoff saves a context transfer between agent invocations.
func (s *Store) RecordHandoff(h *HandoffRecord) error {
	if h.MissionID == "" || h.Summary == "" {
		return fmt.Errorf("handoff mission ID and summary must not be empty")
	}
	h.Timestamp = time.Now().UTC()
	_, err := s.db.Exec(`
		INSERT INTO handoffs (mission_id, from_agent, to_agent, summary, pending_work, key_decisions, timestamp)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		h.MissionID, h.FromAgent, h.ToAgent, h.Summary,
		h.PendingWork, h.KeyDecisions, h.Timestamp.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("insert handoff: %w", err)
	}
	return nil
}

// LatestHandoff returns the most recent handoff for a mission, or nil if none.
func (s *Store) LatestHandoff(missionID string) (*HandoffRecord, error) {
	row := s.db.QueryRow(`
		SELECT id, mission_id, from_agent, to_agent, summary, pending_work, key_decisions, timestamp
		FROM handoffs WHERE mission_id=? ORDER BY timestamp DESC LIMIT 1`, missionID)

	var h HandoffRecord
	var ts string
	err := row.Scan(&h.ID, &h.MissionID, &h.FromAgent, &h.ToAgent,
		&h.Summary, &h.PendingWork, &h.KeyDecisions, &ts)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan handoff: %w", err)
	}
	h.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
	return &h, nil
}

// Handoffs returns all handoff records for a mission, ordered chronologically.
func (s *Store) Handoffs(missionID string) ([]HandoffRecord, error) {
	rows, err := s.db.Query(`
		SELECT id, mission_id, from_agent, to_agent, summary, pending_work, key_decisions, timestamp
		FROM handoffs WHERE mission_id=? ORDER BY timestamp ASC`, missionID)
	if err != nil {
		return nil, fmt.Errorf("query handoffs: %w", err)
	}
	defer rows.Close()

	var handoffs []HandoffRecord
	for rows.Next() {
		var h HandoffRecord
		var ts string
		if err := rows.Scan(&h.ID, &h.MissionID, &h.FromAgent, &h.ToAgent,
			&h.Summary, &h.PendingWork, &h.KeyDecisions, &ts); err != nil {
			return nil, fmt.Errorf("scan handoff: %w", err)
		}
		h.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
		handoffs = append(handoffs, h)
	}
	return handoffs, rows.Err()
}

// --- Consensus ---

// RecordConsensus saves a model's completion judgment.
func (s *Store) RecordConsensus(c *ConsensusRecord) error {
	if c.MissionID == "" || c.Model == "" || c.Verdict == "" {
		return fmt.Errorf("consensus mission ID, model, and verdict must not be empty")
	}
	c.Timestamp = time.Now().UTC()
	gapsJSON, err := json.Marshal(c.GapsFound)
	if err != nil {
		return fmt.Errorf("marshal gaps: %w", err)
	}
	_, err = s.db.Exec(`
		INSERT INTO consensus (mission_id, model, verdict, reasoning, gaps_found, timestamp)
		VALUES (?, ?, ?, ?, ?, ?)`,
		c.MissionID, c.Model, c.Verdict, c.Reasoning,
		string(gapsJSON), c.Timestamp.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("insert consensus: %w", err)
	}
	return nil
}

// HasConsensus returns true if at least N distinct models have voted "complete"
// for the mission with no "incomplete" or "reject" votes after the most recent
// "complete" votes.
func (s *Store) HasConsensus(missionID string, requiredModels int) (bool, error) {
	rows, err := s.db.Query(`
		SELECT model, verdict FROM consensus
		WHERE mission_id=? ORDER BY timestamp DESC`, missionID)
	if err != nil {
		return false, fmt.Errorf("query consensus: %w", err)
	}
	defer rows.Close()

	// Track the most recent verdict per model
	latest := make(map[string]string)
	for rows.Next() {
		var model, verdict string
		if err := rows.Scan(&model, &verdict); err != nil {
			return false, fmt.Errorf("scan consensus: %w", err)
		}
		if _, seen := latest[model]; !seen {
			latest[model] = verdict
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}

	completeCount := 0
	for _, verdict := range latest {
		if verdict == "complete" {
			completeCount++
		}
	}
	return completeCount >= requiredModels, nil
}

// ConsensusRecords returns all consensus records for a mission.
func (s *Store) ConsensusRecords(missionID string) ([]ConsensusRecord, error) {
	rows, err := s.db.Query(`
		SELECT id, mission_id, model, verdict, reasoning, gaps_found, timestamp
		FROM consensus WHERE mission_id=? ORDER BY timestamp DESC`, missionID)
	if err != nil {
		return nil, fmt.Errorf("query consensus: %w", err)
	}
	defer rows.Close()

	var records []ConsensusRecord
	for rows.Next() {
		var c ConsensusRecord
		var gapsJSON, ts string
		if err := rows.Scan(&c.ID, &c.MissionID, &c.Model, &c.Verdict,
			&c.Reasoning, &gapsJSON, &ts); err != nil {
			return nil, fmt.Errorf("scan consensus: %w", err)
		}
		json.Unmarshal([]byte(gapsJSON), &c.GapsFound)
		c.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
		records = append(records, c)
	}
	return records, rows.Err()
}

// --- Audit Trail ---

// Transitions returns the full audit trail for a mission.
func (s *Store) Transitions(missionID string) ([]Transition, error) {
	rows, err := s.db.Query(`
		SELECT id, mission_id, from_phase, to_phase, reason, agent, timestamp
		FROM transitions WHERE mission_id=? ORDER BY timestamp ASC`, missionID)
	if err != nil {
		return nil, fmt.Errorf("query transitions: %w", err)
	}
	defer rows.Close()

	var transitions []Transition
	for rows.Next() {
		var t Transition
		var ts string
		if err := rows.Scan(&t.ID, &t.MissionID, &t.FromPhase, &t.ToPhase,
			&t.Reason, &t.Agent, &ts); err != nil {
			return nil, fmt.Errorf("scan transition: %w", err)
		}
		t.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
		transitions = append(transitions, t)
	}
	return transitions, rows.Err()
}

// --- Convergence Query ---

// ConvergenceStatus returns a summary of how close a mission is to completion.
type ConvergenceStatus struct {
	MissionID         string `json:"mission_id"`
	Phase             Phase  `json:"phase"`
	TotalCriteria     int    `json:"total_criteria"`
	SatisfiedCriteria int    `json:"satisfied_criteria"`
	OpenGapCount      int    `json:"open_gap_count"`
	BlockingGapCount  int    `json:"blocking_gap_count"`
	HandoffCount      int    `json:"handoff_count"`
	ConsensusCount    int    `json:"consensus_count"`    // total consensus records
	CompleteVotes     int    `json:"complete_votes"`      // models voting "complete"
	IsConverged       bool   `json:"is_converged"`        // all criteria met, no blocking gaps
	HasConsensus      bool   `json:"has_consensus"`       // 2+ models agree complete
}

// GetConvergenceStatus computes the full convergence status for a mission.
func (s *Store) GetConvergenceStatus(missionID string, requiredConsensus int) (*ConvergenceStatus, error) {
	m, err := s.Get(missionID)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, fmt.Errorf("mission %q not found", missionID)
	}

	status := &ConvergenceStatus{
		MissionID: missionID,
		Phase:     m.Phase,
	}

	// Criteria
	status.TotalCriteria = len(m.Criteria)
	for _, c := range m.Criteria {
		if c.Satisfied {
			status.SatisfiedCriteria++
		}
	}

	// Gaps
	s.db.QueryRow("SELECT COUNT(*) FROM gaps WHERE mission_id=? AND resolved=0", missionID).Scan(&status.OpenGapCount)
	s.db.QueryRow("SELECT COUNT(*) FROM gaps WHERE mission_id=? AND resolved=0 AND severity='blocking'", missionID).Scan(&status.BlockingGapCount)

	// Handoffs
	s.db.QueryRow("SELECT COUNT(*) FROM handoffs WHERE mission_id=?", missionID).Scan(&status.HandoffCount)

	// Consensus
	s.db.QueryRow("SELECT COUNT(*) FROM consensus WHERE mission_id=?", missionID).Scan(&status.ConsensusCount)

	// Count distinct models with latest verdict = "complete"
	rows, err := s.db.Query("SELECT model, verdict FROM consensus WHERE mission_id=? ORDER BY timestamp DESC", missionID)
	if err == nil {
		defer rows.Close()
		latest := make(map[string]string)
		for rows.Next() {
			var model, verdict string
			rows.Scan(&model, &verdict)
			if _, seen := latest[model]; !seen {
				latest[model] = verdict
			}
		}
		_ = rows.Err() // best-effort mission status; iterate-error is not actionable
		for _, v := range latest {
			if v == "complete" {
				status.CompleteVotes++
			}
		}
	}

	// Converged = all criteria met AND no blocking gaps
	status.IsConverged = status.TotalCriteria > 0 &&
		status.SatisfiedCriteria == status.TotalCriteria &&
		status.BlockingGapCount == 0
	status.HasConsensus = status.CompleteVotes >= requiredConsensus

	return status, nil
}

// --- Scan Helpers ---

// scanMission scans a single row into a Mission.
func (s *Store) scanMission(row *sql.Row) (*Mission, error) {
	var m Mission
	var phase, criteriaJSON, tagsJSON, metaJSON, createdStr, updatedStr string
	var completedStr sql.NullString

	err := row.Scan(&m.ID, &m.Title, &m.Intent, &phase,
		&criteriaJSON, &tagsJSON, &metaJSON,
		&createdStr, &updatedStr, &completedStr)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan mission: %w", err)
	}

	m.Phase = Phase(phase)
	json.Unmarshal([]byte(criteriaJSON), &m.Criteria)
	json.Unmarshal([]byte(tagsJSON), &m.Tags)
	json.Unmarshal([]byte(metaJSON), &m.Metadata)
	m.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdStr)
	m.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedStr)
	if completedStr.Valid {
		t, _ := time.Parse(time.RFC3339Nano, completedStr.String)
		m.CompletedAt = &t
	}

	if m.Criteria == nil {
		m.Criteria = []Criterion{}
	}
	if m.Tags == nil {
		m.Tags = []string{}
	}
	if m.Metadata == nil {
		m.Metadata = map[string]string{}
	}

	return &m, nil
}

// scanMissionRow scans from sql.Rows (for List queries).
func (s *Store) scanMissionRow(rows *sql.Rows) (*Mission, error) {
	var m Mission
	var phase, criteriaJSON, tagsJSON, metaJSON, createdStr, updatedStr string
	var completedStr sql.NullString

	err := rows.Scan(&m.ID, &m.Title, &m.Intent, &phase,
		&criteriaJSON, &tagsJSON, &metaJSON,
		&createdStr, &updatedStr, &completedStr)
	if err != nil {
		return nil, fmt.Errorf("scan mission row: %w", err)
	}

	m.Phase = Phase(phase)
	json.Unmarshal([]byte(criteriaJSON), &m.Criteria)
	json.Unmarshal([]byte(tagsJSON), &m.Tags)
	json.Unmarshal([]byte(metaJSON), &m.Metadata)
	m.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdStr)
	m.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedStr)
	if completedStr.Valid {
		t, _ := time.Parse(time.RFC3339Nano, completedStr.String)
		m.CompletedAt = &t
	}

	if m.Criteria == nil {
		m.Criteria = []Criterion{}
	}
	if m.Tags == nil {
		m.Tags = []string{}
	}
	if m.Metadata == nil {
		m.Metadata = map[string]string{}
	}

	return &m, nil
}

// scanGaps scans rows into Gap slices.
func (s *Store) scanGaps(rows *sql.Rows) ([]Gap, error) {
	var gaps []Gap
	for rows.Next() {
		var g Gap
		var resolved int
		var resolvedStr sql.NullString
		var createdStr string

		err := rows.Scan(&g.ID, &g.MissionID, &g.Category, &g.Severity,
			&g.Description, &g.File, &g.Line, &g.Suggestion,
			&resolved, &resolvedStr, &createdStr)
		if err != nil {
			return nil, fmt.Errorf("scan gap: %w", err)
		}

		g.Resolved = resolved != 0
		g.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdStr)
		if resolvedStr.Valid {
			t, _ := time.Parse(time.RFC3339Nano, resolvedStr.String)
			g.ResolvedAt = &t
		}
		gaps = append(gaps, g)
	}
	return gaps, rows.Err()
}
