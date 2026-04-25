package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/RelayOne/r1-agent/internal/plan"
	"github.com/RelayOne/r1-agent/internal/r1dir"
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

// Store persists session state as JSON files under the per-project data
// dir. Reads prefer the canonical `.r1/` layout and fall back to the
// legacy `.stoke/` layout when only the latter exists (per
// work-r1-rename.md §S1-5 dual-resolve rule). Writes tee into BOTH
// layouts so external consumers that still read `.stoke/` see the
// latest state and rollback to pre-rename builds only requires deleting
// `.r1/`.
//
// `root` is the primary (canonical-preferred) location used for tmp+rename
// atomic writes and subsequent reads. `legacyRoot` is the dual-write tee
// target. When the caller is already on the legacy layout (because only
// `.stoke/` exists), root and legacyRoot collapse to the same path and
// the tee is a no-op.
type Store struct {
	root       string
	legacyRoot string
	mu         sync.Mutex
}

// New creates a session store. The resolved primary root is `.r1/` when
// that directory already exists under projectRoot, otherwise `.stoke/`
// for legacy sessions. For brand-new projects (neither exists) we seed
// the canonical `.r1/` layout so post-rename sessions start on the
// canonical side, while still dual-writing into `.stoke/` for rollback
// safety.
func New(projectRoot string) *Store {
	canonical := filepath.Join(projectRoot, r1dir.Canonical)
	legacy := filepath.Join(projectRoot, r1dir.Legacy)

	canonicalExists := dirExists(canonical)
	legacyExists := dirExists(legacy)

	// Pick the primary root. Canonical wins when present; legacy wins
	// when it is the only one that exists; canonical is the default for
	// brand-new projects so new sessions land on the post-rename layout.
	var root string
	switch {
	case canonicalExists:
		root = canonical
	case legacyExists:
		root = legacy
	default:
		root = canonical
	}

	_ = os.MkdirAll(root, 0700)
	_ = os.MkdirAll(filepath.Join(root, "history"), 0700)

	// Seed the legacy tee target so dual-writes succeed without racing
	// the first MkdirAll inside writeJSON on a fresh project.
	legacyMirror := legacy
	if legacyMirror != root {
		_ = os.MkdirAll(legacyMirror, 0700)
		_ = os.MkdirAll(filepath.Join(legacyMirror, "history"), 0700)
	}

	return &Store{root: root, legacyRoot: legacyMirror}
}

// dirExists reports whether path is an existing directory.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
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
// Removes from BOTH canonical and legacy layouts so a cleared session on
// one side doesn't phantom-resume via the other during the dual-resolve
// window.
func (s *Store) ClearState() error {
	err := os.Remove(filepath.Join(s.root, "session.json"))
	if s.legacyRoot != "" && s.legacyRoot != s.root {
		legacyErr := os.Remove(filepath.Join(s.legacyRoot, "session.json"))
		// Keep the first real error. ENOENT on either side is fine —
		// dual-write may have been partial, and the caller treats
		// "already cleared" as success.
		if err != nil && errors.Is(err, fs.ErrNotExist) {
			err = nil
		}
		if legacyErr != nil && !errors.Is(legacyErr, fs.ErrNotExist) && err == nil {
			err = legacyErr
		}
	}
	return err
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
	if err := writeAtomicJSON(fullPath, data); err != nil {
		return err
	}
	// Dual-write tee into the legacy layout. Partial-write failures on
	// the tee are logged (later: telemetry) but not surfaced to the
	// caller — the authoritative state is already persisted, and the
	// legacy tee exists only for rollback safety.
	if s.legacyRoot != "" && s.legacyRoot != s.root {
		legacyPath := filepath.Join(s.legacyRoot, relPath)
		_ = writeAtomicJSON(legacyPath, data)
	}
	return nil
}

// writeAtomicJSON creates parent dirs then writes data via tmp+rename
// for crash-safety. Extracted so the dual-write tee reuses the same
// atomicity as the primary write.
func writeAtomicJSON(fullPath string, data []byte) error {
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
