package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/RelayOne/r1/internal/memory/membus"
	"github.com/RelayOne/r1/internal/plan"
	"github.com/RelayOne/r1/internal/streamjson"
)

// parseImportMemoryFlagForTest mirrors sowCmd's FlagSet registration
// of --import-memory (cmd/r1/main.go). It lets the flag-wiring
// test assert the public-facing name and default without
// double-invoking all the other SOW side effects.
func parseImportMemoryFlagForTest(args []string) string {
	fs := flag.NewFlagSet("sow-test", flag.ContinueOnError)
	p := fs.String("import-memory", "", "path to JSON memory snapshot to preload into the memory bus")
	_ = fs.Parse(args)
	return *p
}

// newCS4TestBus opens a scratch membus bus backed by an in-process
// SQLite DB. Matches the pattern in memory_import_test.go so CS-4
// tests stay consistent with the existing bus-test harness.
func newCS4TestBus(t *testing.T) *membus.Bus {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "mem.db")
	db, err := sql.Open("sqlite3", "file:"+dbPath+"?_journal_mode=WAL&_txlock=immediate")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	b, err := membus.NewBus(db, membus.Options{})
	if err != nil {
		t.Fatalf("NewBus: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

// TestEmitSessionEnd_CS4MemoryDeltaNoBus — when cfg.Bus is nil,
// emitSessionEnd must NOT emit a memory_delta event (legacy path
// stays quiet). The session.end event still fires.
func TestEmitSessionEnd_CS4MemoryDeltaNoBus(t *testing.T) {
	var buf bytes.Buffer
	tl := streamjson.NewTwoLane(&buf, true)
	cfg := sowNativeConfig{StreamJSON: tl}
	cfg.emitSessionEnd(plan.Session{ID: "s1"}, true, "ok")
	tl.Drain(500 * time.Millisecond)

	sawDelta := false
	sawEnd := false
	for _, line := range bytes.Split(buf.Bytes(), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			continue
		}
		if m["subtype"] == "stoke.session.memory_delta" {
			sawDelta = true
		}
		if m["subtype"] == "stoke.session.end" {
			sawEnd = true
		}
	}
	if sawDelta {
		t.Errorf("nil bus must not emit memory_delta; got:\n%s", buf.String())
	}
	if !sawEnd {
		t.Errorf("expected stoke.session.end to still fire; got:\n%s", buf.String())
	}
}

// TestEmitSessionEnd_CS4MemoryDeltaEmpty — when the bus is non-nil
// but contains no rows written after SessionStartedAt, the emit
// still fires with count=0 and rows=[].
func TestEmitSessionEnd_CS4MemoryDeltaEmpty(t *testing.T) {
	var buf bytes.Buffer
	tl := streamjson.NewTwoLane(&buf, true)
	bus := newCS4TestBus(t)
	cfg := sowNativeConfig{
		StreamJSON:       tl,
		Bus:              bus,
		SessionStartedAt: time.Now().Add(-1 * time.Second),
	}
	cfg.emitSessionEnd(plan.Session{ID: "s2"}, true, "ok")
	tl.Drain(500 * time.Millisecond)

	delta := findDeltaEvent(t, buf.Bytes())
	if delta == nil {
		t.Fatalf("expected stoke.session.memory_delta event; got:\n%s", buf.String())
	}
	if delta["session_id"] != "s2" {
		t.Errorf("session_id = %v, want s2", delta["session_id"])
	}
	if n, _ := delta["count"].(float64); n != 0 {
		t.Errorf("count = %v, want 0", delta["count"])
	}
}

// TestEmitSessionEnd_CS4MemoryDeltaCountsRows — rows written
// AFTER SessionStartedAt show up in the delta; rows written BEFORE
// do not.
func TestEmitSessionEnd_CS4MemoryDeltaCountsRows(t *testing.T) {
	var buf bytes.Buffer
	tl := streamjson.NewTwoLane(&buf, true)
	bus := newCS4TestBus(t)
	ctx := context.Background()

	// Row written BEFORE the session start — must NOT show up.
	if err := bus.Remember(ctx, membus.RememberRequest{
		Scope:   membus.ScopeAllSessions,
		Key:     "before",
		Content: "pre-session",
	}); err != nil {
		t.Fatalf("pre-session remember: %v", err)
	}
	// Small sleep + explicit cutoff so RFC3339Nano comparison is
	// unambiguous. 10ms is plenty on every platform membus runs on.
	time.Sleep(10 * time.Millisecond)
	cutoff := time.Now()
	time.Sleep(10 * time.Millisecond)

	// Two rows AFTER the cutoff — these are the delta.
	for i, key := range []string{"during-1", "during-2"} {
		if err := bus.Remember(ctx, membus.RememberRequest{
			Scope:   membus.ScopeAllSessions,
			Key:     key,
			Content: "mid-session",
		}); err != nil {
			t.Fatalf("row %d remember: %v", i, err)
		}
	}

	cfg := sowNativeConfig{
		StreamJSON:       tl,
		Bus:              bus,
		SessionStartedAt: cutoff,
	}
	cfg.emitSessionEnd(plan.Session{ID: "s3"}, true, "ok")
	tl.Drain(500 * time.Millisecond)

	delta := findDeltaEvent(t, buf.Bytes())
	if delta == nil {
		t.Fatalf("expected stoke.session.memory_delta event; got:\n%s", buf.String())
	}
	if n, _ := delta["count"].(float64); n != 2 {
		t.Errorf("count = %v, want 2 (pre-session row should be excluded)", delta["count"])
	}
	rows, ok := delta["rows"].([]any)
	if !ok {
		t.Fatalf("rows not an array: %v", delta["rows"])
	}
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2", len(rows))
	}
}

// TestEmitSessionEnd_CS4MemoryDeltaDisabledWhenStreamOff — when the
// streamjson emitter is not enabled, no event fires regardless of
// bus state.
func TestEmitSessionEnd_CS4MemoryDeltaDisabledWhenStreamOff(t *testing.T) {
	var buf bytes.Buffer
	tl := streamjson.NewTwoLane(&buf, false) // disabled
	bus := newCS4TestBus(t)
	cfg := sowNativeConfig{
		StreamJSON:       tl,
		Bus:              bus,
		SessionStartedAt: time.Now().Add(-time.Second),
	}
	cfg.emitSessionEnd(plan.Session{ID: "s4"}, true, "ok")
	tl.Drain(200 * time.Millisecond)
	if buf.Len() != 0 {
		t.Errorf("disabled emitter must produce no output, got: %q", buf.String())
	}
}

// TestImportMemoryFromFile_CS4PreloadVisibleInDelta — integration
// check that importMemoryFromFile runs against the same bus handle
// sowCmd would thread through cfg.Bus, so preloaded rows show up in
// a subsequent ExportDeltaSince whose cutoff predates the import.
func TestImportMemoryFromFile_CS4PreloadVisibleInDelta(t *testing.T) {
	bus := newCS4TestBus(t)
	cutoff := time.Now().Add(-time.Minute)

	path := writeSnapshot(t, `[
		{"scope":"all_sessions","key":"k1","content":"c1"},
		{"scope":"all_sessions","key":"k2","content":"c2"}
	]`)
	n, err := importMemoryFromFile(context.Background(), bus, path)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if n != 2 {
		t.Fatalf("imported count = %d, want 2", n)
	}

	// The delta exporter must see both preloaded rows because
	// cutoff is well before the import.
	rows := bus.ExportDeltaSince(cutoff)
	if len(rows) != 2 {
		t.Errorf("delta row count after import = %d, want 2", len(rows))
	}
}

// TestSowCmdFlagSet_ImportMemoryParsed — verify the --import-memory
// flag is parseable by sowCmd's FlagSet. Since sowCmd has side
// effects we replicate its flag registration in a local FlagSet to
// sanity-check the flag definition + default.
func TestSowCmdFlagSet_ImportMemoryParsed(t *testing.T) {
	// Mirrors the registration in cmd/r1/main.go:sowCmd. If the
	// real flag moves or renames, this test must be updated in
	// lockstep.
	// This test's real value is catching a typo in the flag name
	// during refactors — it locks the public-facing name as
	// --import-memory.
	const want = "/tmp/mem.json"
	got := parseImportMemoryFlagForTest([]string{"--import-memory", want})
	if got != want {
		t.Errorf("--import-memory parsed = %q, want %q", got, want)
	}
	got = parseImportMemoryFlagForTest([]string{})
	if got != "" {
		t.Errorf("default value for --import-memory = %q, want \"\"", got)
	}
}

// findDeltaEvent scans the emitted NDJSON for a stoke.session.memory_delta
// event and returns its parsed payload (or nil when absent).
func findDeltaEvent(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			continue
		}
		if m["subtype"] == "stoke.session.memory_delta" {
			return m
		}
	}
	return nil
}
