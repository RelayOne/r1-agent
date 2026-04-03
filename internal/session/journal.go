// Package session - journal.go implements JSONL incremental session persistence.
// Inspired by claw-code-parity's append-only message log: each event is appended
// as a single JSON line, allowing crash recovery without rewriting the entire state.
//
// Key design: append-only writes are atomic at the OS level (single write < PIPE_BUF),
// so we get crash-safe persistence without fsync or WAL. On rotation (context compaction),
// we snapshot the current state and start a new segment.
package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// EventType classifies journal entries for replay filtering.
type EventType string

const (
	EventMessage     EventType = "message"
	EventToolUse     EventType = "tool_use"
	EventToolResult  EventType = "tool_result"
	EventCompaction  EventType = "compaction"
	EventCheckpoint  EventType = "checkpoint"
	EventCostUpdate  EventType = "cost_update"
	EventTaskStart   EventType = "task_start"
	EventTaskEnd     EventType = "task_end"
)

// JournalEntry is a single append-only log entry.
type JournalEntry struct {
	Seq       int64          `json:"seq"`
	Timestamp time.Time      `json:"ts"`
	Type      EventType      `json:"type"`
	SessionID string         `json:"session_id"`
	TaskID    string         `json:"task_id,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
	Text      string         `json:"text,omitempty"`
	CostUSD   float64        `json:"cost_usd,omitempty"`
}

// Journal provides append-only JSONL persistence for session events.
// Thread-safe. Each write is a single line append (atomic for < PIPE_BUF).
type Journal struct {
	mu        sync.Mutex
	file      *os.File
	path      string
	seq       int64
	sessionID string
	segment   int // incremented on rotation
}

// NewJournal creates or opens a journal file for append-only writing.
func NewJournal(dir, sessionID string) (*Journal, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create journal dir: %w", err)
	}

	path := filepath.Join(dir, sessionID+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("open journal: %w", err)
	}

	// Count existing entries to set seq
	seq := countLines(path)

	return &Journal{
		file:      f,
		path:      path,
		seq:       seq,
		sessionID: sessionID,
	}, nil
}

// Append writes a single event to the journal.
func (j *Journal) Append(entry JournalEntry) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	j.seq++
	entry.Seq = j.seq
	entry.Timestamp = time.Now()
	entry.SessionID = j.sessionID

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal journal entry: %w", err)
	}

	// Append newline-terminated JSON (atomic for small writes)
	data = append(data, '\n')
	_, err = j.file.Write(data)
	return err
}

// AppendMessage is a convenience for logging a message event.
func (j *Journal) AppendMessage(role, text string) error {
	return j.Append(JournalEntry{
		Type: EventMessage,
		Data: map[string]any{"role": role},
		Text: text,
	})
}

// AppendToolUse logs a tool invocation.
func (j *Journal) AppendToolUse(toolName, toolID string, input map[string]any) error {
	data := map[string]any{"tool": toolName, "id": toolID}
	for k, v := range input {
		data[k] = v
	}
	return j.Append(JournalEntry{
		Type: EventToolUse,
		Data: data,
	})
}

// AppendToolResult logs a tool result.
func (j *Journal) AppendToolResult(toolID, result string, isError bool) error {
	return j.Append(JournalEntry{
		Type: EventToolResult,
		Data: map[string]any{"id": toolID, "is_error": isError},
		Text: result,
	})
}

// AppendCost logs a cost update.
func (j *Journal) AppendCost(taskID string, costUSD float64) error {
	return j.Append(JournalEntry{
		Type:    EventCostUpdate,
		TaskID:  taskID,
		CostUSD: costUSD,
	})
}

// Rotate snapshots the current journal and starts a new segment.
// Used during context compaction to keep the active journal small.
func (j *Journal) Rotate() error {
	j.mu.Lock()
	defer j.mu.Unlock()

	// Write compaction marker
	marker := JournalEntry{
		Seq:       j.seq + 1,
		Timestamp: time.Now(),
		Type:      EventCompaction,
		SessionID: j.sessionID,
		Data:      map[string]any{"segment": j.segment, "entries_before": j.seq},
	}
	data, _ := json.Marshal(marker)
	data = append(data, '\n')
	j.file.Write(data)

	j.file.Close()

	// Rename current to segment file
	j.segment++
	segPath := fmt.Sprintf("%s.%d", j.path, j.segment-1)
	os.Rename(j.path, segPath)

	// Open new journal
	f, err := os.OpenFile(j.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open new journal segment: %w", err)
	}
	j.file = f
	j.seq = 0
	return nil
}

// Replay reads all entries from a journal file.
func Replay(path string) ([]JournalEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var entries []JournalEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB max line
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry JournalEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue // skip corrupt lines
		}
		entries = append(entries, entry)
	}
	return entries, scanner.Err()
}

// ReplayFiltered reads entries matching a type filter.
func ReplayFiltered(path string, types ...EventType) ([]JournalEntry, error) {
	all, err := Replay(path)
	if err != nil {
		return nil, err
	}
	typeSet := make(map[EventType]bool, len(types))
	for _, t := range types {
		typeSet[t] = true
	}
	var filtered []JournalEntry
	for _, e := range all {
		if typeSet[e.Type] {
			filtered = append(filtered, e)
		}
	}
	return filtered, nil
}

// Close closes the journal file.
func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.file != nil {
		return j.file.Close()
	}
	return nil
}

// Path returns the journal file path.
func (j *Journal) Path() string {
	return j.path
}

// Seq returns the current sequence number.
func (j *Journal) Seq() int64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.seq
}

func countLines(path string) int64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	var count int64
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		count++
	}
	return count
}
