package membus

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/ericmacdougall/stoke/internal/bus"
)

// openTestDB returns a fresh SQLite handle rooted in t.TempDir. The WAL
// files are cleaned up automatically when the test finishes.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "membus.db") + "?_journal_mode=WAL&_busy_timeout=5000&_txlock=immediate"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newTestBus(t *testing.T) *Bus {
	t.Helper()
	b, err := NewBus(openTestDB(t), Options{})
	if err != nil {
		t.Fatalf("NewBus: %v", err)
	}
	return b
}

// --- Scope / validation ---

func TestValidScope(t *testing.T) {
	for _, s := range []Scope{
		ScopeSession, ScopeSessionStep, ScopeWorker,
		ScopeAllSessions, ScopeGlobal, ScopeAlways,
	} {
		if !ValidScope(s) {
			t.Errorf("ValidScope(%q) = false, want true", s)
		}
	}
	if ValidScope("not-a-scope") {
		t.Error("ValidScope(not-a-scope) = true, want false")
	}
	if ValidScope("") {
		t.Error("ValidScope(empty) = true, want false")
	}
}

func TestValidateRemember(t *testing.T) {
	cases := []struct {
		name    string
		req     RememberRequest
		wantErr bool
		isForb  bool
	}{
		{"happy", RememberRequest{Scope: ScopeSession, Content: "hi", Author: "system"}, false, false},
		{"bad scope", RememberRequest{Scope: "nope", Content: "hi"}, true, false},
		{"empty content", RememberRequest{Scope: ScopeSession, Content: ""}, true, false},
		{"too large", RememberRequest{Scope: ScopeSession, Content: strings.Repeat("x", MaxContentBytes+1)}, true, false},
		{"worker-always forbidden", RememberRequest{Scope: ScopeAlways, Content: "x", Author: "worker:w1"}, true, true},
		{"operator-always ok", RememberRequest{Scope: ScopeAlways, Content: "x", Author: "operator"}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRemember(tc.req)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateRemember err=%v, wantErr=%v", err, tc.wantErr)
			}
			if tc.isForb && !errors.Is(err, ErrScopeForbidden) {
				t.Errorf("want ErrScopeForbidden, got %v", err)
			}
		})
	}
}

// --- Schema migration ---

func TestBusSchemaIdempotent(t *testing.T) {
	db := openTestDB(t)
	if err := migrateBus(db); err != nil {
		t.Fatalf("migrate #1: %v", err)
	}
	if err := migrateBus(db); err != nil {
		t.Fatalf("migrate #2: %v", err)
	}
	// Insert a row and make sure it survives a third migration (DDL is
	// IF NOT EXISTS so this should be a no-op).
	_, err := db.Exec(`INSERT INTO stoke_memory_bus
		(created_at, scope, scope_target, key, content, content_hash)
		VALUES (?, 'session', '', 'k1', 'hello', 'abc')`,
		time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := migrateBus(db); err != nil {
		t.Fatalf("migrate #3: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM stoke_memory_bus`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("row count after 3 migrations = %d, want 1", n)
	}
}

func TestNewBusNilDB(t *testing.T) {
	if _, err := NewBus(nil, Options{}); err == nil {
		t.Error("NewBus(nil) want error")
	}
}

// --- Write / Read happy path ---

func TestWriteAndRead(t *testing.T) {
	ctx := context.Background()
	b := newTestBus(t)

	if err := b.WriteMemory(ctx, ScopeSession, "note-one"); err != nil {
		t.Fatalf("WriteMemory: %v", err)
	}
	if err := b.WriteMemory(ctx, ScopeSession, "note-two"); err != nil {
		t.Fatalf("WriteMemory: %v", err)
	}

	mems, err := b.ReadMemories(ctx, ScopeSession)
	if err != nil {
		t.Fatalf("ReadMemories: %v", err)
	}
	if len(mems) != 2 {
		t.Fatalf("got %d memories, want 2", len(mems))
	}
	if mems[0].Content != "note-one" || mems[1].Content != "note-two" {
		t.Errorf("content order wrong: %v / %v", mems[0].Content, mems[1].Content)
	}
	// oldest first
	if !mems[0].CreatedAt.Before(mems[1].CreatedAt) && !mems[0].CreatedAt.Equal(mems[1].CreatedAt) {
		t.Errorf("order: %v !< %v", mems[0].CreatedAt, mems[1].CreatedAt)
	}
	if mems[0].ContentHash == "" || len(mems[0].ContentHash) != 64 {
		t.Errorf("content_hash length = %d, want 64", len(mems[0].ContentHash))
	}
}

func TestReadScopeFilter(t *testing.T) {
	ctx := context.Background()
	b := newTestBus(t)

	mustRemember(t, b, RememberRequest{Scope: ScopeSession, Content: "session one", Author: "system"})
	mustRemember(t, b, RememberRequest{Scope: ScopeWorker, ScopeTarget: "t1", Content: "worker one", Author: "system"})
	mustRemember(t, b, RememberRequest{Scope: ScopeGlobal, Content: "global one", Author: "system"})

	if mems, _ := b.ReadMemories(ctx, ScopeSession); len(mems) != 1 || mems[0].Content != "session one" {
		t.Errorf("session filter: %+v", mems)
	}
	if mems, _ := b.ReadMemories(ctx, ScopeWorker); len(mems) != 1 || mems[0].Content != "worker one" {
		t.Errorf("worker filter: %+v", mems)
	}
	if mems, _ := b.ReadMemories(ctx, ScopeGlobal); len(mems) != 1 || mems[0].Content != "global one" {
		t.Errorf("global filter: %+v", mems)
	}
}

// --- Dedup / UPSERT ---

func TestRememberUpsertDedup(t *testing.T) {
	ctx := context.Background()
	b := newTestBus(t)
	req := RememberRequest{
		Scope:   ScopeSession,
		Key:     "dedup-me",
		Content: "v1",
		Author:  "system",
	}
	mustRemember(t, b, req)
	req.Content = "v2"
	req.Tags = []string{"updated"}
	mustRemember(t, b, req)

	mems, err := b.Recall(ctx, RecallRequest{Scope: ScopeSession, Key: "dedup-me"})
	if err != nil {
		t.Fatal(err)
	}
	if len(mems) != 1 {
		t.Fatalf("rows after UPSERT = %d, want 1", len(mems))
	}
	if mems[0].Content != "v2" {
		t.Errorf("content = %q, want v2", mems[0].Content)
	}
	if len(mems[0].Tags) != 1 || mems[0].Tags[0] != "updated" {
		t.Errorf("tags = %+v, want [updated]", mems[0].Tags)
	}
}

func TestWriteMemoryAutoKeyCollapsesDuplicates(t *testing.T) {
	ctx := context.Background()
	b := newTestBus(t)

	// WriteMemory auto-derives a key from the content hash, so two writes
	// of the same content to the same scope collapse to a single row.
	for i := 0; i < 3; i++ {
		if err := b.WriteMemory(ctx, ScopeGlobal, "identical"); err != nil {
			t.Fatal(err)
		}
	}
	mems, _ := b.ReadMemories(ctx, ScopeGlobal)
	if len(mems) != 1 {
		t.Errorf("duplicate content rows = %d, want 1", len(mems))
	}
}

// --- Expiry ---

func TestRecallHidesExpired(t *testing.T) {
	ctx := context.Background()
	b := newTestBus(t)

	past := time.Now().Add(-1 * time.Hour)
	future := time.Now().Add(1 * time.Hour)
	mustRemember(t, b, RememberRequest{Scope: ScopeSession, Key: "stale", Content: "old", Author: "system", ExpiresAt: &past})
	mustRemember(t, b, RememberRequest{Scope: ScopeSession, Key: "fresh", Content: "new", Author: "system", ExpiresAt: &future})

	mems, err := b.Recall(ctx, RecallRequest{Scope: ScopeSession})
	if err != nil {
		t.Fatal(err)
	}
	if len(mems) != 1 || mems[0].Key != "fresh" {
		t.Errorf("expired filter failed: %+v", mems)
	}
}

// --- Scope forbidden ---

func TestRememberRejectsWorkerAlways(t *testing.T) {
	ctx := context.Background()
	b := newTestBus(t)
	err := b.Remember(ctx, RememberRequest{
		Scope:   ScopeAlways,
		Content: "blocked",
		Author:  "worker:w42",
	})
	if !errors.Is(err, ErrScopeForbidden) {
		t.Errorf("got %v, want ErrScopeForbidden", err)
	}
}

// --- Nil-bus safety ---

func TestNilBusNoOps(t *testing.T) {
	ctx := context.Background()
	var b *Bus
	if err := b.WriteMemory(ctx, ScopeSession, "ignored"); err != nil {
		t.Errorf("nil WriteMemory: %v", err)
	}
	if err := b.Remember(ctx, RememberRequest{Scope: ScopeSession, Content: "ignored"}); err != nil {
		t.Errorf("nil Remember: %v", err)
	}
	mems, err := b.ReadMemories(ctx, ScopeSession)
	if err != nil || mems != nil {
		t.Errorf("nil ReadMemories = (%+v, %v), want (nil, nil)", mems, err)
	}
}

// --- Event emission ---

func TestRememberEmitsEvent(t *testing.T) {
	ctx := context.Background()
	busDir := t.TempDir()
	eb, err := bus.New(busDir)
	if err != nil {
		t.Fatalf("bus.New: %v", err)
	}
	defer eb.Close()

	var mu sync.Mutex
	var seen []bus.Event
	sub := eb.Subscribe(bus.Pattern{TypePrefix: "memory."}, func(evt bus.Event) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, evt)
	})
	defer sub.Cancel()

	b, err := NewBus(openTestDB(t), Options{EventBus: eb})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Remember(ctx, RememberRequest{
		Scope:     ScopeSession,
		SessionID: "s-1",
		TaskID:    "t-1",
		Author:    "worker:w1",
		Key:       "note",
		Content:   "hello world",
	}); err != nil {
		t.Fatal(err)
	}

	// Delivery is async; poll briefly.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(seen)
		mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 {
		t.Fatalf("event count = %d, want 1 (events: %+v)", len(seen), seen)
	}
	got := seen[0]
	if got.Type != EventTypeMemoryStored {
		t.Errorf("event type = %q, want %q", got.Type, EventTypeMemoryStored)
	}
	if got.Scope.LoopID != "s-1" || got.Scope.TaskID != "t-1" {
		t.Errorf("event scope = %+v", got.Scope)
	}
	if !strings.Contains(string(got.Payload), `"key":"note"`) {
		t.Errorf("payload missing key field: %s", got.Payload)
	}
	// Privacy contract: raw content must NEVER be on the event.
	if strings.Contains(string(got.Payload), "hello world") {
		t.Errorf("raw content leaked to event payload: %s", got.Payload)
	}
}

// --- helpers ---

func mustRemember(t *testing.T, b *Bus, req RememberRequest) {
	t.Helper()
	if req.Author == "" {
		req.Author = "system"
	}
	if err := b.Remember(context.Background(), req); err != nil {
		t.Fatalf("Remember(%+v): %v", req, err)
	}
	// Ensure created_at timestamps differ across consecutive writes.
	time.Sleep(1 * time.Millisecond)
}
