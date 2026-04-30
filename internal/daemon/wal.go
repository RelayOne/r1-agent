package daemon

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// WALEvent records a single daemon-level event. The supervisor reads the WAL
// to reconstruct the daemon's history (for resume / debug / audit).
//
// All fields are optional except Type and TS. Use the helper constructors
// (NewIntent / NewDone / NewBlocked) to ensure a consistent shape.
type WALEvent struct {
	TS       time.Time         `json:"ts"`
	Type     string            `json:"type"` // intent | done | blocked | enqueue | start | complete | fail | hook_install | parallelism_change | pause | resume
	TaskID   string            `json:"task_id,omitempty"`
	WorkerID string            `json:"worker_id,omitempty"`
	Message  string            `json:"message,omitempty"`
	Evidence map[string]string `json:"evidence,omitempty"` // file_line / commit / pr / url / cloud_run_rev / curl_probe
}

// WAL is an append-only log of daemon events. Each event is a single JSON
// line; the file is opened with O_APPEND so concurrent writes from the
// daemon's own goroutines stay ordered. The on-disk format is line-delimited
// JSON ("ndjson") for easy `tail -f` debugging.
type WAL struct {
	mu   sync.Mutex
	path string
	f    *os.File
}

// OpenWAL opens (or creates) the WAL file at path.
func OpenWAL(path string) (*WAL, error) {
	if path == "" {
		return nil, errors.New("wal path required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir wal dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open wal: %w", err)
	}
	return &WAL{path: path, f: f}, nil
}

// Append writes one event to the WAL. Sync()s before returning so a crash
// after Append cannot lose the event.
func (w *WAL) Append(ev WALEvent) error {
	if ev.TS.IsZero() {
		ev.TS = time.Now().UTC()
	}
	if ev.Type == "" {
		return errors.New("wal event type required")
	}
	data, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("marshal wal event: %w", err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write wal event: %w", err)
	}
	if err := w.f.Sync(); err != nil {
		return fmt.Errorf("sync wal: %w", err)
	}
	return nil
}

// Close releases the underlying file handle.
func (w *WAL) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// Tail returns up to n most recent events, newest last.
// Reading is lock-free with respect to writes — we open a fresh read handle.
func (w *WAL) Tail(n int) ([]WALEvent, error) {
	if n <= 0 {
		n = 100
	}
	f, err := os.Open(w.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	// Read all lines (WAL files are expected to stay small; daily rotation
	// is a future concern).
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var all []WALEvent
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev WALEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue // skip corrupt line, don't fail the whole tail
		}
		all = append(all, ev)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(all) <= n {
		return all, nil
	}
	return all[len(all)-n:], nil
}

// NewIntent constructs a "INTENT" event — recorded BEFORE an action.
func NewIntent(taskID, workerID, message string) WALEvent {
	return WALEvent{Type: "intent", TaskID: taskID, WorkerID: workerID, Message: message}
}

// NewDone constructs a "DONE" event with proof citations. evidence keys
// should be one of: file_line, commit, pr, gh_url, cloud_run_rev, curl_probe.
func NewDone(taskID, workerID, message string, evidence map[string]string) WALEvent {
	return WALEvent{Type: "done", TaskID: taskID, WorkerID: workerID, Message: message, Evidence: evidence}
}

// NewBlocked constructs a "BLOCKED" event explaining why work cannot proceed.
func NewBlocked(taskID, workerID, reason string) WALEvent {
	return WALEvent{Type: "blocked", TaskID: taskID, WorkerID: workerID, Message: reason}
}
