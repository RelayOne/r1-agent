// Package daemon implements R1's interactive daemon mode: a long-running
// process that maintains a persistent FIFO task queue, dispatches work to
// up to MaxParallel workers, writes a write-ahead log so crashes can resume,
// and exposes an HTTP API so the user (or any other agent) can enqueue
// tasks, inspect state, adjust parallelism, install hooks, or pause/resume.
//
// Design goals:
//   - Survives crashes. Queue + WAL are persisted to $HOME/.stoke/ on every
//     mutation. On startup the daemon replays state and resumes in-flight tasks.
//   - Proof-first. Every task carries an EstimateBytes hint; on completion the
//     daemon records ActualBytes (from PR additions+deletions or PROOFS.md size)
//     and flags Underdelivered when actual < 0.8 * estimate.
//   - Self-modifying. POST /workers and POST /hooks/install let the operator
//     reshape the daemon at runtime without restart.
package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// TaskState is the lifecycle phase of a queued task.
type TaskState string

const (
	StateQueued    TaskState = "queued"
	StateRunning   TaskState = "running"
	StateDone      TaskState = "done"
	StateFailed    TaskState = "failed"
	StateCancelled TaskState = "cancelled"
)

// Task is a single unit of work in the queue.
//
// EstimateBytes is the dispatcher's prediction of how much code the task
// should touch. After completion the daemon compares it against ActualBytes
// (computed from the task's PR diff or its PROOFS.md size) and flags
// Underdelivered when ActualBytes < 0.8 * EstimateBytes.
//
// Repo is optional but lets workers run with `cmd.Dir=Repo` and lets the
// completion hook know which repo to query for PR additions+deletions.
//
// Hooks (queue/start/done/fail) are local to a single task; the daemon
// itself maintains a separate global hook list managed via /hooks/install.
type Task struct {
	ID               string            `json:"id"`
	IdempotencyKey   string            `json:"idempotency_key,omitempty"`
	Title            string            `json:"title"`
	Prompt           string            `json:"prompt"`
	Repo             string            `json:"repo,omitempty"`
	Runner           string            `json:"runner,omitempty"`         // claude | codex | hybrid | native (default: hybrid)
	EstimateBytes    int64             `json:"estimate_bytes,omitempty"` // 0 = no delta check
	ActualBytes      int64             `json:"actual_bytes"`
	DeltaPct         *int              `json:"delta_pct,omitempty"`
	Underdelivered   bool              `json:"underdelivered"`
	Priority         int               `json:"priority"` // higher = sooner
	State            TaskState         `json:"state"`
	Attempts         int               `json:"attempts"`
	MaxAttempts      int               `json:"max_attempts,omitempty"`
	LastAttemptAt    *time.Time        `json:"last_attempt_at,omitempty"`
	NextRetryAt      *time.Time        `json:"next_retry_at,omitempty"`
	LastErrorClass   string            `json:"last_error_class,omitempty"`
	ResumeCheckpoint string            `json:"resume_checkpoint,omitempty"`
	EnqueuedAt       time.Time         `json:"enqueued_at"`
	StartedAt        *time.Time        `json:"started_at,omitempty"`
	FinishedAt       *time.Time        `json:"finished_at,omitempty"`
	WorkerID         string            `json:"worker_id,omitempty"`
	MissionID        string            `json:"mission_id,omitempty"`
	Error            string            `json:"error,omitempty"`
	ProofsPath       string            `json:"proofs_path,omitempty"`
	Tags             []string          `json:"tags,omitempty"`
	Meta             map[string]string `json:"meta,omitempty"`
}

// Queue is a persistent FIFO+priority queue. Mutations are serialized through
// q.mu and atomically flushed to disk via the rename-on-temp pattern, so a
// crash in the middle of an enqueue cannot corrupt the on-disk state.
type Queue struct {
	mu    sync.Mutex
	path  string
	tasks []*Task
}

// NewQueue opens (or creates) the queue file at path. If the file exists it
// is loaded; if not, an empty queue is created and persisted.
func NewQueue(path string) (*Queue, error) {
	if path == "" {
		return nil, errors.New("queue path required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir queue dir: %w", err)
	}
	q := &Queue{path: path}
	if data, err := os.ReadFile(path); err == nil {
		if len(data) > 0 {
			if err := json.Unmarshal(data, &q.tasks); err != nil {
				return nil, fmt.Errorf("parse queue: %w", err)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read queue: %w", err)
	}
	if q.tasks == nil {
		q.tasks = []*Task{}
	}
	if err := q.flushLocked(); err != nil {
		return nil, err
	}
	return q, nil
}

// Enqueue adds a task. If t.ID is empty, a time-based ID is assigned. If a
// task with the same ID already exists, ErrDuplicateID is returned.
var ErrDuplicateID = errors.New("task id already in queue")

func (q *Queue) Enqueue(t *Task) error {
	_, _, err := q.EnqueueOrGet(t)
	return err
}

// EnqueueOrGet adds a task unless an existing task already owns the same
// idempotency key. Returns the stored task and whether the request deduplicated.
func (q *Queue) EnqueueOrGet(t *Task) (*Task, bool, error) {
	if t == nil {
		return nil, false, errors.New("nil task")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if t.ID == "" {
		t.ID = fmt.Sprintf("t-%d", time.Now().UnixNano())
	}
	if t.IdempotencyKey != "" {
		if existing := q.findByIdempotencyKeyLocked(t.IdempotencyKey); existing != nil {
			cp := *existing
			return &cp, true, nil
		}
	}
	for _, existing := range q.tasks {
		if existing.ID == t.ID {
			return nil, false, ErrDuplicateID
		}
	}
	if t.State == "" {
		t.State = StateQueued
	}
	if t.MaxAttempts <= 0 {
		t.MaxAttempts = 3
	}
	if t.EnqueuedAt.IsZero() {
		t.EnqueuedAt = time.Now().UTC()
	}
	q.tasks = append(q.tasks, t)
	if err := q.flushLocked(); err != nil {
		return nil, false, err
	}
	cp := *t
	return &cp, false, nil
}

// Next returns the highest-priority queued task and marks it Running.
// Returns nil, nil when the queue has no work.
func (q *Queue) Next(workerID string) (*Task, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	// Sort a snapshot of indices so we don't disturb on-disk order.
	queued := []int{}
	for i, t := range q.tasks {
		if t.State == StateQueued && !isWaitingForRetry(t, time.Now().UTC()) {
			queued = append(queued, i)
		}
	}
	if len(queued) == 0 {
		return nil, nil
	}
	sort.SliceStable(queued, func(a, b int) bool {
		ta, tb := q.tasks[queued[a]], q.tasks[queued[b]]
		if ta.Priority != tb.Priority {
			return ta.Priority > tb.Priority
		}
		return ta.EnqueuedAt.Before(tb.EnqueuedAt)
	})
	t := q.tasks[queued[0]]
	t.State = StateRunning
	now := time.Now().UTC()
	t.Attempts++
	t.LastAttemptAt = &now
	t.StartedAt = &now
	t.NextRetryAt = nil
	t.WorkerID = workerID
	if err := q.flushLocked(); err != nil {
		return nil, err
	}
	return t, nil
}

// Complete marks a task done with the actual touched-byte count. If actual
// is below 80% of the task's estimate (and estimate > 0), the task is
// flagged Underdelivered.
func (q *Queue) Complete(id string, actualBytes int64, missionID, proofsPath string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	t := q.findLocked(id)
	if t == nil {
		return fmt.Errorf("task %s not found", id)
	}
	t.State = StateDone
	now := time.Now().UTC()
	t.FinishedAt = &now
	t.ActualBytes = actualBytes
	t.MissionID = missionID
	t.ProofsPath = proofsPath
	if t.EstimateBytes > 0 {
		pct := int(actualBytes * 100 / t.EstimateBytes)
		t.DeltaPct = &pct
		if pct < 80 {
			t.Underdelivered = true
		}
	}
	return q.flushLocked()
}

// Fail marks a task failed with an error message.
func (q *Queue) Fail(id, errMsg string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	t := q.findLocked(id)
	if t == nil {
		return fmt.Errorf("task %s not found", id)
	}
	t.State = StateFailed
	now := time.Now().UTC()
	t.FinishedAt = &now
	t.NextRetryAt = nil
	t.Error = errMsg
	return q.flushLocked()
}

// Retry requeues a running task for another attempt after a backoff delay.
func (q *Queue) Retry(id, errMsg, errorClass, resumeCheckpoint string, nextRetry time.Time) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	t := q.findLocked(id)
	if t == nil {
		return fmt.Errorf("task %s not found", id)
	}
	t.State = StateQueued
	t.WorkerID = ""
	t.StartedAt = nil
	t.FinishedAt = nil
	t.Error = errMsg
	t.LastErrorClass = errorClass
	t.ResumeCheckpoint = resumeCheckpoint
	if !nextRetry.IsZero() {
		next := nextRetry.UTC()
		t.NextRetryAt = &next
	} else {
		t.NextRetryAt = nil
	}
	return q.flushLocked()
}

// Cancel marks a queued or running task cancelled. (Worker code is
// responsible for noticing the state change and aborting cleanly.)
func (q *Queue) Cancel(id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	t := q.findLocked(id)
	if t == nil {
		return fmt.Errorf("task %s not found", id)
	}
	if t.State == StateDone || t.State == StateFailed {
		return fmt.Errorf("task %s already finished", id)
	}
	t.State = StateCancelled
	now := time.Now().UTC()
	t.FinishedAt = &now
	t.WorkerID = ""
	return q.flushLocked()
}

// Get returns a copy of the task with the given ID, or nil.
func (q *Queue) Get(id string) *Task {
	q.mu.Lock()
	defer q.mu.Unlock()
	t := q.findLocked(id)
	if t == nil {
		return nil
	}
	cp := *t
	return &cp
}

// List returns a copy of all tasks, optionally filtered to a single state.
// Pass empty state to return everything.
func (q *Queue) List(state TaskState) []*Task {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := []*Task{}
	for _, t := range q.tasks {
		if state == "" || t.State == state {
			cp := *t
			out = append(out, &cp)
		}
	}
	return out
}

// Counts returns the number of tasks per state.
func (q *Queue) Counts() map[TaskState]int {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := map[TaskState]int{}
	for _, t := range q.tasks {
		out[t.State]++
	}
	return out
}

// ReadyQueuedCount returns queued tasks whose retry window has elapsed.
func (q *Queue) ReadyQueuedCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := time.Now().UTC()
	count := 0
	for _, t := range q.tasks {
		if t.State == StateQueued && !isWaitingForRetry(t, now) {
			count++
		}
	}
	return count
}

// ResumeRunning reverts every task currently marked Running back to Queued.
// Called once at daemon startup so a crashed-mid-task resumes cleanly.
func (q *Queue) ResumeRunning() (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := 0
	for _, t := range q.tasks {
		if t.State == StateRunning {
			t.State = StateQueued
			t.WorkerID = ""
			t.StartedAt = nil
			n++
		}
	}
	if n == 0 {
		return 0, nil
	}
	return n, q.flushLocked()
}

// ApplyRecoveryCheckpoints annotates queued tasks with restart checkpoints derived from the WAL.
func (q *Queue) ApplyRecoveryCheckpoints(checkpoints map[string]string) error {
	if len(checkpoints) == 0 {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	changed := 0
	for id, checkpoint := range checkpoints {
		t := q.findLocked(id)
		if t == nil || checkpoint == "" {
			continue
		}
		t.ResumeCheckpoint = checkpoint
		if t.Meta == nil {
			t.Meta = map[string]string{}
		}
		t.Meta["resume_checkpoint"] = checkpoint
		t.Meta["recovered_from_wal"] = "true"
		changed++
	}
	if changed == 0 {
		return nil
	}
	return q.flushLocked()
}

func (q *Queue) findLocked(id string) *Task {
	for _, t := range q.tasks {
		if t.ID == id {
			return t
		}
	}
	return nil
}

func (q *Queue) findByIdempotencyKeyLocked(key string) *Task {
	for _, t := range q.tasks {
		if t.IdempotencyKey == key && t.State != StateFailed && t.State != StateCancelled {
			return t
		}
	}
	return nil
}

// flushLocked writes the current task slice to disk via temp+rename.
// Caller must hold q.mu.
func (q *Queue) flushLocked() error {
	data, err := json.MarshalIndent(q.tasks, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal queue: %w", err)
	}
	tmp := q.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write queue tmp: %w", err)
	}
	if err := os.Rename(tmp, q.path); err != nil {
		return fmt.Errorf("rename queue: %w", err)
	}
	return nil
}

func isWaitingForRetry(t *Task, now time.Time) bool {
	return t.NextRetryAt != nil && t.NextRetryAt.After(now)
}
