package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAutoLoadPolicyDiscovers(t *testing.T) {
	dir := t.TempDir()
	// Write a policy file with a well-known name
	path := filepath.Join(dir, "stoke.yaml")
	if err := os.WriteFile(path, []byte(DefaultPolicyYAML()), 0o644); err != nil {
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
	if err := os.WriteFile(explicit, []byte(DefaultPolicyYAML()), 0o644); err != nil {
		t.Fatal(err)
	}
	// Also write stoke.yaml (should be ignored when explicit is given)
	if err := os.WriteFile(filepath.Join(dir, "stoke.yaml"), []byte("invalid yaml {{{{"), 0o644); err != nil {
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
	path := filepath.Join(dir, "stoke.policy.yaml")
	if err := os.WriteFile(path, []byte(DefaultPolicyYAML()), 0o644); err != nil {
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
