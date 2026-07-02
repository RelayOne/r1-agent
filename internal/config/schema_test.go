package config

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestConfig_LobeFlagsParse round-trips a sample cortex.lobes.* YAML
// covering binary (Enabled) flags and the curator's nested
// AutoCurateCategories + SkipPrivateMessages settings.
//
// Spec: specs/cortex-concerns.md item 3.
func TestConfig_LobeFlagsParse(t *testing.T) {
	raw := `
cortex:
  lobes:
    memory_recall:
      enabled: true
    wal_keeper:
      enabled: true
    rule_check:
      enabled: true
    plan_update:
      enabled: false
    clarifying_q:
      enabled: false
    memory_curator:
      enabled: false
      auto_curate_categories: ["project_facts", "preferences"]
      skip_private_messages: true
`
	var cfg CortexConfigSchema
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if !cfg.Cortex.Lobes.MemoryRecall.IsEnabled(false) {
		t.Fatal("MemoryRecall should be explicitly enabled")
	}
	if !cfg.Cortex.Lobes.WALKeeper.IsEnabled(false) {
		t.Fatal("WALKeeper should be explicitly enabled")
	}
	if !cfg.Cortex.Lobes.RuleCheck.IsEnabled(false) {
		t.Fatal("RuleCheck should be explicitly enabled")
	}
	if cfg.Cortex.Lobes.PlanUpdate.IsEnabled(true) {
		t.Fatal("PlanUpdate should be explicitly disabled")
	}
	if cfg.Cortex.Lobes.ClarifyingQ.IsEnabled(true) {
		t.Fatal("ClarifyingQ should be explicitly disabled")
	}
	if cfg.Cortex.Lobes.MemoryCurator.IsEnabled(true) {
		t.Fatal("MemoryCurator should be explicitly disabled")
	}
	// anti_trunc is absent from the fixture: the flag must be nil so
	// consumers can preserve default-on (audit A057).
	if cfg.Cortex.Lobes.AntiTrunc.Enabled != nil {
		t.Fatal("absent anti_trunc flag should parse as nil (tri-state)")
	}
	if got := len(cfg.Cortex.Lobes.MemoryCurator.AutoCurateCategories); got != 2 {
		t.Fatalf("auto_curate_categories: got %d entries, want 2", got)
	}
	if cfg.Cortex.Lobes.MemoryCurator.AutoCurateCategories[0] != "project_facts" ||
		cfg.Cortex.Lobes.MemoryCurator.AutoCurateCategories[1] != "preferences" {
		t.Fatalf("auto_curate_categories content wrong: %#v",
			cfg.Cortex.Lobes.MemoryCurator.AutoCurateCategories)
	}
	if !cfg.Cortex.Lobes.MemoryCurator.SkipPrivateMessages {
		t.Fatal("skip_private_messages should be true")
	}
}

// TestConfig_LobeFlagsParse_ViaLoadPolicy verifies that the cortex.*
// block is hooked into Policy.Cortex by the YAML loader. parsePolicyYAML
// skips the `cortex:` block (same as `mcp_servers:`) and parseCortexBlock
// reparses it with yaml.v3 in LoadPolicy.
func TestConfig_LobeFlagsParse_ViaLoadPolicy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "r1.policy.yaml")
	body := DefaultPolicyYAML() + `
cortex:
  lobes:
    memory_recall:
      enabled: true
    memory_curator:
      enabled: false
      auto_curate_categories: ["fact"]
      skip_private_messages: true
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := LoadPolicy(path)
	if err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}
	if !p.Cortex.Lobes.MemoryRecall.IsEnabled(false) {
		t.Fatal("Policy.Cortex.Lobes.MemoryRecall should be explicitly enabled")
	}
	if p.Cortex.Lobes.MemoryCurator.IsEnabled(true) {
		t.Fatal("Policy.Cortex.Lobes.MemoryCurator should be explicitly disabled")
	}
	if got := p.Cortex.Lobes.MemoryCurator.AutoCurateCategories; len(got) != 1 || got[0] != "fact" {
		t.Fatalf("auto_curate_categories: got %#v, want [fact]", got)
	}
	if !p.Cortex.Lobes.MemoryCurator.SkipPrivateMessages {
		t.Fatal("skip_private_messages should be true")
	}
}

// TestConfig_CortexAbsentBlockDefaultsOn asserts the default-on contract
// (audit A057): a policy without any `cortex:` block resolves every Lobe
// flag to the caller-supplied default because the tri-state pointers are
// nil, so deployments without a cortex: section keep all deterministic
// Lobes active.
func TestConfig_CortexAbsentBlockDefaultsOn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "r1.policy.yaml")
	if err := os.WriteFile(path, []byte(DefaultPolicyYAML()), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := LoadPolicy(path)
	if err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}
	for _, id := range []string{"memory-recall", "wal-keeper", "rule-check", "antitrunc"} {
		if !p.Cortex.LobeEnabled(id, true) {
			t.Errorf("absent cortex block: LobeEnabled(%q, true) = false, want true (default-on)", id)
		}
	}
	// And an explicit false must win over the default.
	off := false
	p.Cortex.Lobes.MemoryRecall.Enabled = &off
	if p.Cortex.LobeEnabled("memory-recall", true) {
		t.Error("explicit enabled: false must override default-on")
	}
}

// TestConfig_CortexLobeEnabled_IDSpellings verifies LobeEnabled accepts
// the production Lobe.ID() spellings, the YAML key spellings, and the
// package-name spellings for the same flag, and that unknown IDs fall
// back to the supplied default.
func TestConfig_CortexLobeEnabled_IDSpellings(t *testing.T) {
	off := false
	var c CortexConfig
	c.Lobes.WALKeeper.Enabled = &off
	for _, id := range []string{"wal-keeper", "wal_keeper", "walkeeper"} {
		if c.LobeEnabled(id, true) {
			t.Errorf("LobeEnabled(%q, true) = true, want false (explicit off)", id)
		}
	}
	if !c.LobeEnabled("some-future-lobe", true) {
		t.Error("unknown lobe id must resolve to the supplied default")
	}
	if c.LobeEnabled("some-future-lobe", false) {
		t.Error("unknown lobe id must resolve to the supplied default (false)")
	}
}
