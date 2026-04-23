// cmd/stoke/descent_bridge_hitl.go — spec-2 cloudswarm-protocol item 5.
//
// Wires the HITL approval service (internal/hitl) into the verification
// descent engine's T8 soft-pass gate. When the run is launched with
// --governance-tier=enterprise and a hitl.Service is threaded through
// the sow-native config, this builder returns a SoftPassApprovalFunc
// that calls hitl.Service.RequestApproval. The descent engine blocks
// on the returned decision and, on reject, returns DescentFail instead
// of DescentSoftPass.
//
// In every other configuration — community tier, missing HITL, empty
// GovernanceTier — the builder returns nil so plan.DescentConfig keeps
// its legacy auto-grant semantics (documented at
// internal/plan/verification_descent.go:383-392). This preserves
// backward compatibility for `stoke sow` / `stoke ship` callers that
// never populate the CloudSwarm fields.
//
// Contract (spec-2 §Descent Integration):
//   - ApprovalType is always "soft_pass"
//   - File carries the AC ID so CloudSwarm can thread the approval to
//     the correct descent node
//   - Context carries category + reasoning so the operator sees why
//     descent classified the failure as acceptable-as-is, plus the
//     session ID for cross-event correlation.
//
// No new third-party dependencies; only stdlib + internal packages.

package main

import (
	"context"

	"github.com/ericmacdougall/stoke/internal/hitl"
	"github.com/ericmacdougall/stoke/internal/plan"
)

// governanceTierEnterprise is the sentinel value that opts into
// HITL-gated soft-pass. Every other value (including the empty string)
// falls back to community-tier auto-grant.
const governanceTierEnterprise = "enterprise"

// buildSoftPassApprovalFunc returns a closure suitable for assigning
// to plan.DescentConfig.SoftPassApprovalFunc. The returned function is
// nil unless BOTH conditions are met:
//
//  1. cfg.HITL is non-nil (a hitl.Service was wired in by run_cmd.go)
//  2. cfg.GovernanceTier == "enterprise"
//
// A nil return is the legacy auto-grant path: the descent engine
// promotes a T8 candidate straight to DescentSoftPass. A non-nil
// return blocks descent on hitl.Service.RequestApproval, which:
//   - emits an hitl_required line on the critical lane
//   - reads the CloudSwarm supervisor's decision off stdin
//   - times out per the Service's configured deadline (15m enterprise
//     default, overridable via --hitl-timeout)
//
// sessionID flows into the approval Request Context so CloudSwarm can
// stitch the approval to the originating descent session on its side.
func buildSoftPassApprovalFunc(cfg sowNativeConfig, sessionID string) func(context.Context, plan.AcceptanceCriterion, plan.ReasoningVerdict) bool {
	if cfg.HITL == nil {
		return nil
	}
	if cfg.GovernanceTier != governanceTierEnterprise {
		return nil
	}
	svc := cfg.HITL
	return func(ctx context.Context, ac plan.AcceptanceCriterion, verdict plan.ReasoningVerdict) bool {
		d := svc.RequestApproval(ctx, hitl.Request{
			Reason:       "Descent T8 soft-pass approval",
			ApprovalType: "soft_pass",
			File:         ac.ID,
			Context: map[string]any{
				"session_id":       sessionID,
				"ac_id":            ac.ID,
				"ac_description":   ac.Description,
				"category":         verdict.Category,
				"reasoning":        verdict.Reasoning,
				"approve_reason":   verdict.ApproveReason,
				"governance_tier":  governanceTierEnterprise,
			},
		})
		return d.Approved
	}
}
