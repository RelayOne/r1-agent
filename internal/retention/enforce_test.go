package retention

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/RelayOne/r1-agent/internal/memory/membus"
)

// newBusForTest opens a fresh in-tmpdir SQLite DB, runs the membus migration,
// and returns a wired-up *membus.Bus. Cleanup happens via t.TempDir +
// t.Cleanup; callers never need to manually close.
func newBusForTest(t *testing.T) *membus.Bus {
	t.Helper()
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "retention.db") + "?_journal_mode=WAL&_busy_timeout=5000&_txlock=immediate"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	b, err := membus.NewBus(db, membus.Options{})
	if err != nil {
		t.Fatalf("membus.NewBus: %v", err)
	}
	return b
}

// insertMemory speaks directly to the stoke_memory_bus table so tests can
// seed rows with the `memory_type` column populated — something the public
// Remember API does not expose yet. The column is added lazily by
// ensureMemoryTypeColumn; we call that once up front so the INSERT column
// list is valid.
func insertMemory(t *testing.T, b *membus.Bus, scope, sessionID, memType, key, content string, expiresAt *time.Time) {
	t.Helper()
	ctx := context.Background()
	if err := ensureMemoryTypeColumn(ctx, b.DB()); err != nil {
		t.Fatalf("ensureMemoryTypeColumn: %v", err)
	}
	var expires any
	if expiresAt != nil {
		expires = expiresAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := b.DB().ExecContext(ctx, `
		INSERT INTO stoke_memory_bus
			(created_at, expires_at, scope, scope_target, session_id, step_id, task_id,
			 author, key, content, content_hash, tags, metadata, memory_type)
		VALUES (?, ?, ?, '', ?, '', '', 'system', ?, ?, '', '[]', '{}', ?)`,
		time.Now().UTC().Format(time.RFC3339Nano),
		expires, scope, sessionID, key, content, memType)
	if err != nil {
		t.Fatalf("insert memory: %v", err)
	}
}

func countMemories(t *testing.T, b *membus.Bus) int {
	t.Helper()
	var n int
	if err := b.DB().QueryRow(`SELECT COUNT(*) FROM stoke_memory_bus`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// --- EnforceOnSessionEnd ---------------------------------------------------

func TestEnforceOnSessionEndDeletesEphemeralForSession(t *testing.T) {
	ctx := context.Background()
	b := newBusForTest(t)

	// Target session's ephemeral row: should be deleted.
	insertMemory(t, b, "session", "s-A", "ephemeral", "k1", "ephemeral-A", nil)
	// Same session but non-ephemeral: should survive.
	insertMemory(t, b, "session", "s-A", "session", "k2", "session-A", nil)
	// Same session but wrong scope: should survive.
	insertMemory(t, b, "all_sessions", "s-A", "ephemeral", "k3", "ephemeral-A-global", nil)
	// Different session ephemeral row: should survive.
	insertMemory(t, b, "session", "s-B", "ephemeral", "k4", "ephemeral-B", nil)
	// session_step + worker scopes are included in the §5.1 SQL.
	insertMemory(t, b, "session_step", "s-A", "ephemeral", "k5", "step-A", nil)
	insertMemory(t, b, "worker", "s-A", "ephemeral", "k6", "worker-A", nil)

	if got := countMemories(t, b); got != 6 {
		t.Fatalf("precondition: got %d rows, want 6", got)
	}

	if err := EnforceOnSessionEnd(ctx, Defaults(), "s-A", b); err != nil {
		t.Fatalf("EnforceOnSessionEnd: %v", err)
	}

	// After wipe: rows 2,3,4 survive (3 total).
	if got := countMemories(t, b); got != 3 {
		t.Errorf("post-wipe row count = %d, want 3", got)
	}

	// Verify specifically that the three surviving rows are the ones we
	// expect, not some accidental subset.
	rows, err := b.DB().Query(`SELECT key FROM stoke_memory_bus ORDER BY key`)
	if err != nil {
		t.Fatalf("select keys: %v", err)
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows iterate: %v", err)
	}
	want := []string{"k2", "k3", "k4"}
	if len(keys) != len(want) {
		t.Fatalf("surviving keys = %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Errorf("key[%d] = %q, want %q", i, keys[i], want[i])
		}
	}
}

func TestEnforceOnSessionEndNilBusNoOp(t *testing.T) {
	if err := EnforceOnSessionEnd(context.Background(), Defaults(), "s-1", nil); err != nil {
		t.Errorf("nil bus: got err %v, want nil", err)
	}
}

func TestEnforceOnSessionEndEmptySessionErrors(t *testing.T) {
	b := newBusForTest(t)
	if err := EnforceOnSessionEnd(context.Background(), Defaults(), "", b); err == nil {
		t.Error("empty sessionID: want error, got nil")
	}
}

func TestEnforceOnSessionEndRespectsRetainForeverPolicy(t *testing.T) {
	ctx := context.Background()
	b := newBusForTest(t)

	insertMemory(t, b, "session", "s-A", "ephemeral", "k1", "would-be-wiped", nil)
	if got := countMemories(t, b); got != 1 {
		t.Fatalf("precondition: got %d, want 1", got)
	}

	p := Defaults()
	p.EphemeralMemories = RetainForever // operator override: never wipe
	if err := EnforceOnSessionEnd(ctx, p, "s-A", b); err != nil {
		t.Fatalf("EnforceOnSessionEnd: %v", err)
	}

	if got := countMemories(t, b); got != 1 {
		t.Errorf("RetainForever override violated: row count = %d, want 1", got)
	}
}

// --- EnforceSweep ----------------------------------------------------------

func TestEnforceSweepDeletesExpiredMemories(t *testing.T) {
	ctx := context.Background()
	b := newBusForTest(t)

	past := time.Now().Add(-1 * time.Hour)
	future := time.Now().Add(1 * time.Hour)
	insertMemory(t, b, "session", "s-A", "session", "stale", "old", &past)
	insertMemory(t, b, "session", "s-A", "session", "fresh", "new", &future)
	insertMemory(t, b, "session", "s-A", "session", "no-ttl", "forever", nil)

	res, err := EnforceSweep(ctx, Defaults(), b)
	if err != nil {
		t.Fatalf("EnforceSweep: %v", err)
	}
	if res.SessionExpired != 1 {
		t.Errorf("SessionExpired = %d, want 1", res.SessionExpired)
	}
	if got := countMemories(t, b); got != 2 {
		t.Errorf("post-sweep row count = %d, want 2", got)
	}
}

func TestEnforceSweepStreamAndCheckpointFiles(t *testing.T) {
	ctx := context.Background()
	b := newBusForTest(t)

	tmp := t.TempDir()
	sDir := filepath.Join(tmp, "streams")
	cDir := filepath.Join(tmp, "checkpoints")
	if err := os.MkdirAll(sDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Redirect the package-level directory targets for this test.
	prevS, prevC := streamsDir, checkpointsDir
	streamsDir, checkpointsDir = sDir, cDir
	t.Cleanup(func() { streamsDir, checkpointsDir = prevS, prevC })

	oldMtime := time.Now().Add(-100 * 24 * time.Hour) // 100 days old
	newMtime := time.Now().Add(-1 * time.Hour)

	// Streams: one old (>90d), one fresh.
	oldStream := filepath.Join(sDir, "old.jsonl")
	freshStream := filepath.Join(sDir, "fresh.jsonl")
	writeFile(t, oldStream, "old", oldMtime)
	writeFile(t, freshStream, "new", newMtime)

	// Checkpoints: the default policy is 30d. Create one 100d old and one
	// 1h old; only the former should be swept.
	oldCkpt := filepath.Join(cDir, "old.json")
	freshCkpt := filepath.Join(cDir, "fresh.json")
	writeFile(t, oldCkpt, "old", oldMtime)
	writeFile(t, freshCkpt, "new", newMtime)

	res, err := EnforceSweep(ctx, Defaults(), b)
	if err != nil {
		t.Fatalf("EnforceSweep: %v", err)
	}
	if res.StreamFilesRemoved != 1 {
		t.Errorf("StreamFilesRemoved = %d, want 1", res.StreamFilesRemoved)
	}
	if res.CheckpointsRemoved != 1 {
		t.Errorf("CheckpointsRemoved = %d, want 1", res.CheckpointsRemoved)
	}

	if _, err := os.Stat(oldStream); !os.IsNotExist(err) {
		t.Errorf("old stream still present: err=%v", err)
	}
	if _, err := os.Stat(freshStream); err != nil {
		t.Errorf("fresh stream gone: %v", err)
	}
	if _, err := os.Stat(oldCkpt); !os.IsNotExist(err) {
		t.Errorf("old checkpoint still present: err=%v", err)
	}
	if _, err := os.Stat(freshCkpt); err != nil {
		t.Errorf("fresh checkpoint gone: %v", err)
	}
}

func TestEnforceSweepSkipsMissingDirs(t *testing.T) {
	// No dirs, no bus: sweep should still return a clean SweepResult with
	// all counters at zero.
	ctx := context.Background()

	prevS, prevC := streamsDir, checkpointsDir
	streamsDir = filepath.Join(t.TempDir(), "does-not-exist-streams")
	checkpointsDir = filepath.Join(t.TempDir(), "does-not-exist-checkpoints")
	t.Cleanup(func() { streamsDir, checkpointsDir = prevS, prevC })

	res, err := EnforceSweep(ctx, Defaults(), nil)
	if err != nil {
		t.Fatalf("EnforceSweep with no bus + missing dirs: %v", err)
	}
	if res != (SweepResult{}) {
		t.Errorf("expected zero SweepResult, got %+v", res)
	}
}

func TestEnforceSweepRetainForeverSkipsFileSweeps(t *testing.T) {
	ctx := context.Background()
	b := newBusForTest(t)

	tmp := t.TempDir()
	sDir := filepath.Join(tmp, "streams")
	if err := os.MkdirAll(sDir, 0o755); err != nil {
		t.Fatal(err)
	}
	prevS, prevC := streamsDir, checkpointsDir
	streamsDir = sDir
	checkpointsDir = filepath.Join(tmp, "ckpt-missing")
	t.Cleanup(func() { streamsDir, checkpointsDir = prevS, prevC })

	ancient := time.Now().Add(-365 * 24 * time.Hour)
	writeFile(t, filepath.Join(sDir, "ancient.jsonl"), "x", ancient)

	p := Defaults()
	p.StreamFiles = RetainForever // operator opted out of stream-file sweep
	p.CheckpointFiles = RetainForever

	res, err := EnforceSweep(ctx, p, b)
	if err != nil {
		t.Fatalf("EnforceSweep: %v", err)
	}
	if res.StreamFilesRemoved != 0 {
		t.Errorf("StreamFilesRemoved = %d, want 0 under RetainForever", res.StreamFilesRemoved)
	}
	if _, err := os.Stat(filepath.Join(sDir, "ancient.jsonl")); err != nil {
		t.Errorf("ancient file wrongly deleted: %v", err)
	}
}

// --- helpers ---------------------------------------------------------------

func writeFile(t *testing.T, path, content string, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes %q: %v", path, err)
	}
}
