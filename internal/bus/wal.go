package bus

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// walDelayedRecord is the on-disk format for delayed event entries.
type walDelayedRecord struct {
	Action string        `json:"action"` // "schedule" or "cancel"
	Entry  *delayedEntry `json:"entry,omitempty"`
	ID     string        `json:"id,omitempty"` // for cancel actions
}

// WAL is an append-only write-ahead log storing events as NDJSON.
type WAL struct {
	mu          sync.Mutex
	dir         string
	file        *os.File
	delayedFile *os.File
	lastSeq     uint64
	index       map[string]Event // ID -> Event for causality lookups
}

// OpenWAL opens or creates a WAL in the given directory.
func OpenWAL(dir string) (*WAL, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("wal: mkdir: %w", err)
	}

	eventsPath := filepath.Join(dir, "events.log")
	f, err := os.OpenFile(eventsPath, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("wal: open events: %w", err)
	}

	delayedPath := filepath.Join(dir, "delayed.log")
	df, err := os.OpenFile(delayedPath, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("wal: open delayed: %w", err)
	}

	w := &WAL{
		dir:         dir,
		file:        f,
		delayedFile: df,
		index:       make(map[string]Event),
	}

	// Replay existing events to build index and find last sequence.
	if err := w.replayIndex(); err != nil {
		f.Close()
		df.Close()
		return nil, fmt.Errorf("wal: replay index: %w", err)
	}

	return w, nil
}

// replayIndex reads all existing events to build the in-memory index.
func (w *WAL) replayIndex() error {
	if _, err := w.file.Seek(0, 0); err != nil {
		return err
	}
	scanner := bufio.NewScanner(w.file)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var evt Event
		if err := json.Unmarshal(line, &evt); err != nil {
			continue // skip corrupt lines
		}
		w.index[evt.ID] = evt
		if evt.Sequence > w.lastSeq {
			w.lastSeq = evt.Sequence
		}
	}
	return scanner.Err()
}

// LastSeq returns the last sequence number in the WAL.
func (w *WAL) LastSeq() uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastSeq
}

// Append writes an event to the WAL. The write is synced before returning.
func (w *WAL) Append(evt Event) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	data, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("wal: marshal: %w", err)
	}
	data = append(data, '\n')

	if _, err := w.file.Write(data); err != nil {
		return fmt.Errorf("wal: write: %w", err)
	}
	if err := w.file.Sync(); err != nil {
		return fmt.Errorf("wal: sync: %w", err)
	}

	w.index[evt.ID] = evt
	if evt.Sequence > w.lastSeq {
		w.lastSeq = evt.Sequence
	}
	return nil
}

// FindByID returns an event by its ID, or an error if not found.
func (w *WAL) FindByID(id string) (Event, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	evt, ok := w.index[id]
	if !ok {
		return Event{}, fmt.Errorf("wal: event %s not found", id)
	}
	return evt, nil
}

// ReadFrom returns all events with sequence >= from, in order.
func (w *WAL) ReadFrom(from uint64) ([]Event, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if _, err := w.file.Seek(0, 0); err != nil {
		return nil, err
	}

	var result []Event
	scanner := bufio.NewScanner(w.file)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var evt Event
		if err := json.Unmarshal(line, &evt); err != nil {
			continue
		}
		if evt.Sequence >= from {
			result = append(result, evt)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// AppendDelayed persists a delayed event schedule entry.
func (w *WAL) AppendDelayed(entry *delayedEntry) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	rec := walDelayedRecord{
		Action: "schedule",
		Entry:  entry,
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	if _, err := w.delayedFile.Write(data); err != nil {
		return err
	}
	return w.delayedFile.Sync()
}

// AppendDelayedCancel persists a cancellation of a delayed event.
func (w *WAL) AppendDelayedCancel(id string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	rec := walDelayedRecord{
		Action: "cancel",
		ID:     id,
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	if _, err := w.delayedFile.Write(data); err != nil {
		return err
	}
	return w.delayedFile.Sync()
}

// ReadDelayed reads and replays the delayed log, returning only entries
// that are still pending (scheduled but not cancelled and not yet fired).
func (w *WAL) ReadDelayed() ([]*delayedEntry, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if _, err := w.delayedFile.Seek(0, 0); err != nil {
		return nil, err
	}

	pending := make(map[string]*delayedEntry)
	scanner := bufio.NewScanner(w.delayedFile)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec walDelayedRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		switch rec.Action {
		case "schedule":
			if rec.Entry != nil {
				pending[rec.Entry.ID] = rec.Entry
			}
		case "cancel":
			delete(pending, rec.ID)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	result := make([]*delayedEntry, 0, len(pending))
	for _, e := range pending {
		result = append(result, e)
	}
	return result, nil
}

// Close closes the WAL files.
func (w *WAL) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	var firstErr error
	if err := w.file.Close(); err != nil {
		// First close — firstErr is always nil here; capture
		// unconditionally.
		firstErr = err
	}
	if err := w.delayedFile.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}
