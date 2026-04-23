package wisdom

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSQLiteStoreRecordAndRetrieve(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "wisdom.db")

	s, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	s.Record("task-1", Learning{
		Category:    Gotcha,
		Description: "Always run tests before committing",
		File:        "main.go",
	})
	s.Record("task-2", Learning{
		Category:    Decision,
		Description: "Use pgx v5 for database access",
	})

	learnings := s.Learnings()
	if len(learnings) != 2 {
		t.Fatalf("expected 2 learnings, got %d", len(learnings))
	}
	if learnings[0].TaskID != "task-1" {
		t.Errorf("first learning task=%s, want task-1", learnings[0].TaskID)
	}
	if learnings[0].Category != Gotcha {
		t.Errorf("category=%v, want Gotcha", learnings[0].Category)
	}
}

func TestSQLiteStoreFindByPattern(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSQLiteStore(filepath.Join(dir, "wisdom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	s.Record("task-1", Learning{
		Category:       Gotcha,
		Description:    "Import cycle in foo package",
		FailurePattern: "abc123",
	})

	found := s.FindByPattern("abc123")
	if found == nil {
		t.Fatal("expected to find by pattern")
	}
	if found.Description != "Import cycle in foo package" {
		t.Errorf("description=%q", found.Description)
	}

	notFound := s.FindByPattern("nonexistent")
	if notFound != nil {
		t.Error("expected nil for nonexistent pattern")
	}
}

func TestSQLiteStoreForPrompt(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSQLiteStore(filepath.Join(dir, "wisdom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	s.Record("task-1", Learning{
		Category:    Gotcha,
		Description: "Test failure",
	})

	prompt := s.ForPrompt()
	if prompt == "" {
		t.Error("expected non-empty prompt")
	}
	if len(prompt) > maxPromptLen {
		t.Errorf("prompt too long: %d > %d", len(prompt), maxPromptLen)
	}
}

func TestSQLiteStorePersistence(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "wisdom.db")

	// Write some data
	s1, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	s1.Record("task-1", Learning{Category: Pattern, Description: "Use interfaces"})
	s1.Close()

	// Reopen and verify data persisted
	s2, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	learnings := s2.Learnings()
	if len(learnings) != 1 {
		t.Fatalf("expected 1 learning after reopen, got %d", len(learnings))
	}
	if learnings[0].Description != "Use interfaces" {
		t.Errorf("description=%q after reopen", learnings[0].Description)
	}
}

func TestSQLiteStoreEmpty(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSQLiteStore(filepath.Join(dir, "wisdom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if len(s.Learnings()) != 0 {
		t.Error("expected empty learnings")
	}
	if s.ForPrompt() != "" {
		t.Error("expected empty prompt")
	}
	if s.Count() != 0 {
		t.Error("expected count 0")
	}
}

func TestSQLiteStoreFileCreated(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "wisdom.db")

	s, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("database file should exist")
	}
}

// ------------------------------------------------------------------
// S-9: stoke_memories table tests.
// ------------------------------------------------------------------

func newMemoryStore(t *testing.T) *SQLiteStore {
	t.Helper()
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "wisdom.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestStoreMemoryAndList(t *testing.T) {
	s := newMemoryStore(t)

	rows := []struct {
		mtype, key, content, repo string
	}{
		{MemoryTypeEpisodic, "session-1", "build failed on linux", "stoke"},
		{MemoryTypeSemantic, "go-style", "packages are lowercase", "stoke"},
		{MemoryTypeProcedural, "run-tests", "always go test -race", "stoke"},
	}
	for _, r := range rows {
		id, err := s.StoreMemory(r.mtype, r.key, r.content, r.repo, nil)
		if err != nil {
			t.Fatalf("StoreMemory(%s): %v", r.key, err)
		}
		if id <= 0 {
			t.Fatalf("StoreMemory(%s) returned non-positive id %d", r.key, id)
		}
	}

	// Unfiltered list: all 3, newest first.
	all, err := s.ListMemories(nil, "", 0)
	if err != nil {
		t.Fatalf("ListMemories all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("len(all)=%d, want 3", len(all))
	}
	if all[0].Key != "run-tests" || all[2].Key != "session-1" {
		t.Errorf("unexpected order: %q ... %q", all[0].Key, all[2].Key)
	}

	// Filter by a single type.
	onlySem, err := s.ListMemories([]string{MemoryTypeSemantic}, "", 0)
	if err != nil {
		t.Fatalf("ListMemories semantic: %v", err)
	}
	if len(onlySem) != 1 || onlySem[0].Key != "go-style" {
		t.Errorf("semantic filter wrong: %+v", onlySem)
	}

	// Filter by multiple types.
	nonProc, err := s.ListMemories([]string{MemoryTypeEpisodic, MemoryTypeSemantic}, "", 0)
	if err != nil {
		t.Fatalf("ListMemories multi: %v", err)
	}
	if len(nonProc) != 2 {
		t.Errorf("multi-type filter len=%d, want 2", len(nonProc))
	}
}

func TestSearchMemoriesFTS(t *testing.T) {
	s := newMemoryStore(t)

	_, err := s.StoreMemory(MemoryTypeSemantic, "alpha", "the quick brown fox jumps", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.StoreMemory(MemoryTypeSemantic, "beta", "sluggish tortoise plods along", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.StoreMemory(MemoryTypeSemantic, "gamma", "foxhound tracks faint scent", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Distinct-word match: a word that only appears in alpha's content.
	hits, err := s.SearchMemories("jumps", nil, "", 0)
	if err != nil {
		t.Fatalf("SearchMemories jumps: %v", err)
	}
	if len(hits) != 1 || hits[0].Key != "alpha" {
		t.Errorf("jumps should win alpha, got %+v", hits)
	}

	// FTS-only assertion: with real FTS5, the tokenizer splits on word
	// boundaries so a bare `fox` query matches "alpha" but not "foxhound"
	// in "gamma". The LIKE fallback is substring-based and matches both.
	if s.hasFTS {
		foxHits, err := s.SearchMemories("fox", nil, "", 0)
		if err != nil {
			t.Fatalf("SearchMemories fox: %v", err)
		}
		if len(foxHits) != 1 || foxHits[0].Key != "alpha" {
			t.Errorf("fts fox should win alpha, got %+v", foxHits)
		}
	}

	// Prefix match: "fox*" should match both "fox" (alpha) and "foxhound"
	// (gamma). This works under both FTS5 (prefix operator) and LIKE
	// (rewritten to `fox%`).
	prefixHits, err := s.SearchMemories("fox*", nil, "", 0)
	if err != nil {
		t.Fatalf("SearchMemories fox*: %v", err)
	}
	gotKeys := map[string]bool{}
	for _, h := range prefixHits {
		gotKeys[h.Key] = true
	}
	if !gotKeys["alpha"] || !gotKeys["gamma"] {
		t.Errorf("fox* should match alpha+gamma, got %v (len=%d)", gotKeys, len(prefixHits))
	}
	if gotKeys["beta"] {
		t.Errorf("fox* should not match beta (tortoise), got %+v", prefixHits)
	}

	// No-match query returns empty.
	none, err := s.SearchMemories("penguin", nil, "", 0)
	if err != nil {
		t.Fatalf("SearchMemories penguin: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("penguin hits=%d, want 0", len(none))
	}

	// Empty query is a no-op (returns nil, nil).
	empty, err := s.SearchMemories("", nil, "", 0)
	if err != nil {
		t.Fatalf("SearchMemories empty: %v", err)
	}
	if empty != nil {
		t.Errorf("empty query should return nil, got %+v", empty)
	}
}

func TestSearchMemoriesByRepo(t *testing.T) {
	s := newMemoryStore(t)

	// Global (repo="") and two different repo scopes sharing the word "deploy".
	if _, err := s.StoreMemory(MemoryTypeProcedural, "g1", "deploy via make", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StoreMemory(MemoryTypeProcedural, "s1", "deploy stoke via ci", "stoke", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StoreMemory(MemoryTypeProcedural, "o1", "deploy other via helm", "other", nil); err != nil {
		t.Fatal(err)
	}

	// Search scoped to "stoke" only.
	stokeHits, err := s.SearchMemories("deploy", nil, "stoke", 0)
	if err != nil {
		t.Fatalf("SearchMemories stoke: %v", err)
	}
	if len(stokeHits) != 1 || stokeHits[0].Key != "s1" {
		t.Errorf("stoke filter returned %+v, want one row key=s1", stokeHits)
	}

	// Search scoped to "other".
	otherHits, err := s.SearchMemories("deploy", nil, "other", 0)
	if err != nil {
		t.Fatalf("SearchMemories other: %v", err)
	}
	if len(otherHits) != 1 || otherHits[0].Key != "o1" {
		t.Errorf("other filter returned %+v, want one row key=o1", otherHits)
	}

	// Search with no repo filter: all three match.
	all, err := s.SearchMemories("deploy", nil, "", 0)
	if err != nil {
		t.Fatalf("SearchMemories any-repo: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("any-repo hits=%d, want 3", len(all))
	}

	// ListMemories also honors the repo filter.
	listStoke, err := s.ListMemories(nil, "stoke", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(listStoke) != 1 || listStoke[0].Repo != "stoke" {
		t.Errorf("ListMemories(repo=stoke) = %+v", listStoke)
	}
}

func TestDeleteMemoryTombstonesFTS(t *testing.T) {
	s := newMemoryStore(t)

	id1, err := s.StoreMemory(MemoryTypeEpisodic, "k1", "phoenix firebird rising", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.StoreMemory(MemoryTypeEpisodic, "k2", "phoenix nesting quietly", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	before, err := s.SearchMemories("phoenix", nil, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 2 {
		t.Fatalf("before delete hits=%d, want 2", len(before))
	}

	if err := s.DeleteMemory(id1); err != nil {
		t.Fatalf("DeleteMemory: %v", err)
	}

	// ListMemories should not return the deleted row.
	list, err := s.ListMemories(nil, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range list {
		if m.ID == id1 {
			t.Errorf("ListMemories returned deleted id %d", id1)
		}
	}

	// SearchMemories should not return a stale FTS hit for the word "firebird"
	// (which only existed in the deleted row).
	stale, err := s.SearchMemories("firebird", nil, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 0 {
		t.Errorf("stale FTS hit after delete: %+v", stale)
	}

	// But the surviving row still matches.
	after, err := s.SearchMemories("phoenix", nil, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 || after[0].Key != "k2" {
		t.Errorf("after delete hits=%+v, want only k2", after)
	}
}

func TestMemoryMetadataRoundTrip(t *testing.T) {
	s := newMemoryStore(t)

	meta := map[string]string{
		"source":    "meta-reasoner",
		"severity":  "high",
		"unicode":   "héllo wörld",
		"with:punc": "a,b;c",
	}
	id, err := s.StoreMemory(MemoryTypeProcedural, "rule-1", "always run go vet", "stoke", meta)
	if err != nil {
		t.Fatalf("StoreMemory: %v", err)
	}

	rows, err := s.ListMemories(nil, "stoke", 0)
	if err != nil {
		t.Fatal(err)
	}
	var got *Memory
	for i := range rows {
		if rows[i].ID == id {
			got = &rows[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("row id=%d not found", id)
	}
	if !reflect.DeepEqual(got.Metadata, meta) {
		t.Errorf("metadata roundtrip mismatch:\n got %#v\nwant %#v", got.Metadata, meta)
	}

	// Nil metadata should round-trip as nil (not an empty map).
	id2, err := s.StoreMemory(MemoryTypeProcedural, "rule-2", "no meta here", "stoke", nil)
	if err != nil {
		t.Fatal(err)
	}
	rows, err = s.ListMemories(nil, "stoke", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.ID == id2 && r.Metadata != nil {
			t.Errorf("nil metadata became %+v", r.Metadata)
		}
	}
}

func TestWisdomLearningsUnaffected(t *testing.T) {
	s := newMemoryStore(t)

	// Populate wisdom_learnings via the pre-existing API.
	s.Record("task-7", Learning{
		Category:       Gotcha,
		Description:    "goroutine leak in websocket handler",
		File:           "ws/server.go",
		FailurePattern: "ws-leak-001",
	})
	if s.Count() != 1 {
		t.Fatalf("wisdom count=%d, want 1 before memory writes", s.Count())
	}

	// Interleave memory writes across all three types.
	if _, err := s.StoreMemory(MemoryTypeEpisodic, "e", "session A completed", "stoke", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StoreMemory(MemoryTypeSemantic, "s", "websocket code lives in ws/", "stoke", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StoreMemory(MemoryTypeProcedural, "p", "close channels in defer", "stoke", nil); err != nil {
		t.Fatal(err)
	}

	// Wisdom row is still there, still has the right data, still queryable
	// via both Learnings() and FindByPattern().
	if s.Count() != 1 {
		t.Errorf("wisdom count after memory writes=%d, want 1", s.Count())
	}
	ls := s.Learnings()
	if len(ls) != 1 || ls[0].Description != "goroutine leak in websocket handler" {
		t.Errorf("wisdom learnings corrupted: %+v", ls)
	}
	if found := s.FindByPattern("ws-leak-001"); found == nil || found.TaskID != "task-7" {
		t.Errorf("FindByPattern after memory writes: %+v", found)
	}

	// And the memory side is healthy too.
	mem, err := s.ListMemories(nil, "stoke", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(mem) != 3 {
		t.Errorf("memory count=%d, want 3", len(mem))
	}
}
