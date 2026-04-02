package config

import (
	"os"
	"path/filepath"
	"testing"
)

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
