package config

import (
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
	if !cfg.Cortex.Lobes.MemoryRecall.Enabled {
		t.Fatal("MemoryRecall should be enabled")
	}
	if !cfg.Cortex.Lobes.WALKeeper.Enabled {
		t.Fatal("WALKeeper should be enabled")
	}
	if !cfg.Cortex.Lobes.RuleCheck.Enabled {
		t.Fatal("RuleCheck should be enabled")
	}
	if cfg.Cortex.Lobes.PlanUpdate.Enabled {
		t.Fatal("PlanUpdate should be disabled")
	}
	if cfg.Cortex.Lobes.ClarifyingQ.Enabled {
		t.Fatal("ClarifyingQ should be disabled")
	}
	if cfg.Cortex.Lobes.MemoryCurator.Enabled {
		t.Fatal("MemoryCurator should be disabled")
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
