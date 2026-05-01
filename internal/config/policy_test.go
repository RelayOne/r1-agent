package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVerificationAllDisabledHonored(t *testing.T) {
	dir := t.TempDir()
	yaml := `phases:
  plan:
    builtin_tools: [Read]
    denied_rules: []
    allowed_rules: [Read]
  execute:
    builtin_tools: [Read, Edit]
    denied_rules: []
    allowed_rules: [Read, Edit]
  verify:
    builtin_tools: [Read]
    denied_rules: []
    allowed_rules: [Read]
files:
  protected: []
verification:
  build: false
  tests: false
  lint: false
  cross_model_review: false
  scope_check: false
`
	path := filepath.Join(dir, "stoke.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := LoadPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	// All gates should be false — not restored to defaults.
	if p.Verification.Build {
		t.Error("Build should be false when explicitly disabled")
	}
	if p.Verification.Tests {
		t.Error("Tests should be false when explicitly disabled")
	}
	if p.Verification.Lint {
		t.Error("Lint should be false when explicitly disabled")
	}
	if p.Verification.CrossModelReview {
		t.Error("CrossModelReview should be false when explicitly disabled")
	}
	if p.Verification.ScopeCheck {
		t.Error("ScopeCheck should be false when explicitly disabled")
	}
}

func TestVerificationOmittedGetsDefaults(t *testing.T) {
	dir := t.TempDir()
	// YAML with no verification section at all
	yaml := `phases:
  plan:
    builtin_tools: [Read]
    denied_rules: []
    allowed_rules: [Read]
  execute:
    builtin_tools: [Read, Edit]
    denied_rules: []
    allowed_rules: [Read, Edit]
  verify:
    builtin_tools: [Read]
    denied_rules: []
    allowed_rules: [Read]
files:
  protected: []
`
	path := filepath.Join(dir, "stoke.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := LoadPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	// When verification section is omitted, defaults should apply.
	def := DefaultPolicy()
	if p.Verification != def.Verification {
		t.Errorf("omitted verification should get defaults: got %+v, want %+v", p.Verification, def.Verification)
	}
}

func TestAutoLoadPolicyDiscovers(t *testing.T) {
	dir := t.TempDir()
	// Write a policy file with a well-known name
	path := filepath.Join(dir, "stoke.yaml")
	if err := os.WriteFile(path, []byte(DefaultPolicyYAML()), 0o600); err != nil {
		t.Fatal(err)
	}
	// AutoLoadPolicy with empty explicit path should discover it
	p, err := AutoLoadPolicy(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Phases["execute"].BuiltinTools) == 0 {
		t.Fatal("expected auto-discovered policy to have execute builtin tools")
	}
}

func TestAutoLoadPolicyFallsBackToDefault(t *testing.T) {
	dir := t.TempDir()
	// No policy file exists
	p, err := AutoLoadPolicy(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	// Should get default policy
	def := DefaultPolicy()
	if len(p.Phases) != len(def.Phases) {
		t.Errorf("expected default policy phases, got %d vs %d", len(p.Phases), len(def.Phases))
	}
}

func TestAutoLoadPolicyExplicitOverrides(t *testing.T) {
	dir := t.TempDir()
	// Write a policy at a non-standard name
	explicit := filepath.Join(dir, "custom-policy.yaml")
	if err := os.WriteFile(explicit, []byte(DefaultPolicyYAML()), 0o600); err != nil {
		t.Fatal(err)
	}
	// Also write stoke.yaml (should be ignored when explicit is given)
	if err := os.WriteFile(filepath.Join(dir, "stoke.yaml"), []byte("invalid yaml {{{{"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Explicit path should take precedence
	p, err := AutoLoadPolicy(dir, explicit)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Phases["execute"].BuiltinTools) == 0 {
		t.Fatal("expected explicit policy to load correctly")
	}
}

func TestLoadPolicyYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "r1.policy.yaml")
	if err := os.WriteFile(path, []byte(DefaultPolicyYAML()), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := LoadPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Phases["execute"].BuiltinTools) == 0 {
		t.Fatalf("expected execute builtin tools to be loaded")
	}
	if !p.Verification.Build || !p.Verification.ScopeCheck {
		t.Fatalf("expected verification flags to parse as required=true")
	}
}
