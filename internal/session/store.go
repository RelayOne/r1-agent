package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ericmacdougall/stoke/internal/plan"
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
	mu   sync.Mutex
}

// New creates a session store.
func New(projectRoot string) *Store {
	root := filepath.Join(projectRoot, ".stoke")
	_ = os.MkdirAll(root, 0700)
	_ = os.MkdirAll(filepath.Join(root, "history"), 0700)
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
	s.mu.Lock()
	defer s.mu.Unlock()
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

// Pattern records a single failure-to-fix pattern learned across task attempts.
type Pattern struct {
	Issue       string `json:"issue"`
	Fix         string `json:"fix"`
	Occurrences int    `json:"occurrences"`
}

// Learning holds the accumulated cross-task failure patterns used for retry intelligence.
type Learning struct {
	Patterns []Pattern `json:"patterns"`
}

// SaveLearning persists the learned patterns to learning.json.
func (s *Store) SaveLearning(l *Learning) error {
	return s.writeJSON("learning.json", l)
}

// LoadLearning reads the learned patterns from learning.json, returning an empty Learning if none exists.
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

// NextAttemptNumber loads prior attempts and returns the next attempt number.
// This ensures idempotent, monotonic numbering even across crashes.
func NextAttemptNumber(store SessionStore, taskID string) int {
	prior, _ := store.LoadAttempts(taskID)
	return len(prior) + 1
}

// SaveAttempt persists an attempt record and triggers cross-task learning.
func (s *Store) SaveAttempt(a Attempt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
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

// safePath validates that relPath stays within the store root.
func (s *Store) safePath(relPath string) (string, error) {
	fullPath := filepath.Join(s.root, relPath)
	absRoot, _ := filepath.Abs(s.root)
	absFull, _ := filepath.Abs(fullPath)
	if !strings.HasPrefix(absFull, absRoot+string(filepath.Separator)) && absFull != absRoot {
		return "", fmt.Errorf("path traversal rejected: %q escapes session root", relPath)
	}
	return fullPath, nil
}

func (s *Store) writeJSON(relPath string, v interface{}) error {
	fullPath, err := s.safePath(relPath)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	tmp := fullPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, fullPath)
}

func (s *Store) readJSON(relPath string, v interface{}) error {
	fullPath, err := s.safePath(relPath)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}
