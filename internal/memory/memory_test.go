package memory

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRemember(t *testing.T) {
	s, _ := NewStore(Config{})
	entry := s.Remember(CatGotcha, "Always check nil before accessing map", "go", "nil")

	if entry.ID == "" {
		t.Error("should assign ID")
	}
	if entry.Category != CatGotcha {
		t.Error("should set category")
	}
	if s.Count() != 1 {
		t.Errorf("expected 1 entry, got %d", s.Count())
	}
}

func TestRememberWithContext(t *testing.T) {
	s, _ := NewStore(Config{})
	entry := s.RememberWithContext(CatFix, "Use strings.Builder", "optimizing string concat", "main.go", "performance")

	if entry.Context != "optimizing string concat" {
		t.Error("should set context")
	}
	if entry.File != "main.go" {
		t.Error("should set file")
	}
}

func TestRecall(t *testing.T) {
	s, _ := NewStore(Config{})
	s.Remember(CatGotcha, "nil map panic in Go", "go")
	s.Remember(CatPattern, "use table-driven tests", "testing")
	s.Remember(CatFact, "database uses PostgreSQL", "database")

	results := s.Recall("go nil map", 10)
	if len(results) == 0 {
		t.Fatal("should find relevant memories")
	}
	if results[0].Content != "nil map panic in Go" {
		t.Error("most relevant should be the nil map gotcha")
	}
}

func TestRecallByCategory(t *testing.T) {
	s, _ := NewStore(Config{})
	s.Remember(CatGotcha, "gotcha 1")
	s.Remember(CatGotcha, "gotcha 2")
	s.Remember(CatPattern, "pattern 1")

	gotchas := s.RecallByCategory(CatGotcha)
	if len(gotchas) != 2 {
		t.Errorf("expected 2 gotchas, got %d", len(gotchas))
	}
}

func TestRecallForFile(t *testing.T) {
	s, _ := NewStore(Config{})
	s.RememberWithContext(CatFact, "uses custom encoder", "", "internal/stream/parser.go")
	s.RememberWithContext(CatFact, "needs API key", "", "internal/engine/claude.go")

	results := s.RecallForFile("internal/stream/parser.go")
	if len(results) != 1 {
		t.Errorf("expected 1 result, got %d", len(results))
	}
}

func TestMarkUsed(t *testing.T) {
	s, _ := NewStore(Config{})
	entry := s.Remember(CatPattern, "use defer for cleanup")
	s.MarkUsed(entry.ID)

	results := s.RecallByCategory(CatPattern)
	if len(results) == 0 || results[0].UseCount != 1 {
		t.Error("should increment use count")
	}
}

func TestForget(t *testing.T) {
	s, _ := NewStore(Config{})
	entry := s.Remember(CatGotcha, "obsolete fact")
	s.Forget(entry.ID)

	if s.Count() != 0 {
		t.Error("should remove entry")
	}
}

func TestForPrompt(t *testing.T) {
	s, _ := NewStore(Config{})
	s.Remember(CatGotcha, "nil pointer on empty slice", "go", "nil")
	s.Remember(CatPattern, "use table-driven tests", "testing")

	prompt := s.ForPrompt("go nil pointer", 500)
	if prompt == "" {
		t.Error("should generate prompt content")
	}
	if len(prompt) == 0 {
		t.Error("prompt should not be empty")
	}
}

func TestForPromptEmpty(t *testing.T) {
	s, _ := NewStore(Config{})
	prompt := s.ForPrompt("anything", 500)
	if prompt != "" {
		t.Error("empty store should return empty prompt")
	}
}

func TestForPromptBudget(t *testing.T) {
	s, _ := NewStore(Config{})
	for i := 0; i < 50; i++ {
		s.Remember(CatFact, "This is a long memory entry that takes up space")
	}

	prompt := s.ForPrompt("memory", 50)
	// Budget should limit output
	tokens := (len(prompt) + 3) / 4
	if tokens > 100 { // some slack for header
		t.Errorf("prompt should respect budget, got ~%d tokens", tokens)
	}
}

func TestDecay(t *testing.T) {
	s, _ := NewStore(Config{MaxAge: 1 * time.Millisecond})
	s.Remember(CatFact, "old fact")

	// Wait for entry to become old
	time.Sleep(5 * time.Millisecond)

	decayed := s.Decay()
	if decayed != 1 {
		t.Errorf("expected 1 decayed, got %d", decayed)
	}
}

func TestPrune(t *testing.T) {
	s, _ := NewStore(Config{})
	s.Remember(CatFact, "keep this")
	s.Remember(CatFact, "remove this")

	// Manually set low confidence
	s.mu.Lock()
	s.entries[1].Confidence = 0.05
	s.mu.Unlock()

	removed := s.Prune(0.1)
	if removed != 1 {
		t.Errorf("expected 1 pruned, got %d", removed)
	}
	if s.Count() != 1 {
		t.Errorf("expected 1 remaining, got %d", s.Count())
	}
}

func TestPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.json")

	// Create and save
	s1, _ := NewStore(Config{Path: path})
	s1.Remember(CatGotcha, "persistent fact")
	if err := s1.Save(); err != nil {
		t.Fatal(err)
	}

	// Load in new store
	s2, err := NewStore(Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if s2.Count() != 1 {
		t.Errorf("expected 1 loaded entry, got %d", s2.Count())
	}
}

func TestPersistenceEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.json")

	s, err := NewStore(Config{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if s.Count() != 0 {
		t.Error("new store should be empty")
	}
}

func TestIDUniqueness(t *testing.T) {
	s, _ := NewStore(Config{})
	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		entry := s.Remember(CatFact, "entry")
		if ids[entry.ID] {
			t.Fatalf("duplicate ID: %s", entry.ID)
		}
		ids[entry.ID] = true
	}
}

func TestCategoryPriority(t *testing.T) {
	s, _ := NewStore(Config{})
	s.Remember(CatPreference, "user likes tabs")
	s.Remember(CatGotcha, "nil map panic")
	s.Remember(CatAntiPattern, "don't use global state")

	prompt := s.ForPrompt("coding style nil map", 500)
	// Gotchas should appear before preferences in prompt
	gotchaIdx := len(prompt) // default to end
	prefIdx := 0
	for i, line := range splitLines(prompt) {
		if contains(line, "nil map") {
			gotchaIdx = i
		}
		if contains(line, "tabs") {
			prefIdx = i
		}
	}
	if gotchaIdx > prefIdx && prefIdx > 0 {
		t.Error("gotchas should appear before preferences")
	}
}

func TestSaveNoPath(t *testing.T) {
	s, _ := NewStore(Config{})
	s.Remember(CatFact, "ephemeral")
	if err := s.Save(); err != nil {
		t.Error("save with no path should be no-op")
	}
}

func TestAtomicSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.json")

	s, _ := NewStore(Config{Path: path})
	s.Remember(CatFact, "data")
	s.Save()

	// Verify .tmp doesn't linger
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("tmp file should be cleaned up")
	}
}

func splitLines(s string) []string {
	return append([]string{}, splitBy(s, '\n')...)
}

func splitBy(s string, sep byte) []string {
	var parts []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		parts = append(parts, s[start:])
	}
	return parts
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && findStr(s, sub)
}

func findStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
