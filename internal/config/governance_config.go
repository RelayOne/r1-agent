package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// governanceConfigSchema is the top-level container used to round-trip the
// `governance:` section by itself out of the raw policy YAML bytes.
// parseGovernanceBlock uses it internally to extract the section via
// yaml.v3 — the same pattern used for `cortex:` (see schema.go) and
// `mcp_servers:` (see mcp_servers.go).
type governanceConfigSchema struct {
	Governance GovernanceConfig `yaml:"governance" json:"governance"`
}

// parseGovernanceBlock extracts the `governance:` top-level mapping from
// the raw policy YAML bytes using yaml.v3. Returns the zero
// GovernanceConfig (Enabled:false) and nil error when the block is absent.
// Mirrors parseCortexBlock — see schema.go for the same pattern.
//
// Structural errors (bad yaml, wrong node kind) bubble up so the loader
// can surface them to the operator.
func parseGovernanceBlock(raw []byte) (GovernanceConfig, error) {
	var doc governanceConfigSchema
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return GovernanceConfig{}, fmt.Errorf("governance: yaml parse: %w", err)
	}
	return doc.Governance, nil
}
