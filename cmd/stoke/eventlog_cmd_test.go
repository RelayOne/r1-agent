package main

// eventlog_cmd_test.go — tests for the `stoke eventlog verify` and
// `stoke eventlog list-sessions` verbs. We exercise the runEventlogCmd
// entry point against a real eventlog.Log seeded in a tempdir, so the
// tests cover flag parsing, DB resolution, and exit codes end-to-end.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"github.com/ericmacdougall/stoke/internal/bus"
	"github.com/ericmacdougall/stoke/internal/eventlog"
)

// TestEventlog_NoVerb prints usage and exits 2.
func TestEventlog_NoVerb(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runEventlogCmd(nil, &out, &errBuf)
	if code != 2 {
		t.Fatalf("exit=%d, want 2; stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "usage") {
		t.Errorf("stderr=%q; want usage", errBuf.String())
	}
}

// TestEventlog_UnknownVerb exits 2 with a helpful stderr message.
func TestEventlog_UnknownVerb(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runEventlogCmd([]string{"bogus"}, &out, &errBuf)
	if code != 2 {
		t.Fatalf("exit=%d, want 2", code)
	}
	if !strings.Contains(errBuf.String(), "unknown verb") {
		t.Errorf("stderr=%q; want 'unknown verb'", errBuf.String())
	}
}

// TestEventlogVerify_Clean exits 0 on a well-formed log.
func TestEventlogVerify_Clean(t *testing.T) {
	dbPath := seedLog(t, []bus.Event{
		mkEvent("task.dispatch", "M1", "T1", "L1", map[string]any{"i": 1}),
		mkEvent("task.complete", "M1", "T1", "L1", map[string]any{"i": 2}),
	})

	var out, errBuf bytes.Buffer
	code := runEventlogCmd([]string{"verify", "--db", dbPath}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d, want 0; stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "OK") {
		t.Errorf("stdout=%q; want 'OK'", out.String())
	}
}

// TestEventlogVerify_Tampered exits 1 and prints the broken sequence.
//
// We flip a byte in the payload of row 2 using raw SQL so the stored
// parent_hash no longer matches the recomputed hash. Verify should
// return *eventlog.ErrChainBroken pointing at the corrupted row.
func TestEventlogVerify_Tampered(t *testing.T) {
	dbPath := seedLog(t, []bus.Event{
		mkEvent("a", "", "", "L1", map[string]any{"k": "v1"}),
		mkEvent("b", "", "", "L1", map[string]any{"k": "v2"}),
		mkEvent("c", "", "", "L1", map[string]any{"k": "v3"}),
	})
	// Corrupt row 2's payload. Raw update bypasses the canonical writer.
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := db.Exec(`UPDATE events SET payload = ? WHERE sequence = 2`, []byte(`{"k":"TAMPERED"}`)); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	db.Close()

	var out, errBuf bytes.Buffer
	code := runEventlogCmd([]string{"verify", "--db", dbPath}, &out, &errBuf)
	if code != 1 {
		t.Fatalf("exit=%d, want 1; stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "chain broken at sequence") {
		t.Errorf("stderr=%q; want chain-broken message", errBuf.String())
	}
	// The broken row is sequence 3: row 2's hash is still correct in
	// storage, but because row 3 was computed against row 2's ORIGINAL
	// payload, Verify sees the mismatch at sequence 3. (If Verify ever
	// catches the mismatch at row 2 instead, that would be a Verify
	// improvement rather than a test regression — accept either.)
	if !strings.Contains(errBuf.String(), "sequence 2") && !strings.Contains(errBuf.String(), "sequence 3") {
		t.Errorf("stderr=%q; want 'sequence 2' or 'sequence 3'", errBuf.String())
	}
}

// TestEventlogVerify_DBMissing exits 2 with 'db not found'.
func TestEventlogVerify_DBMissing(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runEventlogCmd([]string{"verify", "--db", "/no/such/events.db"}, &out, &errBuf)
	if code != 2 {
		t.Fatalf("exit=%d, want 2", code)
	}
	if !strings.Contains(errBuf.String(), "db not found") {
		t.Errorf("stderr=%q; want 'db not found'", errBuf.String())
	}
}

// TestEventlogListSessions_EmptyDB prints a friendly "no sessions" line.
func TestEventlogListSessions_EmptyDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, ".stoke", "events.db")
	log, err := eventlog.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	log.Close()

	var out, errBuf bytes.Buffer
	code := runEventlogCmd([]string{"list-sessions", "--db", dbPath}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d, want 0; stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "no session") {
		t.Errorf("stdout=%q; want 'no session'", out.String())
	}
}

// TestEventlogListSessions_Grouped prints session / mission / loop IDs
// under distinct headings, each deduplicated and sorted.
func TestEventlogListSessions_Grouped(t *testing.T) {
	dbPath := seedLog(t, []bus.Event{
		// session_id is populated by log.go from Scope.LoopID.
		mkEvent("a", "M-2", "T1", "L-2", map[string]any{}),
		mkEvent("b", "M-1", "T2", "L-1", map[string]any{}),
		mkEvent("c", "M-1", "T3", "L-2", map[string]any{}),
	})

	var out, errBuf bytes.Buffer
	code := runEventlogCmd([]string{"list-sessions", "--db", dbPath}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d, want 0; stderr=%q", code, errBuf.String())
	}
	got := out.String()
	for _, want := range []string{"sessions", "missions", "loops", "L-1", "L-2", "M-1", "M-2"} {
		if !strings.Contains(got, want) {
			t.Errorf("stdout missing %q\n--stdout--\n%s", want, got)
		}
	}
	// Missions should appear sorted ascending.
	m1 := strings.Index(got, "M-1")
	m2 := strings.Index(got, "M-2")
	if m1 < 0 || m2 < 0 || m1 > m2 {
		t.Errorf("expected M-1 before M-2; got M1=%d M2=%d", m1, m2)
	}
}

// TestDecideEventlogResume_FreshStart returns "fresh_start" for a
// session ID with no matching events.
func TestDecideEventlogResume_FreshStart(t *testing.T) {
	dbPath := seedLog(t, []bus.Event{
		mkEvent("task.dispatch", "M1", "T1", "L1", map[string]any{}),
	})
	repo := filepath.Dir(filepath.Dir(dbPath))

	mode, taskID, err := decideEventlogResume(repo, "L-unknown")
	if err != nil {
		t.Fatalf("decideEventlogResume: %v", err)
	}
	if mode != "fresh_start" {
		t.Errorf("mode=%q; want fresh_start", mode)
	}
	if taskID != "" {
		t.Errorf("taskID=%q; want empty", taskID)
	}
}

// TestDecideEventlogResume_RetryTask returns "retry_task" when the
// last task marker is a dispatch with no matching completion.
func TestDecideEventlogResume_RetryTask(t *testing.T) {
	dbPath := seedLog(t, []bus.Event{
		mkEvent("task.dispatch", "M1", "T1", "L-42", map[string]any{}),
	})
	repo := filepath.Dir(filepath.Dir(dbPath))

	mode, taskID, err := decideEventlogResume(repo, "L-42")
	if err != nil {
		t.Fatalf("decideEventlogResume: %v", err)
	}
	if mode != "retry_task" {
		t.Errorf("mode=%q; want retry_task", mode)
	}
	if taskID != "T1" {
		t.Errorf("taskID=%q; want T1", taskID)
	}
}

// TestDecideEventlogResume_MissingDB surfaces a clear error.
func TestDecideEventlogResume_MissingDB(t *testing.T) {
	dir := t.TempDir()
	_, _, err := decideEventlogResume(dir, "L-1")
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "events.db not found") {
		t.Errorf("err=%q; want 'events.db not found'", err.Error())
	}
}

// TestEventlogListSessions_JSON emits a parseable JSON object.
func TestEventlogListSessions_JSON(t *testing.T) {
	dbPath := seedLog(t, []bus.Event{
		mkEvent("a", "M-A", "", "L-A", map[string]any{}),
	})

	var out, errBuf bytes.Buffer
	code := runEventlogCmd([]string{"list-sessions", "--db", dbPath, "--json"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d, want 0; stderr=%q", code, errBuf.String())
	}
	var got eventlog.SessionIDs
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("json decode: %v\n--stdout--\n%s", err, out.String())
	}
	if len(got.Missions) != 1 || got.Missions[0] != "M-A" {
		t.Errorf("Missions=%v; want [M-A]", got.Missions)
	}
	if len(got.Loops) != 1 || got.Loops[0] != "L-A" {
		t.Errorf("Loops=%v; want [L-A]", got.Loops)
	}
}
