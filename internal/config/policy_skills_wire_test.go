package config

// Tests for the skills: policy section wiring (audit A059): a YAML
// skills: block must parse (previously it hard-failed LoadPolicy with
// "unsupported policy structure"), an explicit enabled:false must be
// honored (previously clobbered back to defaults when token_budget was
// omitted), and an omitted section must still yield DefaultSkillsConfig.

import (
	"os"
	"path/filepath"
	"testing"
)

func writePolicyFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "r1.policy.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	return path
}

func TestLoadPolicyYAMLSkillsBlockParses(t *testing.T) {
	path := writePolicyFile(t, `skills:
  enabled: true
  token_budget: 5000
  always_on: [go-style, agent-discipline]
  excluded: [docker]
`)
	p, err := LoadPolicy(path)
	if err != nil {
		t.Fatalf("LoadPolicy with skills: block failed: %v", err)
	}
	if !p.Skills.Enabled {
		t.Error("Skills.Enabled = false, want true")
	}
	if p.Skills.TokenBudget != 5000 {
		t.Errorf("Skills.TokenBudget = %d, want 5000", p.Skills.TokenBudget)
	}
	if len(p.Skills.AlwaysOn) != 2 || p.Skills.AlwaysOn[0] != "go-style" {
		t.Errorf("Skills.AlwaysOn = %v, want [go-style agent-discipline]", p.Skills.AlwaysOn)
	}
	if len(p.Skills.Excluded) != 1 || p.Skills.Excluded[0] != "docker" {
		t.Errorf("Skills.Excluded = %v, want [docker]", p.Skills.Excluded)
	}
}

func TestLoadPolicyYAMLSkillsExplicitDisableHonored(t *testing.T) {
	path := writePolicyFile(t, `skills:
  enabled: false
`)
	p, err := LoadPolicy(path)
	if err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}
	if p.Skills.Enabled {
		t.Error("explicit skills.enabled:false was clobbered back to true")
	}
	if p.Skills.TokenBudget != DefaultSkillsConfig().TokenBudget {
		t.Errorf("TokenBudget = %d, want back-filled default %d",
			p.Skills.TokenBudget, DefaultSkillsConfig().TokenBudget)
	}
}

func TestLoadPolicyYAMLSkillsOmittedGetsDefaults(t *testing.T) {
	path := writePolicyFile(t, `verification:
  build: true
`)
	p, err := LoadPolicy(path)
	if err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}
	d := DefaultSkillsConfig()
	if !p.Skills.Enabled || p.Skills.TokenBudget != d.TokenBudget {
		t.Errorf("omitted skills section: got %+v, want defaults %+v", p.Skills, d)
	}
	if len(p.Skills.AlwaysOn) != len(d.AlwaysOn) {
		t.Errorf("AlwaysOn = %v, want default %v", p.Skills.AlwaysOn, d.AlwaysOn)
	}
}

func TestLoadPolicyJSONSkillsExplicitDisableHonored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, []byte(`{"skills":{"enabled":false}}`), 0o600); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	p, err := LoadPolicy(path)
	if err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}
	if p.Skills.Enabled {
		t.Error("JSON explicit skills.enabled:false was clobbered back to true")
	}
	if p.Skills.TokenBudget != DefaultSkillsConfig().TokenBudget {
		t.Errorf("TokenBudget = %d, want back-filled default", p.Skills.TokenBudget)
	}
}
