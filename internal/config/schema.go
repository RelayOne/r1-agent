package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// CortexConfig configures the cortex package and its Lobes.
//
// The on-disk YAML form lives under the top-level `cortex:` key in
// r1.policy.yaml / `~/.r1/config.yaml` per specs/cortex-concerns.md
// §Privacy & Opt-Out. Operators disable individual Lobes by setting
// `enabled: false`; the MemoryCurator additionally accepts a category
// allow-list and a privacy switch.
//
// CortexConfig is hooked into the top-level Policy struct as
// Policy.Cortex. The Policy YAML loader uses a custom line scanner that
// does not understand arbitrary nested maps, so the `cortex:` block is
// skipped by parsePolicyYAML and reparsed by parseCortexBlock (yaml.v3)
// — exactly the same pattern used for `mcp_servers:`.
//
// Spec: specs/cortex-concerns.md item 3.
type CortexConfig struct {
	Lobes LobeFlags `yaml:"lobes" json:"lobes"`
}

// LobeFlags carries per-Lobe enable / behavior flags. The keys follow the
// underscore-separated naming used by the spec's TestConfig_LobeFlagsParse
// fixture; YAML aliases without underscores remain available via custom
// loaders if a deployment chooses to use them.
type LobeFlags struct {
	MemoryRecall  LobeFlag          `yaml:"memory_recall" json:"memory_recall"`
	WALKeeper     LobeFlag          `yaml:"wal_keeper" json:"wal_keeper"`
	RuleCheck     LobeFlag          `yaml:"rule_check" json:"rule_check"`
	PlanUpdate    LobeFlag          `yaml:"plan_update" json:"plan_update"`
	ClarifyingQ   LobeFlag          `yaml:"clarifying_q" json:"clarifying_q"`
	MemoryCurator MemoryCuratorFlag `yaml:"memory_curator" json:"memory_curator"`
}

// LobeFlag is the minimal binary on/off switch most Lobes use.
type LobeFlag struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
}

// MemoryCuratorFlag is the richer config block for MemoryCuratorLobe.
//
// AutoCurateCategories is the allow-list of memory categories the curator
// is permitted to auto-write without explicit operator confirmation. Per
// OQ-7 the safe default is ["fact"] / ["project_facts"]; deployments that
// want stricter curation can shrink the list, and deployments that opt
// into freer curation can expand it.
//
// SkipPrivateMessages, when true, instructs the curator to bypass any
// message tagged "private" (see specs/cortex-concerns.md §Privacy
// taxonomy). Defaults to true in operator-supplied configs; the struct
// zero value is false because Go zero-values can't express
// privacy-preserving defaults — callers should set it explicitly.
type MemoryCuratorFlag struct {
	Enabled              bool     `yaml:"enabled" json:"enabled"`
	AutoCurateCategories []string `yaml:"auto_curate_categories" json:"auto_curate_categories"`
	SkipPrivateMessages  bool     `yaml:"skip_private_messages" json:"skip_private_messages"`
}

// CortexConfigSchema is the top-level container used by
// TestConfig_LobeFlagsParse and any caller that wants to round-trip the
// `cortex:` section by itself (without the surrounding Policy fields).
// parseCortexBlock uses it internally to extract the section out of the
// raw YAML bytes via yaml.v3.
type CortexConfigSchema struct {
	Cortex CortexConfig `yaml:"cortex" json:"cortex"`
}

// parseCortexBlock extracts the `cortex:` top-level mapping from the raw
// policy YAML bytes using yaml.v3. Returns the zero CortexConfig (and
// nil error) when the block is absent. Mirrors parseMCPServersBlock —
// see mcp_servers.go for the same pattern.
//
// Structural errors (bad yaml, wrong node kind) bubble up as errors so
// the loader can surface them to the operator.
func parseCortexBlock(raw []byte) (CortexConfig, error) {
	var doc CortexConfigSchema
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return CortexConfig{}, fmt.Errorf("cortex: yaml parse: %w", err)
	}
	return doc.Cortex, nil
}
