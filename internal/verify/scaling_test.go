package verify

import (
	"strings"
	"testing"
)

func TestClassifyComplexity(t *testing.T) {
	tests := []struct {
		stats ChangeStats
		want  Complexity
	}{
		{ChangeStats{1, 10, 5}, ComplexitySmall},
		{ChangeStats{3, 50, 40}, ComplexitySmall},
		{ChangeStats{3, 60, 40}, ComplexityStandard}, // 100 lines, standard
		{ChangeStats{10, 200, 100}, ComplexityStandard},
		{ChangeStats{15, 300, 100}, ComplexityStandard},
		{ChangeStats{16, 100, 50}, ComplexityLarge},   // >15 files
		{ChangeStats{5, 300, 200}, ComplexityLarge},    // >500 lines
		{ChangeStats{20, 1000, 500}, ComplexityLarge},
	}

	for _, tc := range tests {
		got := ClassifyComplexity(tc.stats)
		if got != tc.want {
			t.Errorf("ClassifyComplexity(%+v) = %s, want %s", tc.stats, got, tc.want)
		}
	}
}

func TestTotalLines(t *testing.T) {
	s := ChangeStats{LinesAdded: 100, LinesRemoved: 50}
	if s.TotalLines() != 150 {
		t.Errorf("expected 150, got %d", s.TotalLines())
	}
}

func TestParseShortstat(t *testing.T) {
	tests := []struct {
		input string
		want  ChangeStats
	}{
		{" 3 files changed, 42 insertions(+), 10 deletions(-)", ChangeStats{3, 42, 10}},
		{" 1 file changed, 5 insertions(+)", ChangeStats{1, 5, 0}},
		{" 10 files changed, 100 deletions(-)", ChangeStats{10, 0, 100}},
		{"", ChangeStats{}},
	}

	for _, tc := range tests {
		got := parseShortstat(tc.input)
		if got != tc.want {
			t.Errorf("parseShortstat(%q) = %+v, want %+v", tc.input, got, tc.want)
		}
	}
}

func TestVerificationSummary(t *testing.T) {
	outcomes := []Outcome{
		{Name: "build", Success: true},
		{Name: "test", Success: true},
		{Name: "lint", Success: false, Output: "error"},
		{Name: "security", Skipped: true, Success: true},
	}

	summary := VerificationSummary(outcomes, ComplexityLarge)
	if !strings.Contains(summary, "large complexity") {
		t.Error("expected complexity in summary")
	}
	if !strings.Contains(summary, "[PASS] build") {
		t.Error("expected PASS for build")
	}
	if !strings.Contains(summary, "[FAIL] lint") {
		t.Error("expected FAIL for lint")
	}
	if !strings.Contains(summary, "[SKIP] security") {
		t.Error("expected SKIP for security")
	}
}

func TestExtractNumber(t *testing.T) {
	tests := []struct {
		s, keyword, want string
	}{
		{"3 files changed", "file", "3"},
		{"42 insertions(+)", "insertion", "42"},
		{"no match", "foo", "0"},
	}
	for _, tc := range tests {
		got := extractNumber(tc.s, tc.keyword)
		if got != tc.want {
			t.Errorf("extractNumber(%q, %q) = %q, want %q", tc.s, tc.keyword, got, tc.want)
		}
	}
}
