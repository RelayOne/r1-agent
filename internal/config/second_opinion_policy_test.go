package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadPolicyYAML(t *testing.T, yaml string) (Policy, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stoke.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	return LoadPolicy(path)
}

func TestSecondOpinionDefaultOn(t *testing.T) {
	if !DefaultPolicy().Verification.SecondOpinion {
		t.Error("DefaultPolicy SecondOpinion should be true")
	}
	// The starter template must carry the key so `r1 init` policies
	// round-trip through the strict parser.
	p, err := loadPolicyYAML(t, DefaultPolicyYAML())
	if err != nil {
		t.Fatalf("DefaultPolicyYAML must parse: %v", err)
	}
	if !p.Verification.SecondOpinion {
		t.Error("DefaultPolicyYAML SecondOpinion should parse as true")
	}
}

func TestSecondOpinionKillSwitchParses(t *testing.T) {
	yaml := `verification:
  build: required
  tests: required
  lint: required
  cross_model_review: required
  scope_check: required
  second_opinion: false
`
	p, err := loadPolicyYAML(t, yaml)
	if err != nil {
		t.Fatal(err)
	}
	if p.Verification.SecondOpinion {
		t.Error("second_opinion: false must disable the second critic")
	}
	if !p.Verification.CrossModelReview {
		t.Error("other verification keys must be unaffected")
	}
}

func TestSecondOpinionOmittedInExplicitBlockStaysOff(t *testing.T) {
	// Pre-existing policies that spell out verification without the new
	// key must NOT have the critic switched on behind their back.
	yaml := `verification:
  build: required
  tests: required
  lint: required
  cross_model_review: required
  scope_check: required
`
	p, err := loadPolicyYAML(t, yaml)
	if err != nil {
		t.Fatal(err)
	}
	if p.Verification.SecondOpinion {
		t.Error("explicit verification block without second_opinion must leave it off")
	}
}

func TestSecondOpinionUnknownKeyStillErrors(t *testing.T) {
	yaml := `verification:
  second_opinions: required
`
	if _, err := loadPolicyYAML(t, yaml); err == nil || !strings.Contains(err.Error(), "unknown verification key") {
		t.Errorf("typo'd key must error, got: %v", err)
	}
}

func TestSecondOpinionAllFalseGuardIncludesNewField(t *testing.T) {
	// A JSON policy with an all-false verification struct (absent
	// section) restores defaults, including SecondOpinion.
	jsonPolicy := `{"phases":{"plan":{"builtin_tools":["Read"],"denied_rules":[],"allowed_rules":["Read"],"mcp_enabled":false}}}`
	path := filepath.Join(t.TempDir(), "stoke.json")
	if err := os.WriteFile(path, []byte(jsonPolicy), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := LoadPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Verification.SecondOpinion {
		t.Error("omitted verification section must restore SecondOpinion default (true)")
	}
}
