package wisdom

import (
	"strings"
	"sync"
	"testing"
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
