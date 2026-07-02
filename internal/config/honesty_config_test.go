package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTempPolicy writes a policy file and returns its path.
func writeTempPolicy(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "r1.policy.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	return path
}

// TestLoadPolicy_HonestySectionParses locks in the A058 fix: a YAML
// honesty: section must parse (it previously failed the whole policy
// load with "unsupported policy structure") and its values must land
// on Policy.Honesty.
func TestLoadPolicy_HonestySectionParses(t *testing.T) {
	path := writeTempPolicy(t, `phases:
  plan:
    builtin_tools: [Read]
    mcp_enabled: false

honesty:
  enabled: true
  check_imports: false
  claim_decomposition: true
  cot_monitoring: true
  confession: true
  judge_model: claude-haiku-4-5
`)
	p, err := LoadPolicy(path)
	if err != nil {
		t.Fatalf("LoadPolicy with honesty section: %v", err)
	}
	h := p.Honesty
	if !h.Enabled {
		t.Errorf("Enabled = false, want true")
	}
	if h.CheckImports {
		t.Errorf("CheckImports = true, want false (explicitly disabled)")
	}
	if !h.ClaimDecomposition || !h.CoTMonitoring || !h.Confession {
		t.Errorf("LLM flags not honored: %+v", h)
	}
	if h.JudgeModel != "claude-haiku-4-5" {
		t.Errorf("JudgeModel = %q, want claude-haiku-4-5", h.JudgeModel)
	}
}

// TestLoadPolicy_HonestyOmittedGetsDefaults verifies the absent-vs-explicit
// distinction: no honesty: section means DefaultHonestyConfig().
func TestLoadPolicy_HonestyOmittedGetsDefaults(t *testing.T) {
	path := writeTempPolicy(t, `phases:
  plan:
    builtin_tools: [Read]
    mcp_enabled: false
`)
	p, err := LoadPolicy(path)
	if err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}
	want := DefaultHonestyConfig()
	if p.Honesty != want {
		t.Errorf("Honesty = %+v, want defaults %+v", p.Honesty, want)
	}
}

// TestLoadPolicy_HonestyExplicitOffHonored verifies that an explicit
// enabled: false block is NOT overwritten by defaults.
func TestLoadPolicy_HonestyExplicitOffHonored(t *testing.T) {
	path := writeTempPolicy(t, `phases:
  plan:
    builtin_tools: [Read]
    mcp_enabled: false

honesty:
  enabled: false
`)
	p, err := LoadPolicy(path)
	if err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}
	if p.Honesty.Enabled {
		t.Errorf("Enabled = true, want false (explicit off must be honored)")
	}
	if p.Honesty.CheckImports || p.Honesty.CoTMonitoring {
		t.Errorf("explicit block must not inherit defaults: %+v", p.Honesty)
	}
}

// TestLoadPolicy_HonestyJSONProbe verifies the JSON path mirrors the
// YAML absent-vs-explicit behavior.
func TestLoadPolicy_HonestyJSONProbe(t *testing.T) {
	pathExplicit := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(pathExplicit, []byte(`{"honesty":{"enabled":false}}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	p, err := LoadPolicy(pathExplicit)
	if err != nil {
		t.Fatalf("LoadPolicy json: %v", err)
	}
	if p.Honesty.Enabled {
		t.Errorf("json explicit off: Enabled = true, want false")
	}

	pathOmitted := filepath.Join(t.TempDir(), "policy2.json")
	if err := os.WriteFile(pathOmitted, []byte(`{}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	p2, err := LoadPolicy(pathOmitted)
	if err != nil {
		t.Fatalf("LoadPolicy json omitted: %v", err)
	}
	if p2.Honesty != DefaultHonestyConfig() {
		t.Errorf("json omitted: Honesty = %+v, want defaults", p2.Honesty)
	}
}

// TestDefaultPolicy_IncludesHonestyDefaults locks DefaultPolicy() carrying
// DefaultHonestyConfig so code paths that never touch a policy file still
// see the default-on honesty configuration.
func TestDefaultPolicy_IncludesHonestyDefaults(t *testing.T) {
	if DefaultPolicy().Honesty != DefaultHonestyConfig() {
		t.Errorf("DefaultPolicy().Honesty = %+v, want DefaultHonestyConfig()", DefaultPolicy().Honesty)
	}
}
