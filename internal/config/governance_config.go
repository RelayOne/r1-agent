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
// The second return value, present, reports whether a top-level
// `governance:` key exists in the parsed YAML. It powers the
// "absent vs explicit" distinction in LoadPolicy (policy.go) so that
// normalizePolicy can default Governance.Enabled to ON only when the
// operator omitted the section entirely. present is true even when the
// section is `governance: null` or sets enabled:false — any explicit
// top-level governance key counts as present. The returned
// GovernanceConfig value is unchanged: it is still the zero config
// (Enabled:false) when the block is absent.
//
// Structural errors (bad yaml, wrong node kind) bubble up so the loader
// can surface them to the operator.
func parseGovernanceBlock(raw []byte) (GovernanceConfig, bool, error) {
	var doc governanceConfigSchema
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return GovernanceConfig{}, false, fmt.Errorf("governance: yaml parse: %w", err)
	}
	return doc.Governance, governanceKeyPresent(raw), nil
}

// governanceKeyPresent reports whether the raw policy YAML has a top-level
// `governance:` mapping key. It walks the document's root mapping node so a
// `governance: null` or `governance:\n  enabled: false` block still counts
// as present, while an omitted section does not.
func governanceKeyPresent(raw []byte) bool {
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return false
	}
	doc := &root
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		doc = doc.Content[0]
	}
	if doc.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(doc.Content); i += 2 {
		if doc.Content[i].Value == "governance" {
			return true
		}
	}
	return false
}
