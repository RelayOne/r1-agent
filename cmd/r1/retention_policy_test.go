// retention_policy_test.go — regression tests for audit A030: the
// --retention-permanent register must actually store the policy, and
// the session-end enforcement hook must run (gated on
// STOKE_RETENTION=1) with the registered policy.

package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/memory/membus"
	"github.com/RelayOne/r1/internal/plan"
	"github.com/RelayOne/r1/internal/retention"
	_ "github.com/mattn/go-sqlite3"
)

// resetRetentionRegister restores the package-level policy register
// after a test mutates it, keeping tests order-independent.
func resetRetentionRegister(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { setRetentionPolicy(retention.Defaults()) })
}

// newRetentionTestBus builds a throwaway sqlite-backed membus.Bus and
// pre-adds the memory_type column the retention DELETE filters on
// (mirrors ensureMemoryTypeColumn in internal/retention).
func newRetentionTestBus(t *testing.T) *membus.Bus {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "retention.db") + "?_journal_mode=WAL&_busy_timeout=5000&_txlock=immediate"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	b, err := membus.NewBus(db, membus.Options{})
	if err != nil {
		t.Fatalf("membus.NewBus: %v", err)
	}
	if _, err := db.Exec(`ALTER TABLE stoke_memory_bus ADD COLUMN memory_type TEXT NOT NULL DEFAULT ''`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		t.Fatalf("add memory_type column: %v", err)
	}
	return b
}

// seedEphemeralRow inserts one session-scoped ephemeral memory row for
// sessionID — the row the §5.1 session-end wipe targets.
func seedEphemeralRow(t *testing.T, b *membus.Bus, sessionID string) {
	t.Helper()
	_, err := b.DB().Exec(`
		INSERT INTO stoke_memory_bus
			(created_at, expires_at, scope, scope_target, session_id, step_id, task_id,
			 author, key, content, content_hash, tags, metadata, memory_type)
		VALUES (?, NULL, 'session', '', ?, '', '', 'system', 'k', 'v', '', '[]', '{}', 'ephemeral')`,
		time.Now().UTC().Format(time.RFC3339Nano), sessionID)
	if err != nil {
		t.Fatalf("seed ephemeral row: %v", err)
	}
}

func countBusRows(t *testing.T, b *membus.Bus) int {
	t.Helper()
	var n int
	if err := b.DB().QueryRow(`SELECT COUNT(*) FROM stoke_memory_bus`).Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return n
}

// TestSetRetentionPolicyStoresValidPolicy asserts the A030 fix: a
// valid policy is stored in the package register, not discarded.
func TestSetRetentionPolicyStoresValidPolicy(t *testing.T) {
	resetRetentionRegister(t)
	setRetentionPolicy(buildRetentionPolicy(true))
	got := currentRetentionPolicy()
	if got.EphemeralMemories != retention.RetainForever {
		t.Errorf("EphemeralMemories = %q, want retain_forever", got.EphemeralMemories)
	}
	if got.StreamFiles != retention.RetainForever {
		t.Errorf("StreamFiles = %q, want retain_forever", got.StreamFiles)
	}
}

// TestSetRetentionPolicyRejectsInvalidKeepsRegister asserts fail-soft:
// an invalid policy must NOT overwrite the current register.
func TestSetRetentionPolicyRejectsInvalidKeepsRegister(t *testing.T) {
	resetRetentionRegister(t)
	setRetentionPolicy(buildRetentionPolicy(true))
	bad := retention.Defaults()
	bad.PermanentMemories = retention.Retain7Days // immutable-forever field → Validate error
	setRetentionPolicy(bad)
	if got := currentRetentionPolicy(); got.EphemeralMemories != retention.RetainForever {
		t.Errorf("invalid policy overwrote register: EphemeralMemories = %q", got.EphemeralMemories)
	}
}

// TestEnforceRetentionOnSessionEndFlagGate asserts the spec's §Flag
// gate: without STOKE_RETENTION=1 nothing is wiped; with it, the
// default policy's session-end ephemeral wipe runs.
func TestEnforceRetentionOnSessionEndFlagGate(t *testing.T) {
	resetRetentionRegister(t)
	bus := newRetentionTestBus(t)
	seedEphemeralRow(t, bus, "s-ret-1")

	t.Setenv("STOKE_RETENTION", "")
	enforceRetentionOnSessionEnd(context.Background(), "s-ret-1", bus)
	if got := countBusRows(t, bus); got != 1 {
		t.Fatalf("gate closed but rows wiped: got %d rows, want 1", got)
	}

	t.Setenv("STOKE_RETENTION", "1")
	enforceRetentionOnSessionEnd(context.Background(), "s-ret-1", bus)
	if got := countBusRows(t, bus); got != 0 {
		t.Errorf("gate open: got %d rows, want 0 (ephemeral wipe)", got)
	}
}

// TestEnforceRetentionOnSessionEndPermanentOverride asserts the flag's
// promise: with --retention-permanent registered, the session-end wipe
// is a no-op even when enforcement is enabled.
func TestEnforceRetentionOnSessionEndPermanentOverride(t *testing.T) {
	resetRetentionRegister(t)
	setRetentionPolicy(buildRetentionPolicy(true))
	bus := newRetentionTestBus(t)
	seedEphemeralRow(t, bus, "s-ret-2")

	t.Setenv("STOKE_RETENTION", "1")
	enforceRetentionOnSessionEnd(context.Background(), "s-ret-2", bus)
	if got := countBusRows(t, bus); got != 1 {
		t.Errorf("--retention-permanent run wiped rows: got %d, want 1", got)
	}
}

// TestEmitSessionEndRunsRetentionWithoutStreamJSON pins the wiring
// point (spec item 32): the session-end retention pass must fire from
// emitSessionEnd even when no streamjson emitter is configured.
func TestEmitSessionEndRunsRetentionWithoutStreamJSON(t *testing.T) {
	resetRetentionRegister(t)
	bus := newRetentionTestBus(t)
	seedEphemeralRow(t, bus, "s-ret-3")

	t.Setenv("STOKE_RETENTION", "1")
	cfg := sowNativeConfig{Bus: bus} // StreamJSON nil: legacy invocation
	cfg.emitSessionEnd(plan.Session{ID: "s-ret-3"}, true, "done")
	if got := countBusRows(t, bus); got != 0 {
		t.Errorf("emitSessionEnd did not run session-end retention: %d rows left", got)
	}
}
