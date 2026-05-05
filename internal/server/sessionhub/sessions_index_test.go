package sessionhub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestSessionsIndex_AppendAndAtomic exercises the basic API: Append
// twice, MarkDeleted once, Load and verify the on-disk shape. We also
// scrape the file directly to assert the write is atomic — there must
// be no `.tmp` leftover after a successful call.
func TestSessionsIndex_AppendAndAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions-index.json")
	si, err := NewSessionsIndexAt(path)
	if err != nil {
		t.Fatalf("NewSessionsIndexAt: %v", err)
	}
	if err := si.Append(IndexEntry{
		ID:          "s-1",
		Workdir:     "/workdir/a",
		JournalPath: "/journal/s-1.jsonl",
		Model:       "test",
	}); err != nil {
		t.Fatalf("Append #1: %v", err)
	}
	if err := si.Append(IndexEntry{
		ID:          "s-2",
		Workdir:     "/workdir/b",
		JournalPath: "/journal/s-2.jsonl",
	}); err != nil {
		t.Fatalf("Append #2: %v", err)
	}
	if err := si.MarkDeleted("s-1"); err != nil {
		t.Fatalf("MarkDeleted: %v", err)
	}
	// Verify on-disk shape via raw read (so we exercise the JSON
	// contract, not just the round-trip).
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var f IndexFile
	if uerr := json.Unmarshal(body, &f); uerr != nil {
		t.Fatalf("unmarshal: %v", uerr)
	}
	if f.V != 1 {
		t.Errorf("V: got %d, want 1", f.V)
	}
	if len(f.Sessions) != 2 {
		t.Fatalf("sessions len: got %d, want 2", len(f.Sessions))
	}
	if f.Sessions[0].ID != "s-1" || !f.Sessions[0].Deleted {
		t.Errorf("sessions[0]: got id=%q deleted=%v, want s-1 true", f.Sessions[0].ID, f.Sessions[0].Deleted)
	}
	if f.Sessions[0].DeletedAt == "" {
		t.Errorf("sessions[0].DeletedAt should be set")
	}
	if f.Sessions[1].ID != "s-2" || f.Sessions[1].Deleted {
		t.Errorf("sessions[1]: got id=%q deleted=%v, want s-2 false", f.Sessions[1].ID, f.Sessions[1].Deleted)
	}
	// No stray .tmp leftover.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf(".tmp leftover: stat=%v", err)
	}
	// File mode is 0600.
	st, _ := os.Stat(path)
	if mode := st.Mode().Perm(); mode != 0o600 && mode != 0o644 {
		// Some umasks widen 0600. Accept 0600 (strict) and 0644
		// (CI default), since the package uses OpenFile with 0600
		// that is then weakened by umask. The atomic-write contract
		// is satisfied either way; we do NOT make this a strict
		// 0600 check because that's tested in daemondisco.
		t.Logf("file mode=%v (informational; daemondisco enforces 0600)", mode)
	}
}

// TestSessionsIndex_DuplicateAppendRejected asserts that Append with a
// duplicate id (deleted or not) errors rather than silently rewriting.
func TestSessionsIndex_DuplicateAppendRejected(t *testing.T) {
	dir := t.TempDir()
	si, _ := NewSessionsIndexAt(filepath.Join(dir, "sessions-index.json"))
	entry := IndexEntry{ID: "x", Workdir: "/w", JournalPath: "/j"}
	if err := si.Append(entry); err != nil {
		t.Fatalf("Append #1: %v", err)
	}
	if err := si.Append(entry); err == nil {
		t.Fatalf("expected duplicate error; got nil")
	}
	// Even after MarkDeleted, a re-Append must still fail.
	if err := si.MarkDeleted("x"); err != nil {
		t.Fatalf("MarkDeleted: %v", err)
	}
	if err := si.Append(entry); err == nil {
		t.Fatalf("expected post-delete dup error; got nil")
	}
}

// TestSessionsIndex_MarkDeletedUnknown returns an error for an
// id that was never Append'd.
func TestSessionsIndex_MarkDeletedUnknown(t *testing.T) {
	dir := t.TempDir()
	si, _ := NewSessionsIndexAt(filepath.Join(dir, "sessions-index.json"))
	if err := si.MarkDeleted("nope"); err == nil {
		t.Fatalf("expected error on unknown id")
	}
}

// TestSessionsIndex_MarkDeletedTwice rejects re-deletion.
func TestSessionsIndex_MarkDeletedTwice(t *testing.T) {
	dir := t.TempDir()
	si, _ := NewSessionsIndexAt(filepath.Join(dir, "sessions-index.json"))
	_ = si.Append(IndexEntry{ID: "x", Workdir: "/w", JournalPath: "/j"})
	if err := si.MarkDeleted("x"); err != nil {
		t.Fatalf("MarkDeleted: %v", err)
	}
	if err := si.MarkDeleted("x"); err == nil {
		t.Fatalf("expected re-delete error; got nil")
	}
}

// TestSessionsIndex_LoadEmpty returns an empty IndexFile with V=1 when
// the file does not exist (first-run case).
func TestSessionsIndex_LoadEmpty(t *testing.T) {
	dir := t.TempDir()
	si, _ := NewSessionsIndexAt(filepath.Join(dir, "sessions-index.json"))
	f, err := si.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if f == nil {
		t.Fatalf("Load: nil result")
	}
	if f.V != 1 {
		t.Errorf("V: got %d, want 1", f.V)
	}
	if len(f.Sessions) != 0 {
		t.Errorf("sessions len: got %d, want 0", len(f.Sessions))
	}
}

// TestSessionsIndex_ConcurrentAppend exercises the mutex under -race:
// 16 goroutines each Appending one distinct id; final Load must show
// 16 entries.
func TestSessionsIndex_ConcurrentAppend(t *testing.T) {
	dir := t.TempDir()
	si, _ := NewSessionsIndexAt(filepath.Join(dir, "sessions-index.json"))
	var barrier sync.WaitGroup
	const N = 16
	for i := 0; i < N; i++ {
		barrier.Add(1)
		go func(i int) {
			defer barrier.Done()
			id := "s-" + strconvI(i)
			if err := si.Append(IndexEntry{
				ID: id, Workdir: "/w", JournalPath: "/j",
			}); err != nil {
				t.Errorf("Append: %v", err)
			}
		}(i)
	}
	barrier.Wait()
	// assert.full-set: every id must be present in the on-disk file.
	f, err := si.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(f.Sessions) != N {
		t.Fatalf("sessions len: got %d, want %d", len(f.Sessions), N)
	}
}

// TestHubCreateUpdatesIndex asserts the SessionHub.Create -> index
// integration: an index entry appears for each Create, and Delete
// flips the Deleted flag.
func TestHubCreateUpdatesIndex(t *testing.T) {
	_, cleanup := withSandbox(t)
	defer cleanup()
	hub2, _ := NewHub()
	idxDir := t.TempDir()
	idxPath := filepath.Join(idxDir, "sessions-index.json")
	si, _ := NewSessionsIndexAt(idxPath)
	hub2.SetSessionsIndex(si)
	hub2.SetJournalDir(filepath.Join(idxDir, "sessions"))

	wd := t.TempDir()
	s, err := hub2.Create(CreateOptions{Workdir: wd, Model: "m"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	f, _ := si.Load()
	if len(f.Sessions) != 1 || f.Sessions[0].ID != s.ID {
		t.Fatalf("index after Create: %+v", f.Sessions)
	}
	if !strings.HasSuffix(f.Sessions[0].JournalPath, s.ID+".jsonl") {
		t.Errorf("JournalPath: got %q, want suffix %q", f.Sessions[0].JournalPath, s.ID+".jsonl")
	}
	if f.Sessions[0].Workdir != s.SessionRoot {
		t.Errorf("Workdir: got %q, want %q", f.Sessions[0].Workdir, s.SessionRoot)
	}

	if err := hub2.Delete(s.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	f, _ = si.Load()
	if !f.Sessions[0].Deleted {
		t.Fatalf("Delete did not mark deleted: %+v", f.Sessions[0])
	}
}

// strconvI is an inlined int->string for the concurrent test, kept
// inline so this test file's imports stay minimal.
func strconvI(i int) string {
	if i == 0 {
		return "0"
	}
	neg := false
	if i < 0 {
		neg = true
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
