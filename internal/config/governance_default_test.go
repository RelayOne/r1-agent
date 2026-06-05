package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestGovernanceDefault verifies the "absent vs explicit-false" default-on
// behavior for the V2 governance layer, mirroring the verificationExplicit
// trick. The governance section omitted ⇒ Enabled defaults to true; an
// explicit `governance:\n  enabled: false` is honored; `enabled: true` stays
// true. Covers BOTH parse paths (YAML line-scanner + parseGovernanceBlock,
// and the JSON probe branch) and asserts governanceExplicit is set only when
// the block is present.
func TestGovernanceDefault(t *testing.T) {
	t.Run("YAML_absent_block_defaults_on", func(t *testing.T) {
		p := loadPolicyFromString(t, "r1.policy.yaml", DefaultPolicyYAML())
		if !p.Governance.Enabled {
			t.Fatalf("absent governance block: Governance.Enabled = false, want true (default-on)")
		}
		if p.governanceExplicit {
			t.Fatalf("absent governance block: governanceExplicit = true, want false")
		}
	})

	t.Run("YAML_explicit_false_honored", func(t *testing.T) {
		body := DefaultPolicyYAML() + `
governance:
  enabled: false
`
		p := loadPolicyFromString(t, "r1.policy.yaml", body)
		if p.Governance.Enabled {
			t.Fatalf("governance.enabled: false → Governance.Enabled = true, want false")
		}
		if !p.governanceExplicit {
			t.Fatalf("explicit governance block: governanceExplicit = false, want true")
		}
	})

	t.Run("YAML_explicit_true", func(t *testing.T) {
		body := DefaultPolicyYAML() + `
governance:
  enabled: true
`
		p := loadPolicyFromString(t, "r1.policy.yaml", body)
		if !p.Governance.Enabled {
			t.Fatalf("governance.enabled: true → Governance.Enabled = false, want true")
		}
		if !p.governanceExplicit {
			t.Fatalf("explicit governance block: governanceExplicit = false, want true")
		}
	})

	t.Run("JSON_absent_block_defaults_on", func(t *testing.T) {
		// A minimal JSON policy with no governance key → default-on.
		body := `{"verification":{"build":true,"tests":true,"lint":true,"cross_model_review":true,"scope_check":true}}`
		p := loadPolicyFromString(t, "stoke.policy.json", body)
		if !p.Governance.Enabled {
			t.Fatalf("JSON absent governance: Governance.Enabled = false, want true (default-on)")
		}
		if p.governanceExplicit {
			t.Fatalf("JSON absent governance: governanceExplicit = true, want false")
		}
	})

	t.Run("JSON_explicit_false_honored", func(t *testing.T) {
		body := `{"governance":{"enabled":false}}`
		p := loadPolicyFromString(t, "stoke.policy.json", body)
		if p.Governance.Enabled {
			t.Fatalf("JSON governance.enabled false → Governance.Enabled = true, want false")
		}
		if !p.governanceExplicit {
			t.Fatalf("JSON explicit governance: governanceExplicit = false, want true")
		}
	})

	t.Run("JSON_explicit_true", func(t *testing.T) {
		body := `{"governance":{"enabled":true}}`
		p := loadPolicyFromString(t, "stoke.policy.json", body)
		if !p.Governance.Enabled {
			t.Fatalf("JSON governance.enabled true → Governance.Enabled = false, want true")
		}
		if !p.governanceExplicit {
			t.Fatalf("JSON explicit governance: governanceExplicit = false, want true")
		}
	})
}

// loadPolicyFromString writes body to a temp file named name and loads it via
// the same LoadPolicy entrypoint the other config tests use
// (schema_test.go:74 TestConfig_LobeFlagsParse_ViaLoadPolicy).
func loadPolicyFromString(t *testing.T, name, body string) Policy {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	p, err := LoadPolicy(path)
	if err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}
	return p
}
