package convergence

// Tests for crossFileTestCoverageFindings (audit A060): the two enabled
// SevMajor cross-file test-coverage rules ("missing-test-file" and
// "no-missing-error-test") previously returned nil unconditionally with a
// comment claiming integration-level handling that did not exist.

import (
	"strings"
	"testing"
)

func findingsByRule(fs []Finding, id string) []Finding {
	var out []Finding
	for _, f := range fs {
		if f.RuleID == id {
			out = append(out, f)
		}
	}
	return out
}

const untestedSource = `package widget

import "fmt"

func LoadWidget(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("empty name")
	}
	return name, nil
}
`

const coveringTest = `package widget

import "testing"

func TestLoadWidgetError(t *testing.T) {
	if _, err := LoadWidget(""); err == nil {
		t.Fatal("expected error")
	}
}
`

func TestCrossFileRulesFireOnSourceOnlySet(t *testing.T) {
	v := NewValidator()
	report := v.Validate("m1", []FileInput{
		{Path: "widget/widget.go", Content: []byte(untestedSource)},
	})

	if got := findingsByRule(report.Findings, "missing-test-file"); len(got) != 1 {
		t.Errorf("missing-test-file findings = %d, want 1 (%v)", len(got), got)
	} else {
		if got[0].Severity != SevMajor || got[0].Category != CatTestCoverage {
			t.Errorf("missing-test-file finding shape wrong: %+v", got[0])
		}
	}

	got := findingsByRule(report.Findings, "no-missing-error-test")
	if len(got) != 1 {
		t.Fatalf("no-missing-error-test findings = %d, want 1 (%v)", len(got), got)
	}
	if !strings.Contains(got[0].Description, "LoadWidget") {
		t.Errorf("finding should name the untested function: %q", got[0].Description)
	}
}

func TestCrossFileRulesQuietWithCoveringTests(t *testing.T) {
	v := NewValidator()
	report := v.Validate("m2", []FileInput{
		{Path: "widget/widget.go", Content: []byte(untestedSource)},
		{Path: "widget/widget_test.go", Content: []byte(coveringTest)},
	})

	if got := findingsByRule(report.Findings, "missing-test-file"); len(got) != 0 {
		t.Errorf("missing-test-file fired despite covering test in set: %v", got)
	}
	if got := findingsByRule(report.Findings, "no-missing-error-test"); len(got) != 0 {
		t.Errorf("no-missing-error-test fired despite error-path test in set: %v", got)
	}
}

func TestCrossFileRulesRespectEnableRule(t *testing.T) {
	v := NewValidator()
	v.EnableRule("missing-test-file", false)
	v.EnableRule("no-missing-error-test", false)
	report := v.Validate("m3", []FileInput{
		{Path: "widget/widget.go", Content: []byte(untestedSource)},
	})

	if got := findingsByRule(report.Findings, "missing-test-file"); len(got) != 0 {
		t.Errorf("disabled missing-test-file still fired: %v", got)
	}
	if got := findingsByRule(report.Findings, "no-missing-error-test"); len(got) != 0 {
		t.Errorf("disabled no-missing-error-test still fired: %v", got)
	}
}

func TestCrossFileRulesAlsoRunWithCriteria(t *testing.T) {
	v := NewValidator()
	report := v.ValidateWithCriteria("m4", []FileInput{
		{Path: "widget/widget.go", Content: []byte(untestedSource)},
	}, []string{"widgets load by name"})

	if got := findingsByRule(report.Findings, "missing-test-file"); len(got) != 1 {
		t.Errorf("ValidateWithCriteria: missing-test-file findings = %d, want 1", len(got))
	}
}
