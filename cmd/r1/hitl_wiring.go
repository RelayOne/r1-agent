package main

// hitl_wiring.go — audit A032: the seams that turn the HITL approval
// service (internal/hitl) into an actual descent gate.
//
// Two pieces, both consumed by sowCmd:
//
//   - hitlWaitCeiling resolves the RequestApproval wait ceiling from
//     the governance tier + the --hitl-timeout override. Shared with
//     the run_cmd_hitl_test.go contract test so the tier defaults
//     (community=1h, enterprise=15m) have one source of truth.
//
//   - buildSoftPassApprovalFunc builds the plan.DescentConfig
//     SoftPassApprovalFunc (verification_descent.go T8): non-nil ONLY
//     when GovernanceTier=enterprise AND an HITL service exists. On
//     community tier (or with no service) it returns nil, so descent
//     auto-grants soft-passes exactly as before.

import (
	"context"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/hitl"
	"github.com/RelayOne/r1/internal/plan"
)

// hitlWaitCeiling returns the HITL approval wait ceiling: an explicit
// positive override wins; otherwise enterprise waits 15m and community
// (or any other tier value) waits 1h. Spec-2 item 12 defaults.
func hitlWaitCeiling(tier string, override time.Duration) time.Duration {
	if override > 0 {
		return override
	}
	if strings.EqualFold(tier, "enterprise") {
		return 15 * time.Minute
	}
	return time.Hour
}

// buildSoftPassApprovalFunc returns the descent T8 soft-pass approval
// callback, or nil for auto-grant. Non-nil requires BOTH an HITL
// service and the enterprise governance tier (case-insensitive). The
// callback emits hitl_required (via hitl.Service) and blocks until the
// operator's stdin decision, the wait ceiling, or ctx cancellation;
// every non-approval path returns false, which descent turns into a
// FAIL instead of a soft-pass.
func buildSoftPassApprovalFunc(svc *hitl.Service, tier string) func(context.Context, plan.AcceptanceCriterion, plan.ReasoningVerdict) bool {
	if svc == nil || !strings.EqualFold(tier, "enterprise") {
		return nil
	}
	return func(ctx context.Context, ac plan.AcceptanceCriterion, verdict plan.ReasoningVerdict) bool {
		d := svc.RequestApproval(ctx, hitl.Request{
			Reason:       "soft-pass approval: " + ac.ID,
			ApprovalType: "soft_pass",
			Context: map[string]any{
				"ac_id":       ac.ID,
				"description": ac.Description,
				"category":    verdict.Category,
				"reasoning":   verdict.Reasoning,
			},
		})
		return d.Approved
	}
}
