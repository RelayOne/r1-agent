package config

// CortexConfig configures the cortex package and its Lobes.
//
// The on-disk YAML form lives under the top-level `cortex:` key in
// `~/.r1/config.yaml` per specs/cortex-concerns.md §Privacy & Opt-Out.
// Operators disable individual Lobes by setting `enabled: false`; the
// MemoryCurator additionally accepts a category allow-list and a
// privacy switch.
//
// This struct is currently consumed by callers that load the cortex
// section directly via yaml.v3 (see TestConfig_LobeFlagsParse). It is
// intentionally separate from internal/config.Policy because Policy is
// loaded by a custom line-scanner that does not understand arbitrary
// nesting; threading cortex.* through that scanner is out of scope per
// the spec ("Config hot-reload is intentionally out of scope — adding
// one is a separate spec.").
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

// CortexConfigSchema is the top-level container used by tests and any
// future loader that wants to round-trip the `cortex:` section by
// itself. It exists solely to give yaml.v3 a struct to anchor the
// `cortex:` key against without forcing the wider Policy type to grow
// a Cortex field (the Policy YAML is parsed by a custom line scanner
// that does not yet support arbitrary nested maps).
type CortexConfigSchema struct {
	Cortex CortexConfig `yaml:"cortex" json:"cortex"`
}
