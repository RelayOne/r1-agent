package plan

import (
	"context"
	"strings"
	"testing"
)

// The soft-pass HITL hook (spec-2 item 4) fires at T8 after all
// prerequisites pass (intent confirmed, not code_bug, build clean,
// stub clean, other ACs pass, active attempt made). We set up an
// environment-category AC (command-not-found → exit 127) that rides
// down to T8 and grants soft-pass via EnvFixAttempted=true.

func softPassHITLBaseCfg(t *testing.T) (AcceptanceCriterion, DescentConfig) {
	t.Helper()
	ac := AcceptanceCriterion{
		ID:          "AC-SP-HITL",
		Description: "command-not-found fixture (env category)",
		Command:     `echo "my-custom-verifier: command not found" && exit 127`,
	}
	cfg := DescentConfig{
		RepoRoot: t.TempDir(),
		Session: Session{
			ID:    "S-HITL",
			Title: "hitl",
			Tasks: []Task{{ID: "T1", Description: "task"}},
		},
		EnvFixFunc: func(ctx context.Context, rootCause, stderr string) bool {
			return false // tried but couldn't fix — soft-pass territory
		},
		IntentCheckFunc: func(ctx context.Context, ac AcceptanceCriterion) (bool, string) {
			return true, "code looks good"
		},
		BuildCleanFunc:        func(ctx context.Context) bool { return true },
		StubScanCleanFunc:     func(ctx context.Context) bool { return true },
		AllOtherACsPassedFunc: func(acID string) bool { return true },
	}
	return ac, cfg
}

// TestSoftPassApprovalFunc_Rejection verifies that when the HITL
// approver returns false, T8 returns DescentFail instead of
// DescentSoftPass. Spec-2 item 4.
func TestSoftPassApprovalFunc_Rejection(t *testing.T) {
	ac, cfg := softPassHITLBaseCfg(t)
	var approverCalled bool
	cfg.SoftPassApprovalFunc = func(ctx context.Context, a AcceptanceCriterion, v ReasoningVerdict) bool {
		approverCalled = true
		return false // reject
	}

	result := VerificationDescent(context.Background(), ac, "initial fail", cfg)

	if !approverCalled {
		t.Fatalf("expected SoftPassApprovalFunc to be called; outcome=%s reason=%q",
			result.Outcome, result.Reason)
	}
	if result.Outcome != DescentFail {
		t.Errorf("outcome=%s, want DescentFail (approver rejected)", result.Outcome)
	}
	if !strings.Contains(result.Reason, "HITL approver rejected") {
		t.Errorf("reason should mention rejection: %q", result.Reason)
	}
	if result.ResolvedAtTier != TierSoftPass {
		t.Errorf("tier=%v, want TierSoftPass", result.ResolvedAtTier)
	}
}

// TestSoftPassApprovalFunc_Approval verifies approval → DescentSoftPass.
func TestSoftPassApprovalFunc_Approval(t *testing.T) {
	ac, cfg := softPassHITLBaseCfg(t)
	var approverCalled bool
	cfg.SoftPassApprovalFunc = func(ctx context.Context, a AcceptanceCriterion, v ReasoningVerdict) bool {
		approverCalled = true
		return true // approve
	}

	result := VerificationDescent(context.Background(), ac, "initial fail", cfg)

	if !approverCalled {
		t.Fatalf("expected approver called; outcome=%s reason=%q",
			result.Outcome, result.Reason)
	}
	if result.Outcome != DescentSoftPass {
		t.Errorf("outcome=%s, want DescentSoftPass (approver approved); reason=%q",
			result.Outcome, result.Reason)
	}
}

// TestSoftPassApprovalFunc_NilPreservesLegacyBehavior verifies nil
// approver means auto-grant (community-tier default).
func TestSoftPassApprovalFunc_NilPreservesLegacyBehavior(t *testing.T) {
	ac, cfg := softPassHITLBaseCfg(t)
	// SoftPassApprovalFunc left nil.

	result := VerificationDescent(context.Background(), ac, "initial fail", cfg)

	if result.Outcome != DescentSoftPass {
		t.Errorf("outcome=%s, want DescentSoftPass (legacy auto-grant); reason=%q",
			result.Outcome, result.Reason)
	}
}
