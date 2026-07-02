package skill

// Tests for ConfigureInjection (audit A059): policy-driven AlwaysOn and
// Excluded lists must be honored by InjectPromptBudgeted.

import (
	"os"
	"path/filepath"
	"testing"
)

func newPolicyTestRegistry(t *testing.T) *Registry {
	t.Helper()
	dir := t.TempDir()
	skills := map[string]string{
		"alpha.md": `# alpha

> Alpha skill

<!-- keywords: alphaonly -->

Alpha skill content for stack matching.
`,
		"beta.md": `# beta

> Beta skill

<!-- keywords: betaonly -->

Beta skill content, only injectable via always-on.
`,
	}
	for name, content := range skills {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write skill %s: %v", name, err)
		}
	}
	reg := NewRegistry(dir)
	if err := reg.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	return reg
}

func TestConfigureInjectionExcludedFiltersStackMatch(t *testing.T) {
	reg := newPolicyTestRegistry(t)

	// Baseline: alpha is injected via the repo-stack tier.
	_, selected := reg.InjectPromptBudgeted("do the thing", []string{"alpha"}, 3000)
	if !hasSelection(selected, "alpha") {
		t.Fatal("baseline: expected alpha to be injected via stack match")
	}

	reg.ConfigureInjection(nil, []string{"alpha"})
	_, selected = reg.InjectPromptBudgeted("do the thing", []string{"alpha"}, 3000)
	if hasSelection(selected, "alpha") {
		t.Error("excluded skill alpha was still injected")
	}
}

func TestConfigureInjectionAlwaysOnForceIncludes(t *testing.T) {
	reg := newPolicyTestRegistry(t)

	// Baseline: beta is not injected (no stack match, no keyword hit).
	_, selected := reg.InjectPromptBudgeted("do the thing", nil, 3000)
	if hasSelection(selected, "beta") {
		t.Fatal("baseline: beta unexpectedly injected without always-on")
	}

	reg.ConfigureInjection([]string{"beta"}, nil)
	prompt, selected := reg.InjectPromptBudgeted("do the thing", nil, 3000)
	if !hasSelection(selected, "beta") {
		t.Fatal("policy always-on skill beta was not injected")
	}
	for _, sel := range selected {
		if sel.Skill.Name == "beta" {
			if sel.Tier != TierFull {
				t.Errorf("beta tier = %v, want TierFull", sel.Tier)
			}
			if sel.Reason != "policy-always-on" {
				t.Errorf("beta reason = %q, want policy-always-on", sel.Reason)
			}
		}
	}
	if prompt == "do the thing" {
		t.Error("prompt was not augmented with the always-on skill")
	}

	// Clearing the lists restores baseline behavior.
	reg.ConfigureInjection(nil, nil)
	_, selected = reg.InjectPromptBudgeted("do the thing", nil, 3000)
	if hasSelection(selected, "beta") {
		t.Error("beta still injected after ConfigureInjection(nil, nil)")
	}
}

func hasSelection(selected []SkillSelection, name string) bool {
	for _, sel := range selected {
		if sel.Skill.Name == name {
			return true
		}
	}
	return false
}
