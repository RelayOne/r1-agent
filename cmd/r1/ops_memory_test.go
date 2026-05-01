package main

// ops_memory_test.go — UXMEM-core: tests for the `r1 memory list`
// and `r1 memory add` verbs. Follows the ops_tasks_test.go pattern
// — seed a fresh membus-backed SQLite DB in a tempdir, then exercise
// the command over stdout/stderr.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"github.com/RelayOne/r1/internal/memory/membus"
)

// newMemoryDB creates a fresh memory.db under a tempdir and returns
// its path. The bus is migrated via membus.NewBus so the verb under
// test encounters a schema-ready DB. Closes the handle so the verb can
// re-open it.
func newMemoryDB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ".stoke", "memory.db")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dsn := "file:" + path + "?_journal_mode=WAL&_busy_timeout=5000&_txlock=immediate"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	bus, err := membus.NewBus(db, membus.Options{})
	if err != nil {
		t.Fatalf("membus.NewBus: %v", err)
	}
	// Stop the writer goroutine before closing the underlying handle so
	// it doesn't race a concurrent Exec against a closed *sql.DB.
	if err := bus.Close(); err != nil {
		t.Fatalf("bus.Close: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close: %v", err)
	}
	return path
}

// ---------------------------------------------------------------------------
// `memory list`
// ---------------------------------------------------------------------------

func TestMemoryList_EmptyDB(t *testing.T) {
	dbPath := newMemoryDB(t)
	var out, errBuf bytes.Buffer
	code := runMemoryCmd([]string{"list", "--db", dbPath}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "no memories") {
		t.Errorf("stdout=%q; want 'no memories'", out.String())
	}
}

func TestMemoryList_MissingDB(t *testing.T) {
	// A missing DB is treated as "no memories" (success), not an error.
	// Matches the opt-in semantics of the memory bus.
	var out, errBuf bytes.Buffer
	code := runMemoryCmd([]string{"list", "--db", "/no/such/path/memory.db"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "no memories") {
		t.Errorf("stdout=%q; want 'no memories'", out.String())
	}
}

func TestMemoryList_InvalidScope(t *testing.T) {
	dbPath := newMemoryDB(t)
	var out, errBuf bytes.Buffer
	code := runMemoryCmd([]string{"list", "--db", dbPath, "--scope", "bogus"}, &out, &errBuf)
	if code != 2 {
		t.Fatalf("exit=%d, want 2; stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "invalid --scope") {
		t.Errorf("stderr=%q; want 'invalid --scope'", errBuf.String())
	}
}

func TestMemoryList_AfterAdd(t *testing.T) {
	dbPath := newMemoryDB(t)

	// Seed via `memory add`.
	var out, errBuf bytes.Buffer
	addCode := runMemoryCmd([]string{
		"add", "--db", dbPath,
		"--scope", "session",
		"--session", "S1",
		"hello world",
	}, &out, &errBuf)
	if addCode != 0 {
		t.Fatalf("add exit=%d, stderr=%q", addCode, errBuf.String())
	}
	if !strings.Contains(out.String(), "wrote memory") {
		t.Errorf("add stdout=%q; want 'wrote memory'", out.String())
	}

	// Now list.
	out.Reset()
	errBuf.Reset()
	listCode := runMemoryCmd([]string{"list", "--db", dbPath}, &out, &errBuf)
	if listCode != 0 {
		t.Fatalf("list exit=%d, stderr=%q", listCode, errBuf.String())
	}
	got := out.String()
	for _, want := range []string{"SCOPE", "session", "operator", "hello world", "S1"} {
		if !strings.Contains(got, want) {
			t.Errorf("list stdout missing %q\n--stdout--\n%s", want, got)
		}
	}
}

func TestMemoryList_JSON(t *testing.T) {
	dbPath := newMemoryDB(t)

	// Seed two memories under different sessions.
	for _, tc := range []struct {
		content string
		session string
	}{
		{"first", "S1"},
		{"second", "S2"},
	} {
		var out, errBuf bytes.Buffer
		code := runMemoryCmd([]string{
			"add", "--db", dbPath,
			"--scope", "session",
			"--session", tc.session,
			tc.content,
		}, &out, &errBuf)
		if code != 0 {
			t.Fatalf("add %q exit=%d, stderr=%q", tc.content, code, errBuf.String())
		}
	}

	// List filtered to S1, JSON.
	var out, errBuf bytes.Buffer
	code := runMemoryCmd([]string{
		"list", "--db", dbPath,
		"--scope", "session",
		"--session", "S1",
		"--json",
	}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("list exit=%d, stderr=%q", code, errBuf.String())
	}
	dec := json.NewDecoder(bytes.NewReader(out.Bytes()))
	var rows []memoryJSONRow
	for dec.More() {
		var r memoryJSONRow
		if err := dec.Decode(&r); err != nil {
			t.Fatalf("decode: %v\nstdout=%s", err, out.String())
		}
		rows = append(rows, r)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d JSON rows, want 1:\n%s", len(rows), out.String())
	}
	row := rows[0]
	if row.Content != "first" {
		t.Errorf("Content=%q, want 'first'", row.Content)
	}
	if row.SessionID != "S1" {
		t.Errorf("SessionID=%q, want 'S1'", row.SessionID)
	}
	if row.Scope != "session" {
		t.Errorf("Scope=%q, want 'session'", row.Scope)
	}
	if row.Author != "operator" {
		t.Errorf("Author=%q, want 'operator'", row.Author)
	}
}

// ---------------------------------------------------------------------------
// `memory add`
// ---------------------------------------------------------------------------

func TestMemoryAdd_RequiresScope(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runMemoryCmd([]string{"add", "hi"}, &out, &errBuf)
	if code != 2 {
		t.Fatalf("exit=%d, want 2; stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "--scope is required") {
		t.Errorf("stderr=%q; want '--scope is required'", errBuf.String())
	}
}

func TestMemoryAdd_InvalidScope(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runMemoryCmd([]string{"add", "--scope", "bogus", "hi"}, &out, &errBuf)
	if code != 2 {
		t.Fatalf("exit=%d, want 2; stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "invalid --scope") {
		t.Errorf("stderr=%q; want 'invalid --scope'", errBuf.String())
	}
}

func TestMemoryAdd_CreatesDBIfMissing(t *testing.T) {
	// Point to a non-existent file under a tempdir. `add` should
	// create it (NewBus is idempotent) and the follow-up list
	// should see the row.
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "brand", "new", "memory.db")

	var out, errBuf bytes.Buffer
	code := runMemoryCmd([]string{
		"add", "--db", dbPath,
		"--scope", "global",
		"lesson learned: always write tests",
	}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("add exit=%d, stderr=%q", code, errBuf.String())
	}

	// List it back.
	out.Reset()
	errBuf.Reset()
	code = runMemoryCmd([]string{"list", "--db", dbPath}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("list exit=%d, stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "lesson learned") {
		t.Errorf("list stdout missing seeded content:\n%s", out.String())
	}
}

func TestMemoryAdd_JSON(t *testing.T) {
	dbPath := newMemoryDB(t)
	var out, errBuf bytes.Buffer
	code := runMemoryCmd([]string{
		"add", "--db", dbPath,
		"--scope", "session",
		"--session", "S1",
		"--json",
		"some fact",
	}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%q", code, errBuf.String())
	}
	var got memoryAddJSON
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &got); err != nil {
		t.Fatalf("unmarshal: %v\nstdout=%s", err, out.String())
	}
	if !got.OK {
		t.Errorf("OK=false; want true")
	}
	if got.Scope != "session" {
		t.Errorf("Scope=%q, want session", got.Scope)
	}
	if got.ID <= 0 {
		t.Errorf("ID=%d, want > 0", got.ID)
	}
	if got.ContentHash == "" {
		t.Errorf("ContentHash empty; want populated")
	}
}

func TestMemoryAdd_Stdin(t *testing.T) {
	// The content-from-stdin path is exercised by readAddContent
	// directly — runMemoryCmd hard-wires os.Stdin, which we don't
	// redirect from the test harness. Exercising the helper covers
	// the important branches without interfering with the test
	// runner's real stdin.
	got, code := readAddContent(nil, strings.NewReader("piped content\n"), &bytes.Buffer{})
	if code != 0 {
		t.Fatalf("exit=%d, want 0", code)
	}
	if got != "piped content" {
		t.Errorf("content=%q, want 'piped content'", got)
	}
}

func TestMemoryAdd_StdinEmpty(t *testing.T) {
	var errBuf bytes.Buffer
	_, code := readAddContent(nil, strings.NewReader(""), &errBuf)
	if code != 2 {
		t.Fatalf("exit=%d, want 2", code)
	}
	if !strings.Contains(errBuf.String(), "no content") {
		t.Errorf("stderr=%q; want 'no content'", errBuf.String())
	}
}

// ---------------------------------------------------------------------------
// dispatcher
// ---------------------------------------------------------------------------

func TestMemoryDispatcher_NoArgs(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runMemoryCmd(nil, &out, &errBuf)
	if code != 2 {
		t.Fatalf("exit=%d, want 2", code)
	}
	if !strings.Contains(errBuf.String(), "usage: r1 memory") {
		t.Errorf("stderr=%q; want usage banner", errBuf.String())
	}
}

func TestMemoryDispatcher_UnknownVerb(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runMemoryCmd([]string{"frobnicate"}, &out, &errBuf)
	if code != 2 {
		t.Fatalf("exit=%d, want 2", code)
	}
	if !strings.Contains(errBuf.String(), "unknown verb") {
		t.Errorf("stderr=%q; want 'unknown verb'", errBuf.String())
	}
}

func TestMemoryDispatcher_Help(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runMemoryCmd([]string{"--help"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d, want 0", code)
	}
	if !strings.Contains(out.String(), "usage: r1 memory") {
		t.Errorf("stdout=%q; want usage banner", out.String())
	}
}
