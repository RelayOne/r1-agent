package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// honestyConfigSchema is the top-level container used to round-trip the
// `honesty:` section by itself out of the raw policy YAML bytes.
// parseHonestyBlock uses it internally to extract the section via
// yaml.v3 — the same pattern used for `governance:` (see
// governance_config.go) and `cortex:` (see schema.go).
type honestyConfigSchema struct {
	Honesty HonestyConfig `yaml:"honesty" json:"honesty"`
}

// parseHonestyBlock extracts the `honesty:` top-level mapping from the
// raw policy YAML bytes using yaml.v3. Returns the zero HonestyConfig
// and nil error when the block is absent. Mirrors parseGovernanceBlock.
//
// The second return value, present, reports whether a top-level
// `honesty:` key exists in the parsed YAML. It powers the
// "absent vs explicit" distinction in LoadPolicy (policy.go) so that
// normalizePolicy can substitute DefaultHonestyConfig() only when the
// operator omitted the section entirely. present is true even when the
// section is `honesty: null` or sets enabled:false — any explicit
// top-level honesty key counts as present.
//
// Structural errors (bad yaml, wrong node kind) bubble up so the loader
// can surface them to the operator.
func parseHonestyBlock(raw []byte) (HonestyConfig, bool, error) {
	var doc honestyConfigSchema
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return HonestyConfig{}, false, fmt.Errorf("honesty: yaml parse: %w", err)
	}
	return doc.Honesty, topLevelKeyPresent(raw, "honesty"), nil
}

// topLevelKeyPresent reports whether the raw policy YAML has the given
// top-level mapping key. It walks the document's root mapping node so a
// `<key>: null` or nested block still counts as present, while an
// omitted section does not. Shared by the honesty and skills block
// parsers (governance_config.go predates it and keeps its own copy).
func topLevelKeyPresent(raw []byte, key string) bool {
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
		if doc.Content[i].Value == key {
			return true
		}
	}
	return false
}
