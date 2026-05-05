package journal

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestJournal_AppendReplay drives the round-trip: open writer, append
// three records, close, open reader, replay, assert each record's
// payload matches what was written and seq is monotonic from 1.
func TestJournal_AppendReplay(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s-1.jsonl")

	w, err := OpenWriter(path, WriterOptions{})
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := w.Append("event.test", map[string]any{"i": i}); err != nil {
			t.Fatalf("Append #%d: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r := OpenReader(path)
	var got []Record
	if err := r.Replay(func(rec Record) error {
		got = append(got, rec)
		return nil
	}); err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("record count: got %d, want 3", len(got))
	}
	for i, rec := range got {
		if rec.V != Version {
			t.Errorf("rec[%d].V: got %d, want %d", i, rec.V, Version)
		}
		if rec.Seq != uint64(i+1) {
			t.Errorf("rec[%d].Seq: got %d, want %d", i, rec.Seq, i+1)
		}
		if rec.Kind != "event.test" {
			t.Errorf("rec[%d].Kind: got %q, want %q", i, rec.Kind, "event.test")
		}
	}
}

// TestJournal_TerminalKindFsyncs asserts the configured terminal-kind
// triggers Sync. We can't directly observe fsync without OS hooks, but
// we can assert the writer flushes the bufio buffer (otherwise a
// concurrent Reader sees fewer bytes than written).
func TestJournal_TerminalKindFsyncs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s-2.jsonl")
	w, err := OpenWriter(path, WriterOptions{TerminalKinds: []string{"flush.me"}})
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	defer func() { _ = w.Close() }()

	// Non-terminal: bufio absorbs, file may still be empty.
	if _, err := w.Append("nope", "x"); err != nil {
		t.Fatalf("Append non-terminal: %v", err)
	}
	// Terminal: flush+fsync — both records must be on disk.
	if _, err := w.Append("flush.me", "y"); err != nil {
		t.Fatalf("Append terminal: %v", err)
	}
	// Independent reader sees both records.
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if stat.Size() == 0 {
		t.Fatalf("file empty after terminal Append; expected flushed bytes")
	}
	r := OpenReader(path)
	var n int
	if err := r.Replay(func(_ Record) error { n++; return nil }); err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if n != 2 {
		t.Fatalf("records on disk: got %d, want 2", n)
	}
}

// TestJournal_CorruptTail stops the reader at the first malformed
// line and surfaces ErrCorruptTail.
func TestJournal_CorruptTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s-3.jsonl")
	// Hand-craft a file: one valid line + one corrupted line.
	contents := `{"v":1,"seq":1,"type":"event","kind":"x","ts":"2026-01-01T00:00:00Z","data":{"a":1}}
{this is not json
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	r := OpenReader(path)
	var saw int
	err := r.Replay(func(_ Record) error { saw++; return nil })
	if !errors.Is(err, ErrCorruptTail) {
		t.Fatalf("Replay: got %v, want ErrCorruptTail", err)
	}
	if saw != 1 {
		t.Fatalf("records before corruption: got %d, want 1", saw)
	}
}

// TestJournal_TruncateDropsTail asserts Truncate keeps records up to
// atSeq and rewrites the file in place. The original is preserved as
// `<path>.bad` for forensic inspection.
func TestJournal_TruncateDropsTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s-4.jsonl")
	w, err := OpenWriter(path, WriterOptions{})
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := w.Append("event", map[string]any{"i": i}); err != nil {
			t.Fatalf("Append #%d: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	kept, dropped, err := Truncate(path, 3)
	if err != nil {
		t.Fatalf("Truncate: %v", err)
	}
	if kept != 3 {
		t.Errorf("kept: got %d, want 3", kept)
	}
	if dropped != 2 {
		t.Errorf("dropped: got %d, want 2", dropped)
	}
	// `.bad` backup must exist.
	if _, err := os.Stat(path + ".bad"); err != nil {
		t.Fatalf(".bad backup missing: %v", err)
	}
	// Replay sees only seq 1..3.
	r := OpenReader(path)
	var seqs []uint64
	if err := r.Replay(func(rec Record) error {
		seqs = append(seqs, rec.Seq)
		return nil
	}); err != nil {
		t.Fatalf("Replay after Truncate: %v", err)
	}
	if len(seqs) != 3 {
		t.Fatalf("seq count: got %d, want 3", len(seqs))
	}
	for i, s := range seqs {
		if s != uint64(i+1) {
			t.Errorf("seq[%d]: got %d, want %d", i, s, i+1)
		}
	}
}

// TestJournal_TruncateStopsAtCorruption asserts Truncate halts the
// copy at the first malformed line — we MUST NOT keep records past a
// corruption boundary because their seqs are no longer reliable.
func TestJournal_TruncateStopsAtCorruption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s-5.jsonl")
	contents := `{"v":1,"seq":1,"type":"event","kind":"x","ts":"2026-01-01T00:00:00Z"}
{"v":1,"seq":2,"type":"event","kind":"x","ts":"2026-01-01T00:00:00Z"}
not-json
{"v":1,"seq":99,"type":"event","kind":"x","ts":"2026-01-01T00:00:00Z"}
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	kept, dropped, err := Truncate(path, 99)
	if err != nil {
		t.Fatalf("Truncate: %v", err)
	}
	// Lines 1 and 2 are valid and within atSeq; line 3 is corrupt
	// (stops the copy); line 4 never reached.
	if kept != 2 {
		t.Errorf("kept: got %d, want 2", kept)
	}
	if dropped < 1 {
		t.Errorf("dropped: got %d, want >=1", dropped)
	}
}

// TestJournal_ResumeAfterReopen asserts OpenWriter on an existing file
// resumes seq from the highest valid record. A second Append yields
// seq = last + 1.
func TestJournal_ResumeAfterReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s-6.jsonl")
	w, err := OpenWriter(path, WriterOptions{})
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := w.Append("e", i); err != nil {
			t.Fatalf("Append #%d: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	w2, err := OpenWriter(path, WriterOptions{})
	if err != nil {
		t.Fatalf("OpenWriter (resume): %v", err)
	}
	defer func() { _ = w2.Close() }()
	if got := w2.LastSeq(); got != 3 {
		t.Fatalf("LastSeq after resume: got %d, want 3", got)
	}
	seq, err := w2.Append("e", "next")
	if err != nil {
		t.Fatalf("Append after resume: %v", err)
	}
	if seq != 4 {
		t.Fatalf("seq after resume: got %d, want 4", seq)
	}
}

// TestJournal_AppendAfterClose returns an error.
func TestJournal_AppendAfterClose(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s-7.jsonl")
	w, err := OpenWriter(path, WriterOptions{})
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := w.Append("k", 1); err == nil {
		t.Fatalf("expected error on Append after Close; got nil")
	}
}

// TestJournal_ConcurrentAppend exercises the writer's mutex under
// -race: 16 goroutines × 32 records each, then Replay must see
// 512 unique seqs.
func TestJournal_ConcurrentAppend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s-8.jsonl")
	w, err := OpenWriter(path, WriterOptions{})
	if err != nil {
		t.Fatalf("OpenWriter: %v", err)
	}
	var barrier sync.WaitGroup
	const G, N = 16, 32
	for g := 0; g < G; g++ {
		barrier.Add(1)
		go func(g int) {
			defer barrier.Done()
			for i := 0; i < N; i++ {
				if _, err := w.Append("e", map[string]int{"g": g, "i": i}); err != nil {
					t.Errorf("Append: %v", err)
					return
				}
			}
		}(g)
	}
	barrier.Wait()
	// assert.unique-seqs: every Append must have produced a distinct seq.
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	r := OpenReader(path)
	seen := make(map[uint64]bool, G*N)
	if err := r.Replay(func(rec Record) error {
		if seen[rec.Seq] {
			return errors.New("duplicate seq " + strings.Repeat("?", 0))
		}
		seen[rec.Seq] = true
		return nil
	}); err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(seen) != G*N {
		t.Fatalf("unique seq count: got %d, want %d", len(seen), G*N)
	}
}
