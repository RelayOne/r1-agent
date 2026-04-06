package wisdom

import (
	"os"
	"path/filepath"
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
