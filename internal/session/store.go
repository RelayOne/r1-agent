package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"stoke/internal/plan"
)

// SessionStore is the interface for session persistence.
// Both Store (JSON files) and SQLStore (SQLite) implement this.
type SessionStore interface {
	SaveState(state *State) error
	LoadState() (*State, error)
	ClearState() error
	SaveAttempt(a Attempt) error
	LoadAttempts(taskID string) ([]Attempt, error)
	SaveLearning(l *Learning) error
	LoadLearning() (*Learning, error)
}

// Store persists session state as JSON files under .stoke/.
type Store struct {
	root string
}

// New creates a session store.
func New(projectRoot string) *Store {
	root := filepath.Join(projectRoot, ".stoke")
	os.MkdirAll(root, 0755)
	os.MkdirAll(filepath.Join(root, "history"), 0755)
	return &Store{root: root}
}

// State is recoverable session data. Written after every task completion.
type State struct {
	PlanID       string      `json:"plan_id"`
	Tasks        []plan.Task `json:"tasks"`
	TotalCostUSD float64     `json:"total_cost_usd"`
	StartedAt    time.Time   `json:"started_at"`
	SavedAt      time.Time   `json:"saved_at"`
}

// SaveState writes session state for crash recovery.
func (s *Store) SaveState(state *State) error {
	state.SavedAt = time.Now()
	return s.writeJSON("session.json", state)
}

// LoadState reads the last saved session. Returns nil if none exists.
func (s *Store) LoadState() (*State, error) {
	var state State
	if err := s.readJSON("session.json", &state); err != nil {
		if os.IsNotExist(err) { return nil, nil }
		return nil, err
	}
	return &state, nil
}

// ClearState removes session state (called when build completes).
func (s *Store) ClearState() error {
	return os.Remove(filepath.Join(s.root, "session.json"))
}

// --- Learned patterns ---

type Pattern struct {
	Issue       string `json:"issue"`
	Fix         string `json:"fix"`
	Occurrences int    `json:"occurrences"`
}

type Learning struct {
	Patterns []Pattern `json:"patterns"`
}

func (s *Store) SaveLearning(l *Learning) error {
	return s.writeJSON("learning.json", l)
}

func (s *Store) LoadLearning() (*Learning, error) {
	var l Learning
	if err := s.readJSON("learning.json", &l); err != nil {
		if os.IsNotExist(err) { return &Learning{}, nil }
		return nil, err
	}
	return &l, nil
}

// --- Attempt history ---

// Attempt captures everything about one execution attempt for retry intelligence.
type Attempt struct {
	TaskID      string        `json:"task_id"`
	Number      int           `json:"number"`
	Success     bool          `json:"success"`
	CostUSD     float64       `json:"cost_usd"`
	Duration    time.Duration `json:"duration"`
	Error       string        `json:"error,omitempty"`
	FailClass   string        `json:"fail_class,omitempty"`
	FailSummary string        `json:"fail_summary,omitempty"`
	RootCause   string        `json:"root_cause,omitempty"`
	DiffSummary string        `json:"diff_summary,omitempty"`
	LearnedFix  string        `json:"learned_fix,omitempty"`
}

func (s *Store) SaveAttempt(a Attempt) error {
	path := filepath.Join("history", a.TaskID+".json")
	var attempts []Attempt
	s.readJSON(path, &attempts)
	attempts = append(attempts, a)

	// Extract learned pattern if the task succeeded after previous failures
	if a.Success && len(attempts) > 1 {
		prev := attempts[len(attempts)-2]
		if !prev.Success && prev.FailSummary != "" {
			s.addLearnedPattern(prev.FailSummary, "resolved on retry "+fmt.Sprintf("%d", a.Number))
		}
	}

	return s.writeJSON(path, &attempts)
}

// LoadAttempts reads the attempt history for a task.
func (s *Store) LoadAttempts(taskID string) ([]Attempt, error) {
	path := filepath.Join("history", taskID+".json")
	var attempts []Attempt
	if err := s.readJSON(path, &attempts); err != nil {
		if os.IsNotExist(err) { return nil, nil }
		return nil, err
	}
	return attempts, nil
}

// addLearnedPattern records a failure->fix pattern for cross-task learning.
func (s *Store) addLearnedPattern(issue, fix string) {
	learning, _ := s.LoadLearning()
	if learning == nil {
		learning = &Learning{}
	}
	// Deduplicate
	for i, p := range learning.Patterns {
		if p.Issue == issue {
			learning.Patterns[i].Occurrences++
			if fix != "" { learning.Patterns[i].Fix = fix }
			s.SaveLearning(learning)
			return
		}
	}
	learning.Patterns = append(learning.Patterns, Pattern{Issue: issue, Fix: fix, Occurrences: 1})
	s.SaveLearning(learning)
}

// --- JSON helpers ---

func (s *Store) writeJSON(relPath string, v interface{}) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil { return fmt.Errorf("marshal: %w", err) }
	fullPath := filepath.Join(s.root, relPath)
	os.MkdirAll(filepath.Dir(fullPath), 0755)
	tmp := fullPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil { return err }
	return os.Rename(tmp, fullPath)
}

func (s *Store) readJSON(relPath string, v interface{}) error {
	data, err := os.ReadFile(filepath.Join(s.root, relPath))
	if err != nil { return err }
	return json.Unmarshal(data, v)
}
