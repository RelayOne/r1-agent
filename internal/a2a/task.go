// Package a2a — task.go
//
// STOKE-018 task lifecycle: A2A Task state machine with the 7
// spec states (submitted → working → input-required →
// completed/failed/canceled/rejected), plus a TaskStore +
// JSON-RPC handler surface so A2A peers can submit tasks and
// poll status.
//
// Scope of this file:
//
//   - TaskStatus constants (7 states per the A2A spec)
//   - Task + TaskUpdate structs
//   - TaskStore interface + InMemoryTaskStore reference impl
//   - State transition table with explicit allowed edges
//   - JSON-RPC-shaped request/response types for
//     `a2a.task.submit` / `a2a.task.status` / `a2a.task.cancel`
//   - Dispatcher that wires JSON-RPC methods to store ops
//
// The JSON-RPC transport (framing, socket management, auth)
// lives elsewhere (cmd/r1-acp/ for stdio, a future
// cmd/r1-a2a-serve/ for HTTP); this file provides the
// method handlers those transports call.
package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// TaskStatus names the 7 A2A spec states.
type TaskStatus string

const (
	TaskSubmitted    TaskStatus = "submitted"
	TaskWorking      TaskStatus = "working"
	TaskInputRequired TaskStatus = "input-required"
	TaskCompleted    TaskStatus = "completed"
	TaskFailed       TaskStatus = "failed"
	TaskCanceled     TaskStatus = "canceled"
	TaskRejected     TaskStatus = "rejected"
)

// AllTaskStatuses returns the 7 declared statuses in
// canonical order. Used by tests + discovery UIs.
func AllTaskStatuses() []TaskStatus {
	return []TaskStatus{
		TaskSubmitted, TaskWorking, TaskInputRequired,
		TaskCompleted, TaskFailed, TaskCanceled, TaskRejected,
	}
}

// IsTerminal reports whether a task has reached an end state.
// Completed / Failed / Canceled / Rejected are terminal;
// Submitted / Working / InputRequired are not.
func (s TaskStatus) IsTerminal() bool {
	switch s {
	case TaskCompleted, TaskFailed, TaskCanceled, TaskRejected:
		return true
	case TaskSubmitted, TaskWorking, TaskInputRequired:
		return false
	default:
		return false
	}
}

// Task is one A2A task being tracked by this agent.
type Task struct {
	ID        string          `json:"id"`
	Status    TaskStatus      `json:"status"`
	Prompt    json.RawMessage `json:"prompt,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`

	// History is the append-only log of status transitions +
	// artifact emissions. Peers polling status can consume this
	// as a streaming log without subscribing to a separate
	// channel. Bounded by MaxHistoryEntries to prevent
	// unbounded growth for long-running tasks.
	History []TaskUpdate `json:"history,omitempty"`

	// Artifacts are the outputs the task has produced. Opaque
	// JSON blobs; A2A spec lets the agent define shape per
	// task kind.
	Artifacts []json.RawMessage `json:"artifacts,omitempty"`

	// Result is populated on terminal success; Error on
	// terminal failure. Exactly one is non-empty when
	// IsTerminal(Status) is true.
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// TaskUpdate is one entry in the Task history.
type TaskUpdate struct {
	Status  TaskStatus `json:"status"`
	At      time.Time  `json:"at"`
	Message string     `json:"message,omitempty"`
}

// MaxHistoryEntries caps the history slice length so a
// runaway task doesn't eat memory. Older entries are dropped
// from the head when the cap is exceeded (first-in, first-out).
const MaxHistoryEntries = 256

// transitions is the explicit allow-list of task status
// changes. Absence of an entry means the transition is
// forbidden. Mirrors the a2a spec's state diagram.
var taskTransitions = map[TaskStatus]map[TaskStatus]bool{
	TaskSubmitted: {
		TaskWorking:   true, // agent picked up
		TaskRejected:  true, // agent rejected before starting
		TaskCanceled:  true, // submitter canceled before start
	},
	TaskWorking: {
		TaskInputRequired: true,
		TaskCompleted:     true,
		TaskFailed:        true,
		TaskCanceled:      true,
	},
	TaskInputRequired: {
		TaskWorking:  true, // input received, resume
		TaskCanceled: true,
		TaskFailed:   true, // timeout waiting for input
	},
	// All terminal states: no outbound edges.
	TaskCompleted: {},
	TaskFailed:    {},
	TaskCanceled:  {},
	TaskRejected:  {},
}

// ErrInvalidTaskTransition is returned by Store.Transition
// when the requested edge isn't permitted.
var ErrInvalidTaskTransition = errors.New("a2a: invalid task status transition")

// ErrTaskNotFound is returned when Store.Get/Transition can't
// find the named task.
var ErrTaskNotFound = errors.New("a2a: task not found")

// TaskStore holds tasks + enforces the transition table.
//
// Complete + Fail are atomic transition + payload writes —
// use them in preference to Transition-then-SetResult so A2A
// peers never observe a terminal task with a missing
// payload (the documented invariant that terminal tasks
// carry exactly one of Result / Error).
type TaskStore interface {
	Submit(ctx context.Context, prompt json.RawMessage) (Task, error)
	Get(ctx context.Context, id string) (Task, error)
	Transition(ctx context.Context, id string, to TaskStatus, message string) (Task, error)
	Complete(ctx context.Context, id string, result json.RawMessage, message string) (Task, error)
	Fail(ctx context.Context, id string, errMsg, message string) (Task, error)
	AppendArtifact(ctx context.Context, id string, artifact json.RawMessage) (Task, error)
	SetResult(ctx context.Context, id string, result json.RawMessage) (Task, error)
	SetError(ctx context.Context, id string, errMsg string) (Task, error)
	List(ctx context.Context) ([]Task, error)
}

// InMemoryTaskStore is the reference TaskStore. Thread-safe.
type InMemoryTaskStore struct {
	mu    sync.Mutex
	tasks map[string]*Task
	now   func() time.Time
}

// NewInMemoryTaskStore returns an empty in-memory store. Uses
// time.Now() as its clock; tests that want determinism can
// set SetClock.
func NewInMemoryTaskStore() *InMemoryTaskStore {
	return &InMemoryTaskStore{
		tasks: map[string]*Task{},
		now:   func() time.Time { return time.Now().UTC() },
	}
}

// SetClock overrides the store's time source. Tests only.
func (s *InMemoryTaskStore) SetClock(clock func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = clock
}

// Submit creates a new task in the Submitted state.
//
// The caller-provided `prompt` bytes are COPIED into the
// store — a caller that mutates its original buffer after
// Submit cannot alter the stored prompt. The returned Task
// is also deep-copied so the caller can freely mutate its
// copy.
func (s *InMemoryTaskStore) Submit(_ context.Context, prompt json.RawMessage) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	id := uuid.NewString()
	t := &Task{
		ID:        id,
		Status:    TaskSubmitted,
		Prompt:    copyRaw(prompt),
		CreatedAt: now,
		UpdatedAt: now,
		History: []TaskUpdate{
			{Status: TaskSubmitted, At: now, Message: "submitted"},
		},
	}
	s.tasks[id] = t
	return cloneTask(t), nil
}

// Get returns a clone of the task so callers can't mutate the
// store by modifying the returned value.
func (s *InMemoryTaskStore) Get(_ context.Context, id string) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return Task{}, ErrTaskNotFound
	}
	return cloneTask(t), nil
}

// Transition applies a status change and appends a history
// entry. Returns ErrInvalidTaskTransition when the edge isn't
// in the transition table.
func (s *InMemoryTaskStore) Transition(_ context.Context, id string, to TaskStatus, message string) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return Task{}, ErrTaskNotFound
	}
	edges, ok := taskTransitions[t.Status]
	if !ok {
		return Task{}, fmt.Errorf("%w: unknown from-state %q", ErrInvalidTaskTransition, t.Status)
	}
	if !edges[to] {
		return Task{}, fmt.Errorf("%w: %s -> %s", ErrInvalidTaskTransition, t.Status, to)
	}
	now := s.now()
	t.Status = to
	t.UpdatedAt = now
	t.History = append(t.History, TaskUpdate{Status: to, At: now, Message: message})
	if len(t.History) > MaxHistoryEntries {
		drop := len(t.History) - MaxHistoryEntries
		t.History = t.History[drop:]
	}
	return cloneTask(t), nil
}

// ErrInvalidTaskStateForField is returned by SetResult /
// SetError when the task's current status doesn't match what
// the field expects. Prevents exposing contradictions (a
// Submitted task carrying a Result, a Completed task carrying
// an Error, etc.) to A2A peers polling via HandleStatus.
var ErrInvalidTaskStateForField = errors.New("a2a: task status disallows this field")

// Complete atomically transitions to TaskCompleted AND
// writes the result payload under the same lock. Peers
// polling via Get/HandleStatus will NEVER observe the
// intermediate (status=completed, result=nil) state that
// Transition-then-SetResult exposes. The documented
// invariant — terminal tasks carry exactly one of
// Result / Error — is enforced structurally.
func (s *InMemoryTaskStore) Complete(_ context.Context, id string, result json.RawMessage, message string) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return Task{}, ErrTaskNotFound
	}
	edges, ok := taskTransitions[t.Status]
	if !ok {
		return Task{}, fmt.Errorf("%w: unknown from-state %q", ErrInvalidTaskTransition, t.Status)
	}
	if !edges[TaskCompleted] {
		return Task{}, fmt.Errorf("%w: %s -> %s", ErrInvalidTaskTransition, t.Status, TaskCompleted)
	}
	now := s.now()
	t.Status = TaskCompleted
	t.Result = copyRaw(result)
	t.Error = ""
	t.UpdatedAt = now
	t.History = append(t.History, TaskUpdate{Status: TaskCompleted, At: now, Message: message})
	if len(t.History) > MaxHistoryEntries {
		drop := len(t.History) - MaxHistoryEntries
		t.History = t.History[drop:]
	}
	return cloneTask(t), nil
}

// Fail atomically transitions to TaskFailed AND writes the
// error message under the same lock. Mirror of Complete for
// the failure path.
func (s *InMemoryTaskStore) Fail(_ context.Context, id string, errMsg, message string) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return Task{}, ErrTaskNotFound
	}
	edges, ok := taskTransitions[t.Status]
	if !ok {
		return Task{}, fmt.Errorf("%w: unknown from-state %q", ErrInvalidTaskTransition, t.Status)
	}
	if !edges[TaskFailed] {
		return Task{}, fmt.Errorf("%w: %s -> %s", ErrInvalidTaskTransition, t.Status, TaskFailed)
	}
	now := s.now()
	t.Status = TaskFailed
	t.Error = errMsg
	t.Result = nil
	t.UpdatedAt = now
	t.History = append(t.History, TaskUpdate{Status: TaskFailed, At: now, Message: message})
	if len(t.History) > MaxHistoryEntries {
		drop := len(t.History) - MaxHistoryEntries
		t.History = t.History[drop:]
	}
	return cloneTask(t), nil
}

// AppendArtifact attaches an opaque artifact blob to the task.
// The artifact bytes are COPIED into the store so later
// caller-side mutations don't leak through.
func (s *InMemoryTaskStore) AppendArtifact(_ context.Context, id string, artifact json.RawMessage) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return Task{}, ErrTaskNotFound
	}
	t.Artifacts = append(t.Artifacts, copyRaw(artifact))
	t.UpdatedAt = s.now()
	return cloneTask(t), nil
}

// SetResult populates the result field. Only valid on
// TaskCompleted tasks — any other status returns
// ErrInvalidTaskStateForField so A2A peers never observe a
// Submitted/Working/Failed task carrying a Result (the
// documented lifecycle guarantees Result implies Completed).
// Clears any pre-existing Error on the task — the two fields
// are mutually exclusive on terminal state.
func (s *InMemoryTaskStore) SetResult(_ context.Context, id string, result json.RawMessage) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return Task{}, ErrTaskNotFound
	}
	if t.Status != TaskCompleted {
		return Task{}, fmt.Errorf("%w: SetResult requires status=%q, got %q", ErrInvalidTaskStateForField, TaskCompleted, t.Status)
	}
	t.Result = copyRaw(result)
	t.Error = "" // mutual exclusion
	t.UpdatedAt = s.now()
	return cloneTask(t), nil
}

// SetError populates the error field. Only valid on
// TaskFailed tasks. Rejects any other status with
// ErrInvalidTaskStateForField. Clears any pre-existing Result
// — the two fields are mutually exclusive.
func (s *InMemoryTaskStore) SetError(_ context.Context, id string, errMsg string) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return Task{}, ErrTaskNotFound
	}
	if t.Status != TaskFailed {
		return Task{}, fmt.Errorf("%w: SetError requires status=%q, got %q", ErrInvalidTaskStateForField, TaskFailed, t.Status)
	}
	t.Error = errMsg
	t.Result = nil // mutual exclusion
	t.UpdatedAt = s.now()
	return cloneTask(t), nil
}

// List returns a sorted-by-CreatedAt copy of the task list.
// Used by debugging/ops tools; peers that want live updates
// subscribe via a separate streaming API.
func (s *InMemoryTaskStore) List(_ context.Context) ([]Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Task, 0, len(s.tasks))
	for _, t := range s.tasks {
		out = append(out, cloneTask(t))
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

// copyRaw returns a deep copy of a json.RawMessage so
// aliasing between the caller's buffer and the store's
// stored bytes can't leak mutations in either direction.
// nil input round-trips as nil output (no zero-length
// allocation).
func copyRaw(r json.RawMessage) json.RawMessage {
	if r == nil {
		return nil
	}
	out := make(json.RawMessage, len(r))
	copy(out, r)
	return out
}

func cloneTask(t *Task) Task {
	out := *t
	out.Prompt = copyRaw(t.Prompt)
	out.Result = copyRaw(t.Result)
	out.Error = t.Error
	if len(t.History) > 0 {
		out.History = append([]TaskUpdate(nil), t.History...)
	}
	if len(t.Artifacts) > 0 {
		out.Artifacts = make([]json.RawMessage, len(t.Artifacts))
		for i, a := range t.Artifacts {
			out.Artifacts[i] = copyRaw(a)
		}
	}
	return out
}

// --- JSON-RPC handler surface ---

// HandleSubmit implements the `a2a.task.submit` JSON-RPC
// method. params is the RPC params (typically {"prompt":...}).
// Returns the full Task record with its fresh ID.
func HandleSubmit(ctx context.Context, store TaskStore, params json.RawMessage) (Task, error) {
	var p struct {
		Prompt json.RawMessage `json:"prompt"`
	}
	if len(params) > 0 && string(params) != "null" {
		if err := json.Unmarshal(params, &p); err != nil {
			return Task{}, fmt.Errorf("a2a.task.submit: parse params: %w", err)
		}
	}
	return store.Submit(ctx, p.Prompt)
}

// HandleStatus implements `a2a.task.status`. Returns the
// current task record.
func HandleStatus(ctx context.Context, store TaskStore, params json.RawMessage) (Task, error) {
	var p struct {
		TaskID string `json:"taskId"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return Task{}, fmt.Errorf("a2a.task.status: parse params: %w", err)
	}
	if p.TaskID == "" {
		return Task{}, fmt.Errorf("a2a.task.status: taskId required")
	}
	return store.Get(ctx, p.TaskID)
}

// HandleCancel implements `a2a.task.cancel`. Transitions the
// task to Canceled.
func HandleCancel(ctx context.Context, store TaskStore, params json.RawMessage) (Task, error) {
	var p struct {
		TaskID string `json:"taskId"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return Task{}, fmt.Errorf("a2a.task.cancel: parse params: %w", err)
	}
	if p.TaskID == "" {
		return Task{}, fmt.Errorf("a2a.task.cancel: taskId required")
	}
	return store.Transition(ctx, p.TaskID, TaskCanceled, p.Reason)
}
