package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"github.com/RelayOne/r1/internal/memory/membus"
)

func newTestBus(t *testing.T) *membus.Bus {
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

func writeSnapshot(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "snapshot.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	return p
}

func TestImportMemoryFromFile_ImportsThreeRows(t *testing.T) {
	bus := newTestBus(t)
	path := writeSnapshot(t, `[
		{"scope":"all_sessions","key":"k1","content":"c1"},
		{"scope":"all_sessions","key":"k2","content":"c2"},
		{"scope":"all_sessions","key":"k3","content":"c3"}
	]`)

	n, err := importMemoryFromFile(context.Background(), bus, path)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if n != 3 {
		t.Errorf("imported count = %d, want 3", n)
	}

	rows, err := bus.Recall(context.Background(), membus.RecallRequest{
		Scope: membus.ScopeAllSessions,
	})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(rows) != 3 {
		t.Errorf("recall count = %d, want 3", len(rows))
	}
}

func TestImportMemoryFromFile_BadJSON(t *testing.T) {
	bus := newTestBus(t)
	path := writeSnapshot(t, `not-json`)
	_, err := importMemoryFromFile(context.Background(), bus, path)
	if err == nil {
		t.Fatalf("expected parse error, got nil")
	}
}

func TestImportMemoryFromFile_MissingPath(t *testing.T) {
	bus := newTestBus(t)
	_, err := importMemoryFromFile(context.Background(), bus, "/nonexistent/xyz.json")
	if err == nil {
		t.Fatalf("expected read error, got nil")
	}
}

func TestImportMemoryFromFile_NilBus(t *testing.T) {
	path := writeSnapshot(t, `[]`)
	_, err := importMemoryFromFile(context.Background(), nil, path)
	if err == nil {
		t.Fatalf("nil bus should error")
	}
}

func TestImportMemoryFromFile_EmptyArray(t *testing.T) {
	bus := newTestBus(t)
	path := writeSnapshot(t, `[]`)
	n, err := importMemoryFromFile(context.Background(), bus, path)
	if err != nil || n != 0 {
		t.Fatalf("empty array: n=%d err=%v", n, err)
	}
}
