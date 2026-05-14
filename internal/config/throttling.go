package config

import (
	throttlepolicy "github.com/RelayOne/r1/internal/throttle/policy"
)

// ThrottlingConfig is the parsed `throttling:` top-level block in
// r1.policy.yaml. The actual schema lives in
// internal/throttle/policy so that the throttle package can import
// the types without dragging in the rest of internal/config (which
// imports internal/mcp and would create a cycle). See
// specs/per-tool-throttling.md §Data Models.
type ThrottlingConfig = throttlepolicy.Config

// ThrottleScoped is the per-session/per-tenant Limit pair.
type ThrottleScoped = throttlepolicy.Scoped

// ThrottleLimit is the YAML-facing rate-string + burst pair.
type ThrottleLimit = throttlepolicy.Limit

// ThrottleOverride is the principal-glob multiplier rule.
type ThrottleOverride = throttlepolicy.Override

// ThrottleEffective is the parsed (Rate, Burst) pair the limiter consumes.
type ThrottleEffective = throttlepolicy.Effective

// parseThrottlingBlock extracts the optional top-level `throttling:`
// section from raw policy YAML. Returns a zero Config (and nil error)
// when the section is absent. Thin shim around policy.ParseBlock.
func parseThrottlingBlock(raw []byte) (ThrottlingConfig, error) {
	return throttlepolicy.ParseBlock(raw)
}

// ValidateThrottling enforces the structural rules from spec T1.
// Thin shim around policy.Validate so callers outside the throttle
// package don't need a second import.
func ValidateThrottling(cfg ThrottlingConfig) (ThrottlingConfig, error) {
	return throttlepolicy.Validate(cfg)
}
