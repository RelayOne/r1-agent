// Copyright (c) 2026 Good Ventures.
// SPDX-License-Identifier: Apache-2.0

package r1rename

import (
	"os"
	"strconv"
	"strings"
)

// MCP tool-name dual-registration window per work-r1-rename.md S1-4.
// Canonical r1_* and legacy stoke_* are both registered until v2.0.0
// (>=2-week external notice). MCPLegacyDropEnv flips the legacy half
// off for v2.0.0 cutover deployments.

// MCPLegacyDropEnv is the feature-flag env var that disables legacy
// stoke_* MCP tool registration when set to a truthy value. Default
// behaviour (unset / "false") leaves dual-registration active.
const MCPLegacyDropEnv = "R1_MCP_LEGACY_DROP"

const (
	// MCPLegacyToolPrefix is the pre-rename MCP tool-name prefix.
	MCPLegacyToolPrefix = "stoke_"
	// MCPCanonicalToolPrefix is the post-rename MCP tool-name prefix.
	MCPCanonicalToolPrefix = "r1_"
)

// CanonicalToolName converts a legacy stoke_* MCP tool name to its
// canonical r1_* form. Names without the legacy prefix are returned
// unchanged so callers can apply the helper to any tool name.
func CanonicalToolName(legacy string) string {
	if strings.HasPrefix(legacy, MCPLegacyToolPrefix) {
		return MCPCanonicalToolPrefix + strings.TrimPrefix(legacy, MCPLegacyToolPrefix)
	}
	return legacy
}

// LegacyToolName converts a canonical r1_* MCP tool name back to the
// legacy stoke_* form. Used by dispatchers so a single switch case per
// primitive resolves either prefix to the same handler. Names without
// the canonical prefix are returned unchanged.
func LegacyToolName(canonical string) string {
	if strings.HasPrefix(canonical, MCPCanonicalToolPrefix) {
		return MCPLegacyToolPrefix + strings.TrimPrefix(canonical, MCPCanonicalToolPrefix)
	}
	return canonical
}

// MCPLegacyDropEnabled reports whether R1_MCP_LEGACY_DROP is set to a
// truthy value. Resolved per-call (not cached) so a deployment config
// flipped between calls takes effect on the next read. Tool servers
// consult this at registration time to decide whether to publish the
// legacy half of each pair.
func MCPLegacyDropEnabled() bool {
	v, ok := os.LookupEnv(MCPLegacyDropEnv)
	if !ok {
		return false
	}
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return false
	}
	return b
}
