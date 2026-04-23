// Package policy is Stoke's fail-closed authorization engine.
//
// Two tiers share a single Client interface:
//   - Tier 1 (YAMLClient): local YAML rules, evaluated top-to-bottom, first match wins.
//   - Tier 2 (HTTPClient): Cedar-agent PARC endpoint at /v1/is_authorized.
//
// The zero-value Decision is Deny, so any unset field or uninitialized
// Result counts as denial. All transport errors, 5xx responses, parse
// failures, and timeouts also resolve to Deny with Errors populated.
// This is the fail-closed invariant: the engine errs toward denying
// the action, never toward permitting it.
//
// NewFromEnv selects the backend by precedence:
//  1. CLOUDSWARM_POLICY_ENDPOINT  (cedar-agent over HTTPS)
//  2. STOKE_POLICY_FILE           (local YAML)
//  3. (unset)                     (NullClient — dev-mode, prints a banner)
//
// Policy events stream through internal/streamjson as stoke.policy.check
// and stoke.policy.denied. Deny decisions include principal, action,
// resource, and the reasons array so operators can audit every block.
package policy
