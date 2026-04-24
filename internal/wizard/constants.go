package wizard

// Shared string literals used across the wizard package. goconst
// flagged these tokens in multiple production files; centralizing
// them gives callers a single source of truth without changing any
// wire formats or YAML keys.
const (
	// Preset names persisted to .stoke/config.yaml and referenced by
	// init / run to select supervisor / bus / quality bundles.
	presetMinimal  = "minimal"
	presetBalanced = "balanced"
	presetStrict   = "strict"

	// Team-size buckets emitted by InferTeamSize and consumed by the
	// wizard UI. These round-trip to YAML so they must stay as strings.
	teamSizeSolo   = "solo"
	teamSize2to5   = "2-5"
	teamSize6to20  = "6-20"
	teamSize20Plus = "20+"

	// Project maturity stage strings (string form of ScaleTier for
	// JSON / YAML round-tripping via MaturityClassification.Stage).
	stageMVP = "mvp"

	// Popular tool / SaaS names referenced when suggesting skills
	// from detected repo contents.
	detectVendor    = "vendor"
	detectTerraform = "terraform"
	detectStripe    = "stripe"

	// CLI label used when presenting provider choices.
	providerLabelClaude = "claude"
)
