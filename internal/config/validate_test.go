package config

import (
	"testing"
)

func TestValidatePolicyDefault(t *testing.T) {
	errs := ValidatePolicy(DefaultPolicy())
	for _, e := range errs {
		if e.Fatal {
			t.Errorf("default policy has fatal error: %v", e)
		}
	}
}

func TestValidatePolicyMissingPhase(t *testing.T) {
	p := Policy{Phases: map[string]PhasePolicy{"plan": {BuiltinTools: []string{"Read"}}}}
	errs := ValidatePolicy(p)
	fatalCount := 0
	for _, e := range errs {
		if e.Fatal { fatalCount++ }
	}
	if fatalCount < 2 {
		t.Errorf("missing execute+verify should produce 2 fatal errors, got %d", fatalCount)
	}
}

func TestValidatePolicyPlanWithWrite(t *testing.T) {
	p := DefaultPolicy()
	plan := p.Phases["plan"]
	plan.BuiltinTools = append(plan.BuiltinTools, "Edit")
	p.Phases["plan"] = plan

	errs := ValidatePolicy(p)
	found := false
	for _, e := range errs {
		if e.Field == "phases.plan.builtin_tools" { found = true }
	}
	if !found {
		t.Error("plan with Edit tool should generate warning")
	}
}

func TestValidateCommandsMissing(t *testing.T) {
	errs := ValidateCommands("", "", "")
	if len(errs) != 3 {
		t.Errorf("3 missing commands should produce 3 warnings, got %d", len(errs))
	}
}

func TestValidateCommandsPresent(t *testing.T) {
	errs := ValidateCommands("go build ./...", "go test ./...", "go vet ./...")
	if len(errs) != 0 {
		t.Errorf("all commands present should produce 0 warnings, got %d", len(errs))
	}
}
