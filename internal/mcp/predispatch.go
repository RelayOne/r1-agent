// Pre-dispatch policy gates shared by every MCP server in this
// package. Each MCP server (codebase, lanes, r1-stoke) runs a
// `tools/call` switch case that ultimately invokes HandleToolCall.
// Two policy gates have to fire BEFORE that handler runs:
//
//  1. Throttling (C3, this branch) — per-session + per-tenant token
//     buckets, fails closed with a structured retry_after_ms.
//  2. Prompt-injection validation (A1-T2, parallel branch) — scans
//     the tool input arguments for known injection shapes and may
//     redact / reject them.
//
// Both gates run as sibling checks here, NOT nested inside each
// other, so the two branches can both edit this file without
// conflicting on the per-server tools/call switch.
//
// The shared helper has these properties:
//   - Idempotent: callers can wire it into the dispatch site once;
//     re-wiring is a no-op.
//   - Conflict-resistant: throttle and promptguard hooks are exposed
//     as separately-settable function-typed fields on PreDispatch.
//     A1-T2's branch sets ValidateInput; C3's branch sets Throttler.
//     The two never touch each other's field.
//   - Side-effect-free on Allow: a nil-or-allow PreDispatch returns
//     PreDispatchAllow{}, the dispatch site is unchanged.
//
// See specs/per-tool-throttling.md task T7 and the analogous A1-T2
// spec for the contract.

package mcp

import (
	"context"
	"fmt"

	"github.com/RelayOne/r1/internal/throttle"
)

// PreDispatchDecision is what the pre-dispatch helper returns. The
// dispatch site short-circuits when Allowed=false and writes an
// MCP-shaped tool_result with isError=true plus the structured
// metadata in MetaError.
type PreDispatchDecision struct {
	Allowed   bool
	Message   string         // human-readable string for content[].text
	MetaError map[string]any // structured _meta.r1_error payload
}

// PreDispatchAllow is the trivial allow result; saves callers from
// constructing the struct literal at every callsite.
func PreDispatchAllow() PreDispatchDecision {
	return PreDispatchDecision{Allowed: true}
}

// ToolCallContext carries the bits each pre-dispatch hook needs.
// session_id / tenant_id come from params.meta on the MCP tools/call
// JSON-RPC envelope; the dispatch site is responsible for plumbing
// them through.
type ToolCallContext struct {
	Ctx       context.Context
	SessionID string
	TenantID  string
	ToolName  string
	Args      map[string]interface{}
	// Meta is the raw params.meta map from the JSON-RPC envelope so
	// hooks that need additional auth meta-channel data can read it.
	Meta map[string]interface{}
}

// PreDispatch is the function-typed bundle the dispatch site invokes.
// Throttler and ValidateInput are independent — promptguard's A1-T2
// branch sets only ValidateInput; this branch (C3) sets only
// Throttler. Either may be nil.
type PreDispatch struct {
	Throttler     throttle.Limiter
	ValidateInput func(tc ToolCallContext) PreDispatchDecision
}

// Check runs the throttle and validation gates in declared order and
// returns the FIRST denial. Allow returns PreDispatchAllow().
//
// Order is deliberate: throttle first (cheap, deterministic, doesn't
// look at args) then validation (potentially scans the entire
// arguments JSON for injection shapes). A throttle denial avoids the
// scan cost when the call would have been rejected anyway.
//
// Both gates are nil-safe so test harnesses can construct a partial
// PreDispatch.
func (pd PreDispatch) Check(tc ToolCallContext) PreDispatchDecision {
	if pd.Throttler != nil {
		dec := pd.Throttler.AllowMCP(tc.Ctx, tc.SessionID, tc.TenantID, tc.ToolName)
		if !dec.Allowed {
			return PreDispatchDecision{
				Allowed: false,
				Message: fmt.Sprintf("throttled: retry after %.1fs", dec.RetryAfter.Seconds()),
				MetaError: map[string]any{
					"code":           "tool/throttled",
					"scope":          string(dec.Scope),
					"principal":      dec.Principal,
					"tool":           dec.Tool,
					"retry_after_ms": dec.RetryAfter.Milliseconds(),
				},
			}
		}
	}
	if pd.ValidateInput != nil {
		dec := pd.ValidateInput(tc)
		if !dec.Allowed {
			return dec
		}
	}
	return PreDispatchAllow()
}

// extractAuthMeta pulls the session_id and tenant_id (plus the raw
// meta map) out of the JSON-RPC params.meta map. Helper used by
// every MCP server's tools/call switch case so we have ONE place to
// rename / migrate the wire key if the auth contract changes.
//
// Returns ("", "", nil) when meta is nil; defensive against malformed
// MCP envelopes (callers should not panic on a missing key).
func extractAuthMeta(meta map[string]interface{}) (sessionID, tenantID string, raw map[string]interface{}) {
	if meta == nil {
		return "", "", nil
	}
	if s, ok := meta["r1_session_id"].(string); ok {
		sessionID = s
	}
	if t, ok := meta["r1_tenant_id"].(string); ok {
		tenantID = t
	}
	return sessionID, tenantID, meta
}
