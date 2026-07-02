package workflow

// Tests for injectSkillsBudgeted (audit A059): the skill injection
// budget and enable switch must come from Engine.Policy.Skills instead
// of the previously hardcoded 3000.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/config"
	"github.com/RelayOne/r1/internal/skill"
)

func newBudgetTestEngine(t *testing.T) Engine {
	t.Helper()
	dir := t.TempDir()
	content := `# gamma

> Gamma skill

<!-- keywords: gammaonly -->

` + strings.Repeat("gamma skill body line\n", 100)
	if err := os.WriteFile(filepath.Join(dir, "gamma.md"), []byte(content), 0o600); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	reg := skill.NewRegistry(dir)
	if err := reg.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	return Engine{SkillRegistry: reg, StackMatches: []string{"gamma"}}
}

func TestInjectSkillsBudgetedExplicitDisable(t *testing.T) {
	e := newBudgetTestEngine(t)
	e.Policy = config.Policy{Skills: config.SkillsConfig{Enabled: false, TokenBudget: 3000}}
	out := injectSkillsBudgeted(e, "base prompt")
	if out != "base prompt" {
		t.Error("skills.enabled:false policy did not disable injection")
	}
}

func TestInjectSkillsBudgetedZeroPolicyKeepsHistoricalDefault(t *testing.T) {
	e := newBudgetTestEngine(t)
	// Zero-value policy (engine constructed without a loaded policy):
	// injection stays enabled with the historical 3000 default.
	out := injectSkillsBudgeted(e, "base prompt")
	if !strings.Contains(out, "gamma skill body") {
		t.Error("zero-value policy should keep injection enabled with default budget")
	}
}

func TestInjectSkillsBudgetedHonorsPolicyBudget(t *testing.T) {
	e := newBudgetTestEngine(t)
	// The gamma skill costs ~550 estimated tokens. A 10-token policy
	// budget must keep it out; the default 3000 lets it in (previous test).
	e.Policy = config.Policy{Skills: config.SkillsConfig{Enabled: true, TokenBudget: 10}}
	out := injectSkillsBudgeted(e, "base prompt")
	if strings.Contains(out, "gamma skill body") {
		t.Error("policy token_budget=10 was ignored; skill injected anyway")
	}
}

func TestInjectSkillsBudgetedHonorsExcluded(t *testing.T) {
	e := newBudgetTestEngine(t)
	e.Policy = config.Policy{Skills: config.SkillsConfig{
		Enabled:     true,
		TokenBudget: 3000,
		Excluded:    []string{"gamma"},
	}}
	out := injectSkillsBudgeted(e, "base prompt")
	if strings.Contains(out, "gamma skill body") {
		t.Error("policy excluded skill gamma was injected anyway")
	}
}
