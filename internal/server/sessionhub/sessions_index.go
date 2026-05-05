// Package sessionhub: sessions-index.json append-only registry.
//
// =============================================================================
// What this is
// =============================================================================
//
// `~/.r1/sessions-index.json` is the daemon-restart bookkeeping file. It
// records every session the daemon ever Create'd, plus a deleted flag
// for sessions that were Delete'd. On daemon restart (TASK-27) the
// file is scanned, every non-deleted session's journal is reopened,
// and the session is reinstated as `paused-reattachable` so a
// reconnecting client sees its state replayed.
//
// =============================================================================
// Why append-only
// =============================================================================
//
// Sessions are long-lived; the index can grow large but ordering
// matters for deterministic replay. Append-only means a daemon crash
// mid-write never corrupts older entries — the worst case is a
// truncated tail, which the JSON unmarshal stops at cleanly. Mark-
// deleted (rather than physical delete) keeps the wire format small
// and means the order entries arrive in the file matches the order
// sessions were created.
//
// =============================================================================
// Atomic write contract
// =============================================================================
//
// Every state change writes the whole file atomically:
//
//   1. Marshal the entire IndexFile struct to JSON.
//   2. Write to `<path>.tmp` with mode 0600.
//   3. fsync the tmp file.
//   4. Rename tmp → final (same-directory rename is atomic on every
//      supported OS).
//   5. fsync the parent directory so the rename survives a crash.
//
// We ALWAYS rewrite the whole file, not append in-place — atomic
// rename is cheaper than guaranteeing a true append-only fsync
// pattern works across the Linux/macOS/Windows triple. The "append-
// only" property is preserved at the API level (Append, MarkDeleted)
// even though the file is fully rewritten each time.
//
// =============================================================================
package sessionhub

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// IndexFileName is the basename under ~/.r1/.
const IndexFileName = "sessions-index.json"

// IndexEntry is one record in the file. Order in IndexFile.Sessions is
// the order entries were Append'd; later entries with the same ID are
// not produced (Append refuses to register a duplicate id), so the
// list reads naturally as session-creation history.
type IndexEntry struct {
	// ID is the per-daemon session id (matches Session.ID).
	ID string `json:"id"`
	// Workdir is the absolute, validated workdir.
	Workdir string `json:"workdir"`
	// StartedAt is the wall-clock create time, RFC3339.
	StartedAt string `json:"started_at"`
	// JournalPath is the absolute path of the per-session journal
	// file (typically `~/.r1/sessions/<id>.jsonl`). Set on Append;
	// the daemon's startup glue reuses this on TASK-27 replay.
	JournalPath string `json:"journal_path"`
	// Model is the provider model id at session create time. Stored
	// so a replayed session can reattach with the same model
	// configuration without consulting external state.
	Model string `json:"model,omitempty"`
	// Deleted is true iff MarkDeleted has been called for this id.
	// We KEEP the entry on Delete so the audit trail survives. The
	// daemon's startup glue skips deleted entries during replay.
	Deleted bool `json:"deleted,omitempty"`
	// DeletedAt records when MarkDeleted fired (RFC3339). Only set
	// when Deleted is true.
	DeletedAt string `json:"deleted_at,omitempty"`
}

// IndexFile is the on-disk shape.
type IndexFile struct {
	// V is the schema version. v=1 today; future readers can
	// downgrade or migrate based on this field.
	V int `json:"v"`
	// Sessions is the append-ordered list of entries.
	Sessions []IndexEntry `json:"sessions"`
}

// SessionsIndex manages `~/.r1/sessions-index.json`. Concurrency-safe.
// One instance per daemon (singleton pattern); the SessionHub holds a
// reference and calls Append/MarkDeleted from Create/Delete.
type SessionsIndex struct {
	mu   sync.Mutex
	path string
	now  func() time.Time
}

// NewSessionsIndex constructs an index manager rooted at `~/.r1/`
// (or the test override under R1_HOME). The file does not have to
// exist; Append will create it on first call.
func NewSessionsIndex() (*SessionsIndex, error) {
	dir, err := resolveR1Dir()
	if err != nil {
		return nil, fmt.Errorf("sessions-index: resolve r1 dir: %w", err)
	}
	return NewSessionsIndexAt(filepath.Join(dir, IndexFileName))
}

// NewSessionsIndexAt constructs an index manager at an explicit path.
// Used by tests and by callers (e.g. the daemon shutdown reporter)
// that need to read from a non-default location.
func NewSessionsIndexAt(path string) (*SessionsIndex, error) {
	if path == "" {
		return nil, errors.New("sessions-index: empty path")
	}
	return &SessionsIndex{path: path, now: time.Now}, nil
}

// Path returns the absolute path of the index file. Stable across
// the lifetime of the SessionsIndex.
func (si *SessionsIndex) Path() string { return si.path }

// Load reads the index file and returns its parsed contents. If the
// file does not exist, returns an empty IndexFile (V=1) with no error
// — the daemon's startup glue treats "no index" as "no sessions to
// resume", which is the correct first-run behaviour.
func (si *SessionsIndex) Load() (*IndexFile, error) {
	si.mu.Lock()
	defer si.mu.Unlock()
	return si.loadLocked()
}

// loadLocked is the lock-already-held variant.
func (si *SessionsIndex) loadLocked() (*IndexFile, error) {
	body, err := os.ReadFile(si.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &IndexFile{V: 1}, nil
		}
		return nil, fmt.Errorf("sessions-index: read %s: %w", si.path, err)
	}
	var f IndexFile
	if uerr := json.Unmarshal(body, &f); uerr != nil {
		return nil, fmt.Errorf("sessions-index: parse %s: %w", si.path, uerr)
	}
	if f.V == 0 {
		f.V = 1 // tolerate legacy files written before v stamping
	}
	return &f, nil
}

// Append adds a new session entry. Idempotent at the (id+deleted=false)
// key: a second Append for the same id returns an error rather than
// silently rewriting; callers should consult Load first if they need
// idempotent behaviour.
//
// On success, the file is rewritten atomically (tmp+rename+fsync
// parent). The IndexEntry's StartedAt is filled with the current time
// if zero.
func (si *SessionsIndex) Append(entry IndexEntry) error {
	if entry.ID == "" {
		return errors.New("sessions-index: empty entry.ID")
	}
	if entry.Workdir == "" {
		return errors.New("sessions-index: empty entry.Workdir")
	}
	if entry.JournalPath == "" {
		return errors.New("sessions-index: empty entry.JournalPath")
	}
	si.mu.Lock()
	defer si.mu.Unlock()
	f, err := si.loadLocked()
	if err != nil {
		return err
	}
	// Reject a duplicate id on the not-deleted axis. A re-Append after
	// MarkDeleted is also disallowed — the spec's append-only rule
	// means a session id is one-shot. Callers that want a new session
	// must mint a new id.
	for _, e := range f.Sessions {
		if e.ID == entry.ID {
			return fmt.Errorf("sessions-index: id %q already in index (deleted=%v)", entry.ID, e.Deleted)
		}
	}
	if entry.StartedAt == "" {
		entry.StartedAt = si.now().UTC().Format(time.RFC3339Nano)
	}
	f.Sessions = append(f.Sessions, entry)
	return si.writeAtomic(f)
}

// MarkDeleted flips the Deleted flag (and DeletedAt timestamp) on the
// entry with the given id. Returns an error if the id is unknown OR
// already marked. Idempotent only insofar as a caller can safely
// retry on a transient I/O error — a successful re-mark is rejected.
func (si *SessionsIndex) MarkDeleted(id string) error {
	if id == "" {
		return errors.New("sessions-index: empty id")
	}
	si.mu.Lock()
	defer si.mu.Unlock()
	f, err := si.loadLocked()
	if err != nil {
		return err
	}
	idx := -1
	for i, e := range f.Sessions {
		if e.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("sessions-index: id %q not found", id)
	}
	if f.Sessions[idx].Deleted {
		return fmt.Errorf("sessions-index: id %q already deleted", id)
	}
	f.Sessions[idx].Deleted = true
	f.Sessions[idx].DeletedAt = si.now().UTC().Format(time.RFC3339Nano)
	return si.writeAtomic(f)
}

// writeAtomic rewrites the index file via tmp+rename. Parent dir is
// fsynced so the rename survives a crash. The lock MUST be held.
func (si *SessionsIndex) writeAtomic(f *IndexFile) error {
	if dir := filepath.Dir(si.path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("sessions-index: mkdir %s: %w", dir, err)
		}
	}
	body, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("sessions-index: marshal: %w", err)
	}
	tmp := si.path + ".tmp"
	tf, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("sessions-index: open tmp: %w", err)
	}
	if _, werr := tf.Write(body); werr != nil {
		_ = tf.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("sessions-index: write tmp: %w", werr)
	}
	if serr := tf.Sync(); serr != nil {
		_ = tf.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("sessions-index: sync tmp: %w", serr)
	}
	if cerr := tf.Close(); cerr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("sessions-index: close tmp: %w", cerr)
	}
	if rerr := os.Rename(tmp, si.path); rerr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("sessions-index: rename: %w", rerr)
	}
	// fsync parent so the rename survives a crash.
	if dir := filepath.Dir(si.path); dir != "" && dir != "." {
		if df, derr := os.Open(dir); derr == nil {
			_ = df.Sync()
			_ = df.Close()
		}
		// fsync on a directory is a no-op on some platforms (Windows);
		// we ignore errors silently rather than fail the write.
	}
	return nil
}
