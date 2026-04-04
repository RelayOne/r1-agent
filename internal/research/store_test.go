package research

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// --- CRUD ---

func TestAddAndGet(t *testing.T) {
	s := newTestStore(t)

	e := &Entry{
		ID:        "r-1",
		MissionID: "m-auth",
		Topic:     "jwt-authentication",
		Query:     "How does JWT token validation work in Go?",
		Content:   "Use the golang-jwt library. Parse token with jwt.Parse(), validate claims...",
		Source:    "https://pkg.go.dev/github.com/golang-jwt/jwt",
		Tags:      []string{"go", "jwt", "auth"},
	}
	if err := s.Add(e); err != nil {
		t.Fatalf("Add: %v", err)
	}

	got, err := s.Get("r-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nil")
	}
	if got.Topic != "jwt-authentication" {
		t.Errorf("Topic = %q", got.Topic)
	}
	if got.Query != e.Query {
		t.Errorf("Query mismatch")
	}
	if got.Content != e.Content {
		t.Errorf("Content mismatch")
	}
	if got.Source != e.Source {
		t.Errorf("Source = %q", got.Source)
	}
	if len(got.Tags) != 3 || got.Tags[0] != "go" {
		t.Errorf("Tags = %v", got.Tags)
	}
	if got.MissionID != "m-auth" {
		t.Errorf("MissionID = %q", got.MissionID)
	}
	// Get increments use count
	if got.UseCount != 1 {
		t.Errorf("UseCount = %d, want 1 (incremented by Get)", got.UseCount)
	}
}

func TestAddUpsert(t *testing.T) {
	s := newTestStore(t)

	s.Add(&Entry{ID: "dup", Topic: "t", Query: "q", Content: "v1"})
	s.Add(&Entry{ID: "dup", Topic: "t", Query: "q", Content: "v2"})

	got, _ := s.Get("dup")
	if got.Content != "v2" {
		t.Errorf("upsert should update content, got %q", got.Content)
	}
	// Use count: initial 0, upsert increments to 1, Get increments to 2
	if got.UseCount != 2 {
		t.Errorf("UseCount = %d, want 2 (1 from upsert + 1 from Get)", got.UseCount)
	}
}

func TestAddValidation(t *testing.T) {
	s := newTestStore(t)
	tests := []struct {
		name string
		e    *Entry
	}{
		{"empty ID", &Entry{ID: "", Topic: "t", Content: "c"}},
		{"empty topic", &Entry{ID: "x", Topic: "", Content: "c"}},
		{"empty content", &Entry{ID: "x", Topic: "t", Content: ""}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.Add(tc.e); err == nil {
				t.Error("should reject invalid entry")
			}
		})
	}
}

func TestGetNonexistent(t *testing.T) {
	s := newTestStore(t)
	got, err := s.Get("ghost")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Error("should return nil for missing entry")
	}
}

func TestDelete(t *testing.T) {
	s := newTestStore(t)
	s.Add(&Entry{ID: "del", Topic: "t", Query: "q", Content: "c"})
	if err := s.Delete("del"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get("del")
	if got != nil {
		t.Error("should be gone after delete")
	}
}

func TestDeleteNonexistent(t *testing.T) {
	s := newTestStore(t)
	err := s.Delete("ghost")
	if err == nil {
		t.Error("should error deleting nonexistent entry")
	}
}

// --- Search ---

func TestFullTextSearch(t *testing.T) {
	s := newTestStore(t)

	s.Add(&Entry{ID: "r-1", Topic: "auth", Query: "JWT validation", Content: "Use golang-jwt library for token parsing and validation"})
	s.Add(&Entry{ID: "r-2", Topic: "auth", Query: "OAuth flow", Content: "Implement OAuth2 authorization code flow with PKCE"})
	s.Add(&Entry{ID: "r-3", Topic: "database", Query: "Connection pooling", Content: "Use sql.DB with SetMaxOpenConns for connection management"})

	results, err := s.Search("JWT token", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("should find JWT-related results")
	}
	// JWT result should rank higher than OAuth
	if results[0].Entry.ID != "r-1" {
		t.Errorf("expected r-1 as top result, got %q", results[0].Entry.ID)
	}
	if results[0].Score <= 0 {
		t.Error("score should be positive")
	}
}

func TestSearchNoResults(t *testing.T) {
	s := newTestStore(t)
	s.Add(&Entry{ID: "r-1", Topic: "auth", Query: "JWT", Content: "JWT stuff"})

	results, _ := s.Search("kubernetes deployment", 10)
	if len(results) > 0 {
		t.Error("should return empty for unmatched query")
	}
}

func TestSearchEmptyQuery(t *testing.T) {
	s := newTestStore(t)
	results, err := s.Search("", 10)
	if err != nil {
		t.Fatal(err)
	}
	if results != nil {
		t.Error("empty query should return nil")
	}
}

// --- Topic-based Retrieval ---

func TestByTopic(t *testing.T) {
	s := newTestStore(t)
	s.Add(&Entry{ID: "a1", Topic: "auth", Query: "q1", Content: "c1"})
	s.Add(&Entry{ID: "a2", Topic: "auth", Query: "q2", Content: "c2"})
	s.Add(&Entry{ID: "d1", Topic: "database", Query: "q3", Content: "c3"})

	auth, _ := s.ByTopic("auth")
	if len(auth) != 2 {
		t.Errorf("auth entries = %d, want 2", len(auth))
	}

	db, _ := s.ByTopic("database")
	if len(db) != 1 {
		t.Errorf("database entries = %d, want 1", len(db))
	}

	empty, _ := s.ByTopic("nonexistent")
	if len(empty) != 0 {
		t.Error("should return empty for nonexistent topic")
	}
}

func TestByMission(t *testing.T) {
	s := newTestStore(t)
	s.Add(&Entry{ID: "m1-a", MissionID: "m-1", Topic: "t", Query: "q", Content: "for mission 1"})
	s.Add(&Entry{ID: "m1-b", MissionID: "m-1", Topic: "t", Query: "q", Content: "also for mission 1"})
	s.Add(&Entry{ID: "m2-a", MissionID: "m-2", Topic: "t", Query: "q", Content: "for mission 2"})

	m1, _ := s.ByMission("m-1")
	if len(m1) != 2 {
		t.Errorf("mission 1 entries = %d, want 2", len(m1))
	}
}

func TestTopics(t *testing.T) {
	s := newTestStore(t)
	s.Add(&Entry{ID: "a1", Topic: "auth", Query: "q", Content: "c"})
	s.Add(&Entry{ID: "a2", Topic: "auth", Query: "q", Content: "c"})
	s.Add(&Entry{ID: "d1", Topic: "database", Query: "q", Content: "c"})

	topics, err := s.Topics()
	if err != nil {
		t.Fatal(err)
	}
	if topics["auth"] != 2 {
		t.Errorf("auth count = %d", topics["auth"])
	}
	if topics["database"] != 1 {
		t.Errorf("database count = %d", topics["database"])
	}
}

// --- Deduplication ---

func TestHasResearch(t *testing.T) {
	s := newTestStore(t)
	s.Add(&Entry{ID: "r-1", Topic: "auth", Query: "JWT validation", Content: "content"})

	has, _ := s.HasResearch("auth", "JWT validation")
	if !has {
		t.Error("should find existing research")
	}

	has, _ = s.HasResearch("auth", "OAuth flow")
	if has {
		t.Error("should not find non-existing research")
	}
}

// --- Count ---

func TestCount(t *testing.T) {
	s := newTestStore(t)
	count, _ := s.Count()
	if count != 0 {
		t.Errorf("empty store count = %d", count)
	}

	s.Add(&Entry{ID: "a", Topic: "t", Query: "q", Content: "c"})
	s.Add(&Entry{ID: "b", Topic: "t", Query: "q", Content: "c"})

	count, _ = s.Count()
	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}
}

// --- Prune ---

func TestPrune(t *testing.T) {
	s := newTestStore(t)

	old := time.Now().Add(-48 * time.Hour)
	s.Add(&Entry{ID: "old", Topic: "t", Query: "q", Content: "old stuff", CreatedAt: old})
	s.Add(&Entry{ID: "new", Topic: "t", Query: "q", Content: "new stuff"})

	// Force the old entry's updated_at to be old
	s.db.Exec("UPDATE entries SET updated_at=? WHERE id='old'", old.Format(time.RFC3339Nano))

	cutoff := time.Now().Add(-24 * time.Hour)
	removed, err := s.Prune(cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("pruned %d, want 1", removed)
	}

	count, _ := s.Count()
	if count != 1 {
		t.Errorf("count after prune = %d, want 1", count)
	}
}

func TestPruneKeepsFrequentlyUsed(t *testing.T) {
	s := newTestStore(t)

	old := time.Now().Add(-48 * time.Hour)
	s.Add(&Entry{ID: "old-used", Topic: "t", Query: "q", Content: "old but valuable"})
	// Set old timestamp and high use count
	s.db.Exec("UPDATE entries SET updated_at=?, use_count=5 WHERE id='old-used'",
		old.Format(time.RFC3339Nano))

	cutoff := time.Now().Add(-24 * time.Hour)
	removed, _ := s.Prune(cutoff)
	if removed != 0 {
		t.Error("should not prune frequently-used entries")
	}
}

// --- Persistence ---

func TestStoreReopen(t *testing.T) {
	dir := t.TempDir()

	s1, _ := NewStore(dir)
	s1.Add(&Entry{ID: "persist", Topic: "t", Query: "q", Content: "survives restart"})
	s1.Close()

	s2, _ := NewStore(dir)
	defer s2.Close()

	got, _ := s2.Get("persist")
	if got == nil {
		t.Fatal("entry should survive store reopen")
	}
	if got.Content != "survives restart" {
		t.Errorf("content = %q", got.Content)
	}
}

// --- Concurrency ---

func TestConcurrentAdds(t *testing.T) {
	s := newTestStore(t)
	var wg sync.WaitGroup
	errs := make(chan error, 30)

	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if err := s.Add(&Entry{
				ID:      fmt.Sprintf("c-%d", n),
				Topic:   "concurrent",
				Query:   fmt.Sprintf("query %d", n),
				Content: fmt.Sprintf("content %d", n),
			}); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent add error: %v", err)
	}

	count, _ := s.Count()
	if count != 30 {
		t.Errorf("count = %d, want 30", count)
	}
}

func TestConcurrentSearchAndAdd(t *testing.T) {
	s := newTestStore(t)
	// Seed some data
	for i := 0; i < 10; i++ {
		s.Add(&Entry{ID: fmt.Sprintf("seed-%d", i), Topic: "test", Query: "query", Content: fmt.Sprintf("content number %d about testing", i)})
	}

	var wg sync.WaitGroup
	// Concurrent reads and writes
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func(n int) {
			defer wg.Done()
			s.Add(&Entry{ID: fmt.Sprintf("new-%d", n), Topic: "test", Query: "q", Content: "new content"})
		}(i)
		go func() {
			defer wg.Done()
			s.Search("testing", 5)
		}()
	}
	wg.Wait()
	// Just verify no panics or deadlocks
}

// --- Edge Cases ---

func TestNewStoreEmptyDir(t *testing.T) {
	_, err := NewStore("")
	if err == nil {
		t.Error("should reject empty directory")
	}
}

func TestSearchSpecialCharacters(t *testing.T) {
	s := newTestStore(t)
	s.Add(&Entry{ID: "special", Topic: "test", Query: "special chars", Content: `content with "quotes" and 'apostrophes' and (parens)`})

	// Should not crash on special characters in search
	results, err := s.Search(`"quotes" (parens)`, 10)
	if err != nil {
		t.Fatalf("search with special chars: %v", err)
	}
	_ = results // just verify no crash
}
