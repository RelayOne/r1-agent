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
// Wiring status (audit A057): the per-Lobe enable flags are honored by
// the `r1 mcp serve` cortex construction (cmd/r1/mcp_serve_runtime.go
// buildCortexBackend). The native-loop construction site
// (internal/engine buildDeterministicCortex) still registers the full
// deterministic set gated only by --cortex/--no-cortex — per-lobe
// policy gating there is pending activation (specs/cortex-activation.md
// ITEM 7 deferral). The MemoryCurator privacy switches gate nothing yet
// because no production path constructs the memorycurator Lobe.
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
	AntiTrunc     LobeFlag          `yaml:"anti_trunc" json:"anti_trunc"`
	MemoryCurator MemoryCuratorFlag `yaml:"memory_curator" json:"memory_curator"`
}

// LobeFlag is the minimal binary on/off switch most Lobes use.
//
// Enabled is a *bool so consumers can distinguish "operator explicitly
// set enabled: true/false" from "flag absent". An absent flag (nil)
// resolves to the caller-supplied default via IsEnabled — required to
// preserve the default-on contract for the deterministic Lobes when a
// deployment has no `cortex:` block at all (audit A057: a plain bool
// would zero-value to false and silently disable every Lobe).
type LobeFlag struct {
	Enabled *bool `yaml:"enabled" json:"enabled,omitempty"`
}

// IsEnabled resolves the tri-state flag: nil/absent → defaultOn,
// explicit true → true, explicit false → false.
func (f LobeFlag) IsEnabled(defaultOn bool) bool {
	if f.Enabled == nil {
		return defaultOn
	}
	return *f.Enabled
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
	Enabled              *bool    `yaml:"enabled" json:"enabled,omitempty"`
	AutoCurateCategories []string `yaml:"auto_curate_categories" json:"auto_curate_categories"`
	SkipPrivateMessages  bool     `yaml:"skip_private_messages" json:"skip_private_messages"`
}

// IsEnabled resolves the curator's tri-state enable flag: nil/absent →
// defaultOn, explicit value → that value.
func (f MemoryCuratorFlag) IsEnabled(defaultOn bool) bool {
	if f.Enabled == nil {
		return defaultOn
	}
	return *f.Enabled
}

// LobeEnabled resolves the enable flag for a Lobe by its runtime ID,
// accepting the production Lobe.ID() spellings (e.g. "memory-recall"),
// the YAML key spellings (e.g. "memory_recall"), and the package-name
// spellings (e.g. "memoryrecall"). Unknown IDs resolve to defaultOn so
// a new Lobe is never silently disabled by an older config.
func (c CortexConfig) LobeEnabled(lobeID string, defaultOn bool) bool {
	switch lobeID {
	case "memory-recall", "memory_recall", "memoryrecall":
		return c.Lobes.MemoryRecall.IsEnabled(defaultOn)
	case "wal-keeper", "wal_keeper", "walkeeper":
		return c.Lobes.WALKeeper.IsEnabled(defaultOn)
	case "rule-check", "rule_check", "rulecheck":
		return c.Lobes.RuleCheck.IsEnabled(defaultOn)
	case "anti-trunc", "anti_trunc", "antitrunc":
		return c.Lobes.AntiTrunc.IsEnabled(defaultOn)
	case "plan-update", "plan_update", "planupdate":
		return c.Lobes.PlanUpdate.IsEnabled(defaultOn)
	case "clarifying-q", "clarifying_q", "clarifyq":
		return c.Lobes.ClarifyingQ.IsEnabled(defaultOn)
	case "memory-curator", "memory_curator", "memorycurator":
		return c.Lobes.MemoryCurator.IsEnabled(defaultOn)
	}
	return defaultOn
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
