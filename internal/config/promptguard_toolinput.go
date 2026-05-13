// internal/config/promptguard_toolinput.go
//
// YAML-block parser for the `promptguard.tool_input:` section of
// r1.policy.yaml. The custom line-scanner in parsePolicyYAML skips the
// tool_input block (it carries a nested-map shape the scanner does not
// model) and this file's parsePromptGuardToolInputBlock parses it via
// yaml.v3 — same pattern as parseMCPServersBlock (mcp_servers.go) and
// parseCortexBlock (schema.go).
//
// See specs/promptguard-hardening.md §T2 item 10.
package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// promptGuardToolInputSchema is the on-disk shape used to decode the
// `promptguard.tool_input:` block via yaml.v3. Kept private — callers
// receive a fully-populated PromptGuardToolInputConfig.
type promptGuardToolInputSchema struct {
	PromptGuard struct {
		ToolInput PromptGuardToolInputConfig `yaml:"tool_input"`
	} `yaml:"promptguard"`
}

// parsePromptGuardToolInputBlock extracts the
// `promptguard.tool_input:` block from raw policy YAML bytes via
// yaml.v3. Returns a zero-value PromptGuardToolInputConfig (and nil
// error) when the block is absent. Mirrors parseMCPServersBlock and
// parseCortexBlock.
//
// Structural errors (bad yaml, wrong node kind) bubble up so the
// LoadPolicy caller can surface them to the operator. Malformed rule
// bodies (e.g. bad regex) are not validated here — that happens at
// Apply time so a regex-compile error doesn't fail policy load.
func parsePromptGuardToolInputBlock(raw []byte) (PromptGuardToolInputConfig, error) {
	var doc promptGuardToolInputSchema
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return PromptGuardToolInputConfig{}, fmt.Errorf("promptguard.tool_input: yaml parse: %w", err)
	}
	return doc.PromptGuard.ToolInput, nil
}
