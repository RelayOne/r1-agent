// Package checkpoint — timeline.go
//
// Deterministic checkpoint timeline for SOW execution. An
// append-only WAL of labeled checkpoints at every decision
// point, each carrying enough state to resume from that
// exact position after a code fix + binary rebuild.
//
// Design goals:
//   - Append-only: never mutate history. Resume is a
//     "re-enter at checkpoint X" operation, not a rewrite.
//   - Parallel-safe: each goroutine can write concurrently;
//     the WAL is mutex-protected with fsync-on-write.
//   - Deterministic resume: given a checkpoint ID, the
//     scheduler can reconstruct session-marker state +
//     git SHA + cost budget and re-enter at that point.
//   - Human-readable: one JSON object per line in the WAL
//     so operators can `tail -f` or `jq` the file.
//
// Checkpoint labels (auto-emitted at these boundaries):
//
//   sow-converted           — SOW planning complete, before dispatch
//   session-start:<SID>     — about to dispatch session SID
//   session-done:<SID>      — session SID completed/failed
//   ac-attempt:<SID>:<N>    — about to run AC check attempt N for SID
//   refine:<round>          — about to run refine round N
//   task-start:<SID>:<TID>  — about to dispatch task TID in session SID
//
// The timeline lives at <repo>/.stoke/checkpoints/timeline.jsonl.
// Each line is a TimelineEntry (JSON). Snapshots are self-contained:
// they carry the full set of completed session IDs + the git SHA so
// resume doesn't need to parse the entire WAL to reconstruct state.
package checkpoint

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// TimelineEntry is one checkpoint in the append-only WAL.
type TimelineEntry struct {
	// ID is a monotonic counter within this run: CP-001, CP-002, ...
	// Unique per WAL file (each `--fresh` run starts a new WAL).
	ID string `json:"id"`

	// Label is the human-readable checkpoint name. Format:
	// "<category>:<detail>" (e.g., "session-start:S2",
	// "ac-attempt:S1:3", "refine:round-2").
	Label string `json:"label"`

	// Timestamp is when the checkpoint was written.
	Timestamp time.Time `json:"timestamp"`

	// GitSHA is HEAD at checkpoint time. Resume restores
	// the worktree to this SHA before re-entering.
	GitSHA string `json:"git_sha,omitempty"`

	// CompletedSessions is the set of session IDs with
	// completion markers on disk at checkpoint time. Resume
	// skips these and dispatches everything else.
	CompletedSessions []string `json:"completed_sessions"`

	// CostUSD is the cumulative run cost at checkpoint time.
	CostUSD float64 `json:"cost_usd"`

	// TasksCompleted is the count of ✓-tasks at this point.
	TasksCompleted int `json:"tasks_completed"`

	// SessionID is the active session (if this checkpoint is
	// session-scoped). Empty for run-level checkpoints like
	// "sow-converted" or "refine:round-N".
	SessionID string `json:"session_id,omitempty"`

	// Metadata carries checkpoint-specific extras (e.g.,
	// AC pass count, fidelity score, worker ID for parallel).
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Timeline is the append-only WAL writer. Thread-safe.
type Timeline struct {
	mu      sync.Mutex
	path    string
	file    *os.File
	seq     int // monotonic counter
	runID   string
}

// NewTimeline opens (or creates) a timeline WAL at
// <repoRoot>/.stoke/checkpoints/timeline.jsonl.
// runID is a per-run unique (e.g., ULID or timestamp)
// that disambiguates entries across runs sharing the
// same file.
func NewTimeline(repoRoot, runID string) (*Timeline, error) {
	dir := filepath.Join(repoRoot, ".stoke", "checkpoints")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("checkpoint: mkdir: %w", err)
	}
	p := filepath.Join(dir, "timeline.jsonl")
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("checkpoint: open WAL: %w", err)
	}
	// Count existing entries to continue the sequence.
	existing, _ := os.ReadFile(p)
	seq := 0
	for _, b := range existing {
		if b == '\n' {
			seq++
		}
	}
	return &Timeline{path: p, file: f, seq: seq, runID: runID}, nil
}

// Checkpoint writes one entry to the WAL. Returns the
// assigned checkpoint ID (e.g., "CP-042").
func (t *Timeline) Checkpoint(label string, gitSHA string, completedSessions []string, costUSD float64, tasksCompleted int, sessionID string, metadata map[string]any) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seq++
	id := fmt.Sprintf("CP-%03d", t.seq)
	entry := TimelineEntry{
		ID:                id,
		Label:             label,
		Timestamp:         time.Now().UTC(),
		GitSHA:            gitSHA,
		CompletedSessions: completedSessions,
		CostUSD:           costUSD,
		TasksCompleted:    tasksCompleted,
		SessionID:         sessionID,
		Metadata:          metadata,
	}
	b, err := json.Marshal(entry)
	if err != nil {
		return "", fmt.Errorf("checkpoint: marshal: %w", err)
	}
	b = append(b, '\n')
	if _, err := t.file.Write(b); err != nil {
		return "", fmt.Errorf("checkpoint: write WAL: %w", err)
	}
	// fsync so a crash doesn't lose the last checkpoint.
	_ = t.file.Sync()
	return id, nil
}

// Close flushes and closes the WAL file.
func (t *Timeline) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.file != nil {
		return t.file.Close()
	}
	return nil
}

// ListCheckpoints reads the WAL and returns all entries.
// Used by `stoke sow --list-checkpoints` and by the
// resume logic to find the target checkpoint.
func ListCheckpoints(repoRoot string) ([]TimelineEntry, error) {
	p := filepath.Join(repoRoot, ".stoke", "checkpoints", "timeline.jsonl")
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("checkpoint: read WAL: %w", err)
	}
	lines := splitLines(data)
	entries := make([]TimelineEntry, 0, len(lines))
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		var e TimelineEntry
		if err := json.Unmarshal(line, &e); err != nil {
			continue // skip malformed lines
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// FindCheckpoint looks up a specific checkpoint by ID.
// Returns nil when not found.
func FindCheckpoint(repoRoot, id string) (*TimelineEntry, error) {
	entries, err := ListCheckpoints(repoRoot)
	if err != nil {
		return nil, err
	}
	for i := range entries {
		if entries[i].ID == id {
			return &entries[i], nil
		}
	}
	return nil, nil
}

// splitLines splits data on '\n' without allocating strings
// for the stdlib's Split overhead.
func splitLines(data []byte) [][]byte {
	var lines [][]byte
	for len(data) > 0 {
		nl := -1
		for i, b := range data {
			if b == '\n' {
				nl = i
				break
			}
		}
		if nl < 0 {
			if len(data) > 0 {
				lines = append(lines, data)
			}
			break
		}
		lines = append(lines, data[:nl])
		data = data[nl+1:]
	}
	return lines
}
