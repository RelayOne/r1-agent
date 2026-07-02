package memoryrecall

import (
	"testing"

	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/memory"
	"github.com/RelayOne/r1/internal/wisdom"
)

// TestLobeAcceptsPersistentWisdomStore covers SOTA gap #2 (native half):
// the memory-recall lobe's constructor now takes the wisdomStore
// interface, so a persistent *wisdom.SQLiteStore (not just the in-memory
// *wisdom.Store) can back the native loop's cross-session recall. This
// asserts the wiring compiles + the lobe indexes learnings from a
// SQLite-backed store.
func TestLobeAcceptsPersistentWisdomStore(t *testing.T) {
	path := t.TempDir() + "/wisdom.db"
	sq, err := wisdom.NewSQLiteStore(path)
	if err != nil {
		t.Skipf("SQLite unavailable (CGO-less build): %v", err)
	}
	sq.Record("prior-run", wisdom.Learning{
		Category:    wisdom.Gotcha,
		Description: "the auth middleware panics on a nil session token",
	})

	mem, err := memory.NewStore(memory.Config{})
	if err != nil {
		t.Fatal(err)
	}
	ws := cortex.NewWorkspace(nil, nil)
	// The key assertion: *wisdom.SQLiteStore satisfies the widened
	// wisdomStore param (it exposes Learnings()).
	lobe := NewMemoryRecallLobe(ws, mem, sq, nil)
	if lobe == nil {
		t.Fatal("lobe construction failed")
	}
	lobe.mu.Lock()
	lobe.rebuildIndexLocked()
	lobe.mu.Unlock()
	if lobe.DocCount() == 0 {
		t.Error("persistent wisdom learning was not indexed by the recall lobe")
	}
}
