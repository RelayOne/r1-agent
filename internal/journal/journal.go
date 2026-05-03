// Package journal implements the per-session append-only event log
// for the r1d-server multi-session daemon (specs/r1d-server.md Phase D
// item 23).
//
// # Format
//
// JSON-lines, one record per line:
//
//	{"v":1,"seq":N,"type":"event","kind":"hub.event","data":{...},"ts":"2026-05-02T10:11:12Z"}
//
// The "v" field is the schema version; readers refuse to consume any
// line where v != 1 so future format changes can land additively.
//
//   - seq is per-journal monotonic, starts at 1.
//   - type is the wire kind (always "event" today; reserved for
//     future kinds like "checkpoint", "marker").
//   - kind is the application-level subtype the writer uses to
//     classify the event for fsync semantics — see Writer.Append.
//   - data is the opaque per-event payload (anything json-marshalable).
//   - ts is RFC3339Nano UTC.
//
// # fsync policy
//
// Every Append fsyncs the file when the kind matches one of the
// terminal kinds set on the Writer (defaults: "session.dispose",
// "tool.post_use", "task.completed", "task.failed"). Non-terminal
// events trade durability for throughput — the OS page cache absorbs
// them and a daemon crash loses at most a few non-terminal events.
// Terminal events MUST survive a crash because replay rebuilds the
// session from them.
//
// # Crash recovery
//
// Reader validates each line as v:1 JSON and stops at the first
// invalid line (truncated tail from a crash mid-write). Truncate
// rewrites the file as the prefix up to and including a given seq —
// callers use it to drop a corrupted tail before resuming writes.
//
// # File layout
//
//   <dir>/<session-id>.jsonl
//
// One file per session; the daemon's startup glue picks the dir
// (typically `~/.r1/sessions/`).
package journal

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Version is the wire-format version. Readers reject any line whose
// v field != Version.
const Version = 1

// DefaultTerminalKinds is the kind list the Writer fsyncs by default.
// Append always fsyncs records with these kinds; non-terminal kinds
// rely on the OS page cache.
//
// The list is lifted from spec §11.23 — the four kinds the daemon
// MUST persist before the writer returns:
//
//   - session.dispose: end-of-session marker; replay needs it
//   - tool.post_use: tool effect already happened on disk
//   - task.completed/task.failed: causal anchors for cross-session
//     coordination
//
// Tests can override this via WriterOptions.TerminalKinds.
var DefaultTerminalKinds = []string{
	"session.dispose",
	"tool.post_use",
	"task.completed",
	"task.failed",
}

// Record is the on-wire shape of one journal line.
type Record struct {
	V    int             `json:"v"`
	Seq  uint64          `json:"seq"`
	Type string          `json:"type"`
	Kind string          `json:"kind"`
	TS   string          `json:"ts"`
	Data json.RawMessage `json:"data,omitempty"`
}

// Writer appends records to a journal file. Concurrency-safe: a single
// Writer is shared by the session's hot path; the mutex serialises
// Append calls because the file's monotonic seq counter MUST stay
// race-free.
type Writer struct {
	mu             sync.Mutex
	f              *os.File
	bw             *bufio.Writer
	seq            uint64
	terminalKinds  map[string]struct{}
	now            func() time.Time
	closed         bool
}

// WriterOptions configures Writer construction.
type WriterOptions struct {
	// TerminalKinds, when non-nil, replaces DefaultTerminalKinds. An
	// EMPTY slice means "no kinds are terminal" — useful for tests
	// that want to assert non-fsync paths in isolation.
	TerminalKinds []string

	// NowFn, when non-nil, replaces time.Now (for deterministic test
	// timestamps). Production callers leave this nil.
	NowFn func() time.Time
}

// OpenWriter opens (or creates) the journal file at path for append.
// Existing content is preserved; the writer's seq is initialised
// from the highest seq seen in the file (so a daemon restart resumes
// without colliding seqs). Returns an error if the file is corrupted
// past the read tolerance — callers should run Truncate to drop the
// tail and retry.
func OpenWriter(path string, opts WriterOptions) (*Writer, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("journal: mkdir %s: %w", dir, err)
		}
	}
	// First, scan the existing file to learn the last seq. We do this
	// with a separate read-only handle so a partial last line doesn't
	// affect the writer's state.
	last, err := readLastSeq(path)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("journal: open %s: %w", path, err)
	}
	w := &Writer{
		f:             f,
		bw:            bufio.NewWriter(f),
		seq:           last,
		terminalKinds: makeKindSet(opts.TerminalKinds),
		now:           opts.NowFn,
	}
	if w.now == nil {
		w.now = time.Now
	}
	return w, nil
}

// makeKindSet builds a lookup set from a slice. nil slice -> use
// DefaultTerminalKinds; empty slice -> no terminal kinds.
func makeKindSet(kinds []string) map[string]struct{} {
	if kinds == nil {
		kinds = DefaultTerminalKinds
	}
	m := make(map[string]struct{}, len(kinds))
	for _, k := range kinds {
		m[k] = struct{}{}
	}
	return m
}

// Append writes one record. data is JSON-marshaled (use any concrete
// type or json.RawMessage). If the kind matches the writer's terminal
// kind set, the file is flushed AND fsynced before Append returns.
// Otherwise the buffered writer absorbs the bytes.
//
// Returns the assigned seq number for callers that want to record it.
func (w *Writer) Append(kind string, data any) (uint64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, errors.New("journal: writer closed")
	}
	w.seq++
	rawData, err := json.Marshal(data)
	if err != nil {
		w.seq-- // roll back the bump on marshal failure
		return 0, fmt.Errorf("journal: marshal data: %w", err)
	}
	rec := Record{
		V:    Version,
		Seq:  w.seq,
		Type: "event",
		Kind: kind,
		TS:   w.now().UTC().Format(time.RFC3339Nano),
		Data: rawData,
	}
	line, err := json.Marshal(&rec)
	if err != nil {
		w.seq--
		return 0, fmt.Errorf("journal: marshal record: %w", err)
	}
	if _, err := w.bw.Write(line); err != nil {
		w.seq--
		return 0, fmt.Errorf("journal: write: %w", err)
	}
	if err := w.bw.WriteByte('\n'); err != nil {
		w.seq--
		return 0, fmt.Errorf("journal: write newline: %w", err)
	}
	if w.isTerminal(kind) {
		if err := w.flushAndSync(); err != nil {
			return 0, err
		}
	}
	return rec.Seq, nil
}

// Flush forces buffered bytes out to the OS page cache. Does NOT
// fsync — call Sync for that. Useful for graceful shutdown paths
// that want bytes written without waiting for disk.
func (w *Writer) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.bw.Flush()
}

// Sync flushes AND fsyncs the underlying file. Called automatically
// on terminal kinds; exposed for tests and explicit checkpoints.
func (w *Writer) Sync() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.flushAndSync()
}

// flushAndSync is the lock-already-held variant.
func (w *Writer) flushAndSync() error {
	if err := w.bw.Flush(); err != nil {
		return fmt.Errorf("journal: flush: %w", err)
	}
	if err := w.f.Sync(); err != nil {
		return fmt.Errorf("journal: fsync: %w", err)
	}
	return nil
}

// Close flushes, fsyncs, and closes the underlying file. After Close,
// further Append calls return an error.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	flushErr := w.bw.Flush()
	syncErr := w.f.Sync()
	closeErr := w.f.Close()
	if flushErr != nil {
		return fmt.Errorf("journal: close-flush: %w", flushErr)
	}
	if syncErr != nil {
		return fmt.Errorf("journal: close-sync: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("journal: close: %w", closeErr)
	}
	return nil
}

// LastSeq returns the highest seq written. Useful for resume bookkeeping.
func (w *Writer) LastSeq() uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.seq
}

// isTerminal reports whether the kind triggers fsync.
func (w *Writer) isTerminal(kind string) bool {
	_, ok := w.terminalKinds[kind]
	return ok
}

// Reader reads records from a journal file, validating v:1 on each
// line. Stop at first malformed line (treated as a corrupted tail);
// the prefix is reported to the handler.
type Reader struct {
	path string
}

// OpenReader returns a Reader for the given file. Lazy — no I/O
// happens until Replay is called.
func OpenReader(path string) *Reader {
	return &Reader{path: path}
}

// HandlerFunc is the per-record callback for Replay. Returning a
// non-nil error stops the replay; the error is surfaced to the
// caller. Returning nil continues to the next record.
type HandlerFunc func(r Record) error

// Replay reads the journal from the start and invokes handler for
// each valid record. Stops cleanly at:
//
//   - end-of-file (returns nil)
//   - first malformed line (returns ErrCorruptTail wrapping the byte
//     offset; the caller can Truncate at the last good seq and resume)
//   - first record with v != Version (same handling — strict)
//
// The handler error short-circuits.
func (r *Reader) Replay(handler HandlerFunc) error {
	if handler == nil {
		return errors.New("journal: nil handler")
	}
	f, err := os.Open(r.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// An absent journal is "empty" — no records to replay.
			return nil
		}
		return fmt.Errorf("journal: open %s: %w", r.path, err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	// Bump the scanner buffer — record payloads can be large (tool
	// outputs), and the default 64KB cap will choke on a single big
	// hub.Event with model output attached.
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var lineNo int
	for scanner.Scan() {
		lineNo++
		raw := scanner.Bytes()
		if len(raw) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(raw, &rec); err != nil {
			return fmt.Errorf("%w: line %d: %v", ErrCorruptTail, lineNo, err)
		}
		if rec.V != Version {
			return fmt.Errorf("%w: line %d: v=%d want %d", ErrCorruptTail, lineNo, rec.V, Version)
		}
		if err := handler(rec); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		// io.ErrShortBuffer / unexpected EOF on the last line is the
		// classic crashed-mid-write signal — surface as ErrCorruptTail.
		return fmt.Errorf("%w: scan: %v", ErrCorruptTail, err)
	}
	return nil
}

// ErrCorruptTail is returned by Replay when the journal has a
// malformed line. Callers can `errors.Is` and run Truncate to drop
// the bad tail, then resume.
var ErrCorruptTail = errors.New("journal: corrupt tail")

// Truncate rewrites the journal file in place, keeping records
// whose seq is <= atSeq. The original file is renamed to a `.bad`
// backup so an operator can inspect what was dropped, then the
// truncated copy is renamed atomically into place.
//
// After Truncate, callers should call OpenWriter again to re-attach
// the writer; an existing Writer's seq counter is now stale.
//
// Returns the number of records kept and the number dropped (which
// includes a corrupt last-line, hence atSeq+1 might not equal
// "kept count" exactly).
func Truncate(path string, atSeq uint64) (kept, dropped int, err error) {
	src, err := os.Open(path)
	if err != nil {
		return 0, 0, fmt.Errorf("journal: open %s for truncate: %w", path, err)
	}
	defer func() { _ = src.Close() }()

	tmp := path + ".trunc"
	dst, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, 0, fmt.Errorf("journal: open %s: %w", tmp, err)
	}
	bw := bufio.NewWriter(dst)

	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		raw := scanner.Bytes()
		if len(raw) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(raw, &rec); err != nil {
			// Treat malformed lines as the corrupted tail — drop and
			// stop the copy. We do NOT try to recover individual
			// records past the first corruption.
			dropped++
			break
		}
		if rec.V != Version {
			dropped++
			break
		}
		if rec.Seq > atSeq {
			dropped++
			continue
		}
		if _, werr := bw.Write(raw); werr != nil {
			_ = dst.Close()
			_ = os.Remove(tmp)
			return kept, dropped, fmt.Errorf("journal: write tmp: %w", werr)
		}
		if werr := bw.WriteByte('\n'); werr != nil {
			_ = dst.Close()
			_ = os.Remove(tmp)
			return kept, dropped, fmt.Errorf("journal: write tmp newline: %w", werr)
		}
		kept++
	}
	if ferr := bw.Flush(); ferr != nil {
		_ = dst.Close()
		_ = os.Remove(tmp)
		return kept, dropped, fmt.Errorf("journal: flush tmp: %w", ferr)
	}
	if serr := dst.Sync(); serr != nil {
		_ = dst.Close()
		_ = os.Remove(tmp)
		return kept, dropped, fmt.Errorf("journal: sync tmp: %w", serr)
	}
	if cerr := dst.Close(); cerr != nil {
		_ = os.Remove(tmp)
		return kept, dropped, fmt.Errorf("journal: close tmp: %w", cerr)
	}
	// Rename original out of the way for forensic inspection. Best
	// effort — if it fails (Windows: file locked), proceed with the
	// rename onto path; the tmp will overwrite.
	bad := path + ".bad"
	_ = os.Rename(path, bad)
	if rerr := os.Rename(tmp, path); rerr != nil {
		// Try to put the original back if we moved it.
		_ = os.Rename(bad, path)
		_ = os.Remove(tmp)
		return kept, dropped, fmt.Errorf("journal: rename %s -> %s: %w", tmp, path, rerr)
	}
	return kept, dropped, nil
}

// readLastSeq scans an existing file and returns the highest valid
// seq. Stops at first corruption (the writer can resume from there
// after Truncate). Returns 0 if the file does not exist or is empty.
func readLastSeq(path string) (uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("journal: scan-open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var last uint64
	for scanner.Scan() {
		raw := scanner.Bytes()
		if len(raw) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(raw, &rec); err != nil {
			// Corrupt tail — stop here. The writer must Truncate
			// before appending; we do NOT silently drop seqs.
			return last, fmt.Errorf("%w: scan tail: %v", ErrCorruptTail, err)
		}
		if rec.V != Version {
			return last, fmt.Errorf("%w: v=%d at seq %d", ErrCorruptTail, rec.V, rec.Seq)
		}
		if rec.Seq > last {
			last = rec.Seq
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return last, fmt.Errorf("%w: scan EOF: %v", ErrCorruptTail, err)
		}
		return last, fmt.Errorf("journal: scan: %w", err)
	}
	return last, nil
}
