// Package checkpoint implements synchronous checkpointing before dangerous operations.
// Inspired by SOTA patterns (OpenHands, community consensus):
// - Synchronous, atomic checkpoint writes before any dangerous operation
// - Rolling buffer: current + previous + recovery
// - Validation-first recovery: verify integrity, confirm resources exist
// - State machine: pending → running → checkpointed → completed | failed
//
// Use this at finer granularity than session-level persistence: checkpoint
// after each successful phase completion, not just at task boundaries.
package checkpoint

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Phase represents the execution phase at checkpoint time.
type Phase string

const (
	PhasePending      Phase = "pending"
	PhaseRunning      Phase = "running"
	PhaseCheckpointed Phase = "checkpointed"
	PhaseCompleted    Phase = "completed"
	PhaseFailed       Phase = "failed"
)

// Checkpoint captures the full state at a point in time.
type Checkpoint struct {
	ID             string         `json:"id"`
	TaskID         string         `json:"task_id"`
	Phase          Phase          `json:"phase"`
	Step           int            `json:"step"`          // monotonic step counter
	WorktreePath   string         `json:"worktree_path"`
	Branch         string         `json:"branch"`
	BaseCommit     string         `json:"base_commit"`
	HeadCommit     string         `json:"head_commit,omitempty"`
	CostUSD        float64        `json:"cost_usd"`
	Attempt        int            `json:"attempt"`
	IdempotencyKey string         `json:"idempotency_key"` // prevents duplicate side effects
	Metadata       map[string]any `json:"metadata,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
}

// Store manages checkpoint persistence with a rolling buffer.
type Store struct {
	mu  sync.Mutex
	dir string
}

// NewStore creates a checkpoint store in the given directory.
func NewStore(dir string) *Store {
	os.MkdirAll(dir, 0700)
	return &Store{dir: dir}
}

// Save writes a checkpoint atomically. Rotates: current→previous→recovery.
func (s *Store) Save(cp Checkpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cp.CreatedAt = time.Now()

	// Rotate existing checkpoints
	recovery := s.path("recovery")
	previous := s.path("previous")
	current := s.path("current")

	os.Remove(recovery)
	os.Rename(previous, recovery)
	os.Rename(current, previous)

	// Write new current
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal checkpoint: %w", err)
	}

	tmp := current + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("write checkpoint: %w", err)
	}
	return os.Rename(tmp, current)
}

// Load reads the most recent valid checkpoint.
// Tries current → previous → recovery in order.
func (s *Store) Load() (*Checkpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, name := range []string{"current", "previous", "recovery"} {
		cp, err := s.loadFile(name)
		if err == nil && cp != nil {
			return cp, nil
		}
	}
	return nil, nil // no checkpoint found
}

// LoadForTask loads the most recent checkpoint for a specific task.
func (s *Store) LoadForTask(taskID string) (*Checkpoint, error) {
	cp, err := s.Load()
	if err != nil {
		return nil, err
	}
	if cp != nil && cp.TaskID == taskID {
		return cp, nil
	}
	return nil, nil
}

// Clear removes all checkpoints (called on successful completion).
func (s *Store) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	os.Remove(s.path("current"))
	os.Remove(s.path("previous"))
	os.Remove(s.path("recovery"))
}

// Validate checks that a checkpoint's referenced resources still exist.
func Validate(cp *Checkpoint) []string {
	var issues []string

	if cp.WorktreePath != "" {
		if _, err := os.Stat(cp.WorktreePath); os.IsNotExist(err) {
			issues = append(issues, fmt.Sprintf("worktree path %q does not exist", cp.WorktreePath))
		}
	}

	if cp.Phase == "" {
		issues = append(issues, "missing phase")
	}

	if cp.TaskID == "" {
		issues = append(issues, "missing task ID")
	}

	return issues
}

// ShouldResume decides if execution should resume from a checkpoint.
func ShouldResume(cp *Checkpoint) bool {
	if cp == nil {
		return false
	}
	// Resume from running or checkpointed states
	return cp.Phase == PhaseRunning || cp.Phase == PhaseCheckpointed
}

// IdempotencyKey generates a unique key for a task+step+attempt combination.
func IdempotencyKey(taskID string, step, attempt int) string {
	return fmt.Sprintf("%s:step%d:attempt%d", taskID, step, attempt)
}

// --- Internal ---

func (s *Store) path(name string) string {
	return filepath.Join(s.dir, "checkpoint-"+name+".json")
}

func (s *Store) loadFile(name string) (*Checkpoint, error) {
	data, err := os.ReadFile(s.path(name))
	if err != nil {
		return nil, err
	}
	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, err
	}
	return &cp, nil
}
