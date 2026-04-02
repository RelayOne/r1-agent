package audit

import (
	"strings"
	"testing"

	"github.com/ericmacdougall/stoke/internal/scan"
)

func TestDefaultPersonas(t *testing.T) {
	personas := DefaultPersonas()
	if len(personas) != 17 {
		t.Fatalf("personas=%d, want 17", len(personas))
	}
	ids := map[string]bool{}
	for _, p := range personas {
		ids[p.ID] = true
		if p.Name == "" { t.Errorf("persona %s has no name", p.ID) }
		if p.Focus == "" { t.Errorf("persona %s has no focus", p.ID) }
	}
	// Core 5 must always be present
	for _, required := range []string{"security", "performance", "reliability", "maintainability", "ops"} {
		if !ids[required] { t.Errorf("missing core persona: %s", required) }
	}
	// Specialized must be present
	for _, required := range []string{"api-design", "concurrency", "testing", "privacy", "compliance"} {
		if !ids[required] { t.Errorf("missing specialized persona: %s", required) }
	}
}

func TestBuildPromptIncludesContext(t *testing.T) {
	p := Persona{ID: "security", Name: "Security Reviewer", Focus: "auth, injection"}
	req := ReviewRequest{
		Persona:     p,
		Files:       []string{"src/auth.ts"},
		DiffSummary: "+++ new auth code",
		ScanResult: &scan.ScanResult{
			Findings: []scan.Finding{
				{Rule: "no-eval", Severity: "high", File: "src/auth.ts", Line: 10, Message: "eval() is dangerous"},
			},
		},
		SecurityMap: &scan.SecurityMap{
			Surfaces: []scan.SecuritySurface{
				{Category: "auth", File: "src/auth.ts", Line: 5, Note: "JWT usage", Risk: "high"},
			},
		},
	}

	prompt := BuildPrompt(p, req)

	if !strings.Contains(prompt, "Security Reviewer") {
		t.Error("prompt should mention persona")
	}
	if !strings.Contains(prompt, "src/auth.ts") {
		t.Error("prompt should list modified files")
	}
	if !strings.Contains(prompt, "new auth code") {
		t.Error("prompt should include diff")
	}
	if !strings.Contains(prompt, "eval()") {
		t.Error("security persona should see eval finding")
	}
	if !strings.Contains(prompt, "JWT") {
		t.Error("security persona should see security surface")
	}
	if !strings.Contains(prompt, "JSON array") {
		t.Error("prompt should specify output format")
	}
}

func TestBuildPromptPerformance(t *testing.T) {
	p := Persona{ID: "performance", Name: "Performance Reviewer", Focus: "N+1, allocations"}
	req := ReviewRequest{Persona: p, DiffSummary: "changes"}

	prompt := BuildPrompt(p, req)
	if !strings.Contains(prompt, "Performance Reviewer") {
		t.Error("should mention performance")
	}
}

func TestSelectPersonasWithSecurity(t *testing.T) {
	all := DefaultPersonas()
	secMap := &scan.SecurityMap{
		Surfaces: []scan.SecuritySurface{
			{Category: "auth", Risk: "high"},
			{Category: "crypto", Risk: "high"},
		},
	}

	selected := SelectPersonas(all, secMap, nil)
	found := false
	for _, p := range selected {
		if p.ID == "security" { found = true }
	}
	if !found {
		t.Error("security persona should be selected when auth surface exists")
	}
}

func TestSelectPersonasMinimum(t *testing.T) {
	all := DefaultPersonas()
	// No scan results or security map -- should return core 5
	selected := SelectPersonas(all, nil, nil)
	if len(selected) != 5 {
		t.Errorf("should return 5 core personas when no context, got %d", len(selected))
	}
	ids := map[string]bool{}
	for _, p := range selected { ids[p.ID] = true }
	for _, core := range []string{"security", "performance", "reliability", "maintainability", "ops"} {
		if !ids[core] { t.Errorf("missing core persona: %s", core) }
	}
}

func TestFilterFindingsSecurityPersona(t *testing.T) {
	findings := []scan.Finding{
		{Rule: "no-eval", Severity: "high"},
		{Rule: "no-console-log", Severity: "medium"},
		{Rule: "no-hardcoded-secret", Severity: "critical"},
	}
	filtered := filterFindings(findings, "security")
	if len(filtered) != 2 {
		t.Errorf("security persona should see 2 findings (eval + secret), got %d", len(filtered))
	}
}

func TestFilterFindingsMaintainability(t *testing.T) {
	findings := []scan.Finding{
		{Rule: "no-console-log", Severity: "medium"},
		{Rule: "no-eval", Severity: "high"},
		{Rule: "no-todo-fixme", Severity: "low"},
	}
	filtered := filterFindings(findings, "maintainability")
	if len(filtered) != 2 {
		t.Errorf("maintainability should see console-log + todo, got %d", len(filtered))
	}
}
