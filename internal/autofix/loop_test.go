package autofix

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseOutput(t *testing.T) {
	output := `main.go:10:5: undefined: foo
main.go:20: unused variable bar
utils.go:3:1: warning: exported function without comment`

	issues := ParseOutput(output)
	if len(issues) != 3 {
		t.Fatalf("expected 3 issues, got %d", len(issues))
	}

	if issues[0].File != "main.go" || issues[0].Line != 10 || issues[0].Column != 5 {
		t.Errorf("issue 0: %+v", issues[0])
	}
	if issues[1].File != "main.go" || issues[1].Line != 20 {
		t.Errorf("issue 1: %+v", issues[1])
	}
	if issues[2].Level != "warning" {
		t.Errorf("expected warning level, got %s", issues[2].Level)
	}
}

func TestParseEmpty(t *testing.T) {
	issues := ParseOutput("")
	if len(issues) != 0 {
		t.Errorf("expected 0 issues, got %d", len(issues))
	}
}

func TestLoopCleanOnFirstRun(t *testing.T) {
	result := Loop(
		[]string{"main.go"},
		func(name string, args ...string) (string, error) { return "", nil },
		func(issue Issue) error { return nil },
		DefaultLoopConfig(),
	)

	if !result.Clean {
		t.Error("expected clean")
	}
	if result.Iterations != 1 {
		t.Errorf("expected 1 iteration, got %d", result.Iterations)
	}
}

func TestLoopFixesIssues(t *testing.T) {
	iteration := 0
	result := Loop(
		[]string{"main.go"},
		func(name string, args ...string) (string, error) {
			iteration++
			if iteration <= 1 {
				return "main.go:10:5: undefined: foo", nil
			}
			return "", nil // clean after fix
		},
		func(issue Issue) error { return nil },
		DefaultLoopConfig(),
	)

	if !result.Clean {
		t.Error("expected clean after fix")
	}
	if result.IssuesBefore != 1 {
		t.Errorf("expected 1 issue before, got %d", result.IssuesBefore)
	}
	if result.Fixed != 1 {
		t.Errorf("expected 1 fixed, got %d", result.Fixed)
	}
}

func TestLoopMaxIterations(t *testing.T) {
	result := Loop(
		[]string{"main.go"},
		func(name string, args ...string) (string, error) {
			return "main.go:10:5: persistent error", nil
		},
		func(issue Issue) error { return nil },
		LoopConfig{MaxIterations: 2},
	)

	if result.Clean {
		t.Error("should not be clean")
	}
	if result.Iterations != 2 {
		t.Errorf("expected 2 iterations, got %d", result.Iterations)
	}
	if len(result.Remaining) != 1 {
		t.Errorf("expected 1 remaining, got %d", len(result.Remaining))
	}
}

func TestLoopSkipsWarnings(t *testing.T) {
	result := Loop(
		[]string{"main.go"},
		func(name string, args ...string) (string, error) {
			return "main.go:10:5: warning: unused variable", nil
		},
		func(issue Issue) error { return nil },
		LoopConfig{MaxIterations: 1, FixWarnings: false},
	)

	if !result.Clean {
		t.Error("warnings-only should be clean when FixWarnings=false")
	}
}

func TestFormatIssue(t *testing.T) {
	s := FormatIssue(Issue{File: "x.go", Line: 10, Column: 5, Message: "bad"})
	if s != "x.go:10:5: bad" {
		t.Errorf("unexpected: %s", s)
	}

	s = FormatIssue(Issue{File: "x.go", Line: 10, Message: "bad"})
	if s != "x.go:10: bad" {
		t.Errorf("unexpected: %s", s)
	}
}

func TestFormatFixPrompt(t *testing.T) {
	issues := []Issue{
		{File: "main.go", Line: 10, Message: "undefined: foo"},
		{File: "main.go", Line: 20, Message: "unused variable"},
	}
	prompt := FormatFixPrompt(issues)
	if !strings.Contains(prompt, "main.go") {
		t.Error("expected file name in prompt")
	}
	if !strings.Contains(prompt, "Line 10") {
		t.Error("expected line number in prompt")
	}
}

func TestFormatFixPromptEmpty(t *testing.T) {
	prompt := FormatFixPrompt(nil)
	if prompt != "" {
		t.Errorf("expected empty, got %q", prompt)
	}
}

func TestFilterErrors(t *testing.T) {
	issues := []Issue{
		{Level: "error", Message: "bad"},
		{Level: "warning", Message: "meh"},
		{Level: "error", Message: "also bad"},
	}
	filtered := filterErrors(issues)
	if len(filtered) != 2 {
		t.Errorf("expected 2 errors, got %d", len(filtered))
	}
}

func TestLoopFixWarnings(t *testing.T) {
	iteration := 0
	result := Loop(
		[]string{"main.go"},
		func(name string, args ...string) (string, error) {
			iteration++
			if iteration <= 1 {
				return "main.go:10:5: warning: unused variable", nil
			}
			return "", nil
		},
		func(issue Issue) error { return nil },
		LoopConfig{MaxIterations: 3, FixWarnings: true},
	)
	_ = fmt.Sprintf("result: %+v", result) // use fmt
	if !result.Clean {
		t.Error("expected clean after fixing warnings")
	}
}
