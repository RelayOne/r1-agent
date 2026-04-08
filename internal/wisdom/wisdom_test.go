package wisdom

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRecordAndLearnings(t *testing.T) {
	s := NewStore()
	s.Record("TASK-1", Learning{Category: Gotcha, Description: "pnpm install required"})
	s.Record("TASK-2", Learning{Category: Decision, Description: "use parameterized queries"})

	got := s.Learnings()
	if len(got) != 2 {
		t.Fatalf("expected 2 learnings, got %d", len(got))
	}
	if got[0].TaskID != "TASK-1" {
		t.Errorf("expected TaskID TASK-1, got %s", got[0].TaskID)
	}
	if got[1].TaskID != "TASK-2" {
		t.Errorf("expected TaskID TASK-2, got %s", got[1].TaskID)
	}
}

func TestRecordSetsTaskID(t *testing.T) {
	s := NewStore()
	// Even if the caller sets TaskID on the Learning, Record overwrites it.
	s.Record("TASK-5", Learning{TaskID: "ignored", Category: Pattern, Description: "test"})
	got := s.Learnings()
	if got[0].TaskID != "TASK-5" {
		t.Errorf("expected TaskID TASK-5, got %s", got[0].TaskID)
	}
}

func TestForPromptEmpty(t *testing.T) {
	s := NewStore()
	if out := s.ForPrompt(); out != "" {
		t.Errorf("expected empty string for empty store, got %q", out)
	}
}

func TestForPromptContainsHeader(t *testing.T) {
	s := NewStore()
	s.Record("T1", Learning{Category: Pattern, Description: "uses DI"})
	out := s.ForPrompt()
	if !strings.HasPrefix(out, "## Learnings from previous tasks\n") {
		t.Errorf("missing header in output:\n%s", out)
	}
}

func TestForPromptPrioritizesGotchas(t *testing.T) {
	s := NewStore()
	s.Record("T1", Learning{Category: Pattern, Description: "pattern first recorded"})
	s.Record("T2", Learning{Category: Gotcha, Description: "gotcha second recorded"})
	s.Record("T3", Learning{Category: Decision, Description: "decision third recorded"})

	out := s.ForPrompt()
	gotchaIdx := strings.Index(out, "[gotcha]")
	decisionIdx := strings.Index(out, "[decision]")
	patternIdx := strings.Index(out, "[pattern]")

	if gotchaIdx == -1 || decisionIdx == -1 || patternIdx == -1 {
		t.Fatalf("expected all categories in output:\n%s", out)
	}
	if gotchaIdx > decisionIdx {
		t.Error("gotcha should appear before decision")
	}
	if decisionIdx > patternIdx {
		t.Error("decision should appear before pattern")
	}
}

func TestForPromptIncludesFile(t *testing.T) {
	s := NewStore()
	s.Record("T1", Learning{Category: Gotcha, Description: "missing import", File: "main.go"})
	out := s.ForPrompt()
	if !strings.Contains(out, "(main.go)") {
		t.Errorf("expected file reference in output:\n%s", out)
	}
}

func TestForPromptRespectsMaxLength(t *testing.T) {
	s := NewStore()
	// Add many learnings to exceed the 500 char cap.
	for i := 0; i < 50; i++ {
		s.Record("TASK-99", Learning{
			Category:    Gotcha,
			Description: "this is a fairly long description that takes up space in the prompt",
		})
	}
	out := s.ForPrompt()
	if len(out) > maxPromptLen {
		t.Errorf("ForPrompt output is %d chars, expected <= %d", len(out), maxPromptLen)
	}
	// Should still have at least the header and one entry.
	if !strings.Contains(out, "[gotcha]") {
		t.Error("expected at least one gotcha in truncated output")
	}
}

func TestForPromptLineFormat(t *testing.T) {
	s := NewStore()
	s.Record("TASK-1", Learning{Category: Gotcha, Description: "pnpm install required"})
	out := s.ForPrompt()
	expected := "- [gotcha] Task TASK-1: pnpm install required"
	if !strings.Contains(out, expected) {
		t.Errorf("expected line %q in output:\n%s", expected, out)
	}
}

func TestCategoryString(t *testing.T) {
	tests := []struct {
		cat  Category
		want string
	}{
		{Gotcha, "gotcha"},
		{Decision, "decision"},
		{Pattern, "pattern"},
		{Category(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.cat.String(); got != tt.want {
			t.Errorf("Category(%d).String() = %q, want %q", tt.cat, got, tt.want)
		}
	}
}

func TestParseCategory(t *testing.T) {
	tests := []struct {
		input string
		want  Category
	}{
		{"gotcha", Gotcha},
		{"GOTCHA", Gotcha},
		{"decision", Decision},
		{"Decision", Decision},
		{"pattern", Pattern},
		{"PATTERN", Pattern},
		{"unknown", Pattern}, // default
		{"", Pattern},        // default
	}
	for _, tt := range tests {
		if got := ParseCategory(tt.input); got != tt.want {
			t.Errorf("ParseCategory(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestFindByPattern(t *testing.T) {
	s := NewStore()
	s.Record("T1", Learning{Category: Gotcha, Description: "build broke", FailurePattern: "abc123"})
	s.Record("T2", Learning{Category: Decision, Description: "used mutex"})

	// Match existing pattern
	if got := s.FindByPattern("abc123"); got == nil {
		t.Fatal("expected match for pattern abc123")
	} else if got.TaskID != "T1" {
		t.Errorf("expected TaskID T1, got %s", got.TaskID)
	}

	// No match
	if got := s.FindByPattern("nonexistent"); got != nil {
		t.Errorf("expected nil for nonexistent pattern, got %+v", got)
	}

	// Empty hash
	if got := s.FindByPattern(""); got != nil {
		t.Errorf("expected nil for empty hash, got %+v", got)
	}
}

func TestConcurrentAccess(t *testing.T) {
	s := NewStore()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			s.Record("T1", Learning{Category: Gotcha, Description: "concurrent write"})
			_ = s.ForPrompt()
			_ = s.Learnings()
		}(i)
	}
	wg.Wait()
	got := s.Learnings()
	if len(got) != 100 {
		t.Errorf("expected 100 learnings after concurrent writes, got %d", len(got))
	}
}

func TestAsOf(t *testing.T) {
	s := NewStore()

	now := time.Now()
	past := now.Add(-48 * time.Hour)
	recent := now.Add(-1 * time.Hour)
	future := now.Add(24 * time.Hour)

	// Record learnings at different times
	s.Record("T1", Learning{
		Category:    Gotcha,
		Description: "old learning",
		ValidFrom:   past,
	})
	expiry := now.Add(-12 * time.Hour)
	s.Record("T2", Learning{
		Category:    Decision,
		Description: "expired learning",
		ValidFrom:   past,
		ValidUntil:  &expiry,
	})
	s.Record("T3", Learning{
		Category:    Pattern,
		Description: "recent learning",
		ValidFrom:   recent,
	})
	s.Record("T4", Learning{
		Category:    Pattern,
		Description: "future learning",
		ValidFrom:   future,
	})

	// Query as of now: should see T1 (old, still valid), T3 (recent), not T2 (expired) or T4 (future)
	result := s.AsOf(now)
	if len(result) != 2 {
		t.Fatalf("AsOf(now) returned %d learnings, want 2", len(result))
	}

	descriptions := map[string]bool{}
	for _, l := range result {
		descriptions[l.Description] = true
	}
	if !descriptions["old learning"] {
		t.Error("expected 'old learning' in AsOf(now)")
	}
	if !descriptions["recent learning"] {
		t.Error("expected 'recent learning' in AsOf(now)")
	}
	if descriptions["expired learning"] {
		t.Error("'expired learning' should not be in AsOf(now)")
	}

	// Query 24 hours ago: should see T1 (old, still valid) and T2 (not yet expired at -24h)
	oneDayAgo := now.Add(-24 * time.Hour)
	oldResult := s.AsOf(oneDayAgo)
	if len(oldResult) != 2 {
		t.Fatalf("AsOf(24h ago) returned %d learnings, want 2 (T1+T2)", len(oldResult))
	}
}

func TestInvalidate(t *testing.T) {
	s := NewStore()
	s.Record("T1", Learning{Category: Gotcha, Description: "will be invalidated"})

	now := time.Now()
	ok := s.Invalidate("T1", "will be invalidated", now)
	if !ok {
		t.Fatal("Invalidate should return true for existing learning")
	}

	// The learning should now have ValidUntil set
	learnings := s.Learnings()
	if len(learnings) != 1 {
		t.Fatalf("expected 1 learning, got %d", len(learnings))
	}
	if learnings[0].ValidUntil == nil {
		t.Fatal("ValidUntil should be set after Invalidate")
	}

	// Should not appear in AsOf after invalidation
	result := s.AsOf(now.Add(1 * time.Second))
	if len(result) != 0 {
		t.Errorf("invalidated learning should not appear in AsOf, got %d", len(result))
	}

	// Should not find non-existent
	ok = s.Invalidate("T99", "nonexistent", now)
	if ok {
		t.Error("Invalidate should return false for non-existent learning")
	}
}
