// Package boulder implements idle detection and continuation enforcement.
// Inspired by OmO's Boulder/Todo Continuation Enforcer: monitors agent activity
// and forces re-engagement when agents go idle with incomplete tasks.
//
// Key patterns from OmO:
// - On session idle, check for incomplete todos → force continuation
// - Exponential backoff: 30s base, x2 per failure, max 5 failures then 5-min pause
// - State persisted in .stoke/boulder.json
// - Prevents agents from claiming "done" when work remains
package boulder

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// TaskStatus represents a tracked task's completion state.
type TaskStatus string

const (
	StatusPending    TaskStatus = "pending"
	StatusInProgress TaskStatus = "in_progress"
	StatusComplete   TaskStatus = "complete"
	StatusBlocked    TaskStatus = "blocked"
)

// TrackedTask is a task the enforcer monitors for completion.
type TrackedTask struct {
	ID          string     `json:"id"`
	Description string     `json:"description"`
	Status      TaskStatus `json:"status"`
	WorktreeID  string     `json:"worktree_id,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// NudgeRecord tracks a single nudge event.
type NudgeRecord struct {
	TaskID    string    `json:"task_id"`
	Attempt   int       `json:"attempt"`
	SentAt    time.Time `json:"sent_at"`
	Message   string    `json:"message"`
}

// State persists enforcer state across sessions.
type State struct {
	Tasks          []TrackedTask `json:"tasks"`
	Nudges         []NudgeRecord `json:"nudges"`
	ConsecFailures int           `json:"consec_failures"`
	LastActivity   time.Time     `json:"last_activity"`
	PausedUntil    time.Time     `json:"paused_until,omitempty"`
}

// Config controls enforcer behavior.
type Config struct {
	IdleTimeout     time.Duration // how long before considering agent idle (default 30s)
	BaseBackoff     time.Duration // initial backoff between nudges (default 30s)
	BackoffMultiple float64       // multiplier per consecutive failure (default 2.0)
	MaxFailures     int           // failures before extended pause (default 5)
	PauseDuration   time.Duration // extended pause after max failures (default 5min)
	MaxNudges       int           // max nudges per task (default 3)
	ScanInterval    time.Duration // minimum time between scans (default 5s)
}

// DefaultConfig returns production defaults matching OmO's behavior.
func DefaultConfig() Config {
	return Config{
		IdleTimeout:     30 * time.Second,
		BaseBackoff:     30 * time.Second,
		BackoffMultiple: 2.0,
		MaxFailures:     5,
		PauseDuration:   5 * time.Minute,
		MaxNudges:       3,
		ScanInterval:    5 * time.Second,
	}
}

// NudgeFunc is called when the enforcer wants to re-engage an idle agent.
// Returns true if the nudge was delivered successfully.
type NudgeFunc func(taskID, message string) bool

// Enforcer monitors tasks and nudges idle agents back to work.
type Enforcer struct {
	mu       sync.Mutex
	cfg      Config
	state    State
	stateDir string
	lastScan time.Time
}

// New creates an enforcer, loading persisted state if available.
func New(stateDir string, cfg Config) *Enforcer {
	e := &Enforcer{
		cfg:      cfg,
		stateDir: stateDir,
	}
	e.loadState()
	return e
}

// TrackTask adds a task for monitoring.
func (e *Enforcer) TrackTask(id, description, worktreeID string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	now := time.Now()
	// Don't duplicate
	for i, t := range e.state.Tasks {
		if t.ID == id {
			e.state.Tasks[i].Description = description
			e.state.Tasks[i].UpdatedAt = now
			return
		}
	}

	e.state.Tasks = append(e.state.Tasks, TrackedTask{
		ID:          id,
		Description: description,
		Status:      StatusPending,
		WorktreeID:  worktreeID,
		CreatedAt:   now,
		UpdatedAt:   now,
	})
	e.saveState()
}

// UpdateStatus updates a task's completion status.
func (e *Enforcer) UpdateStatus(id string, status TaskStatus) {
	e.mu.Lock()
	defer e.mu.Unlock()

	for i, t := range e.state.Tasks {
		if t.ID == id {
			e.state.Tasks[i].Status = status
			e.state.Tasks[i].UpdatedAt = time.Now()
			break
		}
	}
	e.state.LastActivity = time.Now()
	e.saveState()
}

// RecordActivity marks that an agent did something (resets idle timer).
func (e *Enforcer) RecordActivity() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.state.LastActivity = time.Now()
}

// Scan checks for idle agents with incomplete tasks and sends nudges.
// Returns the number of nudges sent. Call this periodically.
func (e *Enforcer) Scan(now time.Time, nudge NudgeFunc) int {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Throttle scans
	if now.Sub(e.lastScan) < e.cfg.ScanInterval {
		return 0
	}
	e.lastScan = now

	// Check if we're in extended pause
	if !e.state.PausedUntil.IsZero() && now.Before(e.state.PausedUntil) {
		return 0
	}

	// Not idle yet?
	if !e.state.LastActivity.IsZero() && now.Sub(e.state.LastActivity) < e.cfg.IdleTimeout {
		return 0
	}

	// Find incomplete tasks
	incomplete := e.incompleteTasks()
	if len(incomplete) == 0 {
		return 0
	}

	sent := 0
	for _, task := range incomplete {
		nudgeCount := e.nudgeCountForTask(task.ID)
		if nudgeCount >= e.cfg.MaxNudges {
			continue
		}

		// Compute backoff
		backoff := e.cfg.BaseBackoff
		for i := 0; i < e.state.ConsecFailures; i++ {
			backoff = time.Duration(float64(backoff) * e.cfg.BackoffMultiple)
		}

		// Check if enough time has passed since last nudge for this task
		lastNudge := e.lastNudgeTime(task.ID)
		if !lastNudge.IsZero() && now.Sub(lastNudge) < backoff {
			continue
		}

		msg := fmt.Sprintf("Task %q (ID: %s) is still incomplete. Status: %s. Please continue working on it.",
			task.Description, task.ID, task.Status)

		delivered := nudge(task.ID, msg)

		e.state.Nudges = append(e.state.Nudges, NudgeRecord{
			TaskID:  task.ID,
			Attempt: nudgeCount + 1,
			SentAt:  now,
			Message: msg,
		})

		if delivered {
			e.state.ConsecFailures = 0
			sent++
		} else {
			e.state.ConsecFailures++
			if e.state.ConsecFailures >= e.cfg.MaxFailures {
				e.state.PausedUntil = now.Add(e.cfg.PauseDuration)
				break
			}
		}
	}

	e.saveState()
	return sent
}

// IncompleteTasks returns tasks that aren't done yet.
func (e *Enforcer) IncompleteTasks() []TrackedTask {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.incompleteTasks()
}

func (e *Enforcer) incompleteTasks() []TrackedTask {
	var result []TrackedTask
	for _, t := range e.state.Tasks {
		if t.Status != StatusComplete {
			result = append(result, t)
		}
	}
	return result
}

// IsPaused returns true if the enforcer is in extended pause.
func (e *Enforcer) IsPaused(now time.Time) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return !e.state.PausedUntil.IsZero() && now.Before(e.state.PausedUntil)
}

// ConsecFailures returns the current consecutive failure count.
func (e *Enforcer) ConsecFailures() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state.ConsecFailures
}

// NudgeCount returns the total number of nudges sent for a task.
func (e *Enforcer) NudgeCount(taskID string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.nudgeCountForTask(taskID)
}

func (e *Enforcer) nudgeCountForTask(taskID string) int {
	count := 0
	for _, n := range e.state.Nudges {
		if n.TaskID == taskID {
			count++
		}
	}
	return count
}

func (e *Enforcer) lastNudgeTime(taskID string) time.Time {
	var last time.Time
	for _, n := range e.state.Nudges {
		if n.TaskID == taskID && n.SentAt.After(last) {
			last = n.SentAt
		}
	}
	return last
}

// --- Persistence ---

func (e *Enforcer) loadState() {
	path := filepath.Join(e.stateDir, "boulder.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	json.Unmarshal(data, &e.state)
}

func (e *Enforcer) saveState() {
	os.MkdirAll(e.stateDir, 0700)
	data, err := json.MarshalIndent(e.state, "", "  ")
	if err != nil {
		return
	}
	path := filepath.Join(e.stateDir, "boulder.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return
	}
	os.Rename(tmp, path)
}
