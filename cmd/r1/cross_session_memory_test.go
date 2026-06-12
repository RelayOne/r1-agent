package main

import (
	"path/filepath"
	"testing"

	"github.com/RelayOne/r1/internal/memory"
)

// TestOpenCrossSessionMemoryRoundTrip guards the cross-session memory bridge:
// openCrossSessionMemory must load the SAME .r1/agent-memory.json file that the
// in-task memory_store tool writes (internal/tools/memory_tools.go), so a
// learning persisted in one session is recalled at the start of the next.
// If this regresses, app.RunConfig.Memory goes nil again and cross-session
// recall silently dies (the original audit finding).
func TestOpenCrossSessionMemoryRoundTrip(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, ".r1", "agent-memory.json")

	// Simulate a prior session writing a learning via the same store/path the
	// memory_store tool uses.
	prior, err := memory.NewStore(memory.Config{Path: path})
	if err != nil {
		t.Fatalf("seed NewStore: %v", err)
	}
	prior.Remember(memory.CatGotcha, "the auth middleware double-counts retries", "auth", "retry")
	if err := prior.Save(); err != nil {
		t.Fatalf("seed Save: %v", err)
	}

	// A later session opens the bridge and must recall it.
	store := openCrossSessionMemory(repo)
	if store == nil {
		t.Fatal("openCrossSessionMemory returned nil for a valid repo with a persisted store")
	}
	hits := store.Recall("auth middleware retries", 5)
	if len(hits) == 0 {
		t.Fatal("cross-session recall returned no entries; the bridge is not reading the persisted .r1/agent-memory.json")
	}
}

// TestOpenCrossSessionMemoryMissingFileIsSafe verifies a fresh repo (no prior
// memory file) yields a usable empty store rather than nil, so the bridge never
// disables itself just because this is the first run.
func TestOpenCrossSessionMemoryMissingFileIsSafe(t *testing.T) {
	store := openCrossSessionMemory(t.TempDir())
	if store == nil {
		t.Fatal("openCrossSessionMemory returned nil for a fresh repo; first-run recall would be disabled")
	}
	if got := store.Count(); got != 0 {
		t.Fatalf("expected empty store on fresh repo, got %d entries", got)
	}
}
