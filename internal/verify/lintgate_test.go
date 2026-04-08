package verify

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLintGateShouldGate(t *testing.T) {
	lg := NewLintGate()

	tests := []struct {
		file string
		want bool
	}{
		{"main.go", true},
		{"app.ts", true},
		{"style.css", false},
		{"data.json", true},
		{"config.yaml", true},
		{"script.py", true},
		{"lib.rs", true},
		{"readme.md", false},
	}

	for _, tc := range tests {
		got := lg.ShouldGate(tc.file)
		if got != tc.want {
			t.Errorf("ShouldGate(%q) = %v, want %v", tc.file, got, tc.want)
		}
	}
}

func TestLintGateSkipUnknown(t *testing.T) {
	lg := NewLintGate()
	result := lg.Check(nil, "/tmp", "readme.md")
	if !result.Skipped {
		t.Error("expected skipped for unknown file type")
	}
	if !result.Passed {
		t.Error("skipped files should pass")
	}
}

func TestLintGateSetLinter(t *testing.T) {
	lg := NewLintGate()
	lg.SetLinter(".css", "stylelint %s")
	if !lg.ShouldGate("style.css") {
		t.Error("expected CSS to be gated after SetLinter")
	}
}

func TestFormatRejection(t *testing.T) {
	results := []LintResult{
		{File: "main.go", Passed: true},
		{File: "handler.go", Passed: false, Output: "undefined: foo"},
		{File: "readme.md", Passed: true, Skipped: true},
	}

	msg := FormatRejection(results)
	if !strings.Contains(msg, "EDIT REJECTED") {
		t.Error("expected rejection header")
	}
	if !strings.Contains(msg, "handler.go") {
		t.Error("expected failing file")
	}
	if !strings.Contains(msg, "undefined: foo") {
		t.Error("expected error output")
	}
	if strings.Contains(msg, "main.go") {
		t.Error("passing files should not appear in rejection")
	}
}

func TestCheckMultiple(t *testing.T) {
	lg := NewLintGate()

	// Test with files that have no linter (all should skip/pass)
	results, allPassed := lg.CheckMultiple(nil, "/tmp", []string{"a.md", "b.txt"})
	if !allPassed {
		t.Error("unknown files should all pass")
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}
}

func TestLintGateExtDetection(t *testing.T) {
	lg := NewLintGate()

	ext := filepath.Ext("src/components/App.tsx")
	if ext != ".tsx" {
		t.Errorf("expected .tsx, got %s", ext)
	}
	if !lg.ShouldGate("src/components/App.tsx") {
		t.Error("expected TSX to be gated")
	}
}
