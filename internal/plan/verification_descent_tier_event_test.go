package plan

import (
	"context"
	"testing"
)

// TestOnTierEventFiresPerTier verifies that the structured tier-event
// hook (spec-2 item 6) receives one event per tier boundary a descent
// run traverses. We exercise a soft-pass path (T1 -> T2 fail -> T3
// classify env -> T5 env-fix fail -> T8 gates pass) and assert the
// collected tier set matches the expected traversal.
func TestOnTierEventFiresPerTier(t *testing.T) {
	ac := AcceptanceCriterion{
		ID:          "AC-TIER-1",
		Description: "command-not-found fixture for tier traversal",
		Command:     `echo "custom-verifier: command not found" && exit 127`,
	}

	var tiers []DescentTier
	cfg := DescentConfig{
		RepoRoot: t.TempDir(),
		Session: Session{
			ID:    "S-TIER",
			Title: "tier-events",
			Tasks: []Task{{ID: "T1", Description: "task"}},
		},
		EnvFixFunc: func(ctx context.Context, rootCause, stderr string) bool {
			return false
		},
		IntentCheckFunc: func(ctx context.Context, a AcceptanceCriterion) (bool, string) {
			return true, "ok"
		},
		BuildCleanFunc:        func(ctx context.Context) bool { return true },
		StubScanCleanFunc:     func(ctx context.Context) bool { return true },
		AllOtherACsPassedFunc: func(acID string) bool { return true },
		OnTierEvent: func(evt DescentTierEvent) {
			tiers = append(tiers, evt.Tier)
		},
	}

	result := VerificationDescent(context.Background(), ac, "initial fail", cfg)
	if result.Outcome != DescentSoftPass {
		t.Fatalf("outcome=%s, want DescentSoftPass", result.Outcome)
	}

	// Expected traversal: T1 (intent) -> T2 (run AC fail) -> T3
	// (classify) -> T5 (env fix) -> T8 (soft-pass gates).
	want := []DescentTier{TierIntentMatch, TierRunAC, TierClassify, TierEnvFix, TierSoftPass}
	if len(tiers) != len(want) {
		t.Fatalf("tier event count=%d (%v), want %d (%v)",
			len(tiers), tiers, len(want), want)
	}
	for i, tier := range want {
		if tiers[i] != tier {
			t.Errorf("tier[%d]=%v, want %v", i, tiers[i], tier)
		}
	}
}

// TestOnTierEventCarriesCategory verifies the T3 event's Category
// field is populated (used by the streamjson bridge to emit
// _stoke.dev/category on descent.tier observability lines).
func TestOnTierEventCarriesCategory(t *testing.T) {
	ac := AcceptanceCriterion{
		ID:          "AC-TIER-2",
		Description: "env category",
		Command:     `echo "xxx: command not found" && exit 127`,
	}
	var t3 *DescentTierEvent
	cfg := DescentConfig{
		RepoRoot: t.TempDir(),
		Session: Session{
			ID:    "S-TIER-2",
			Title: "tier-events-2",
			Tasks: []Task{{ID: "T1"}},
		},
		EnvFixFunc: func(ctx context.Context, rc, stderr string) bool { return false },
		IntentCheckFunc: func(ctx context.Context, a AcceptanceCriterion) (bool, string) {
			return true, "ok"
		},
		BuildCleanFunc:        func(ctx context.Context) bool { return true },
		StubScanCleanFunc:     func(ctx context.Context) bool { return true },
		AllOtherACsPassedFunc: func(acID string) bool { return true },
		OnTierEvent: func(evt DescentTierEvent) {
			if evt.Tier == TierClassify {
				tmp := evt
				t3 = &tmp
			}
		},
	}
	_ = VerificationDescent(context.Background(), ac, "initial fail", cfg)
	if t3 == nil {
		t.Fatalf("expected a T3 event")
	}
	if t3.Category != "environment" {
		t.Errorf("T3 category=%q, want environment", t3.Category)
	}
	if t3.ACID != "AC-TIER-2" {
		t.Errorf("T3 ACID=%q, want AC-TIER-2", t3.ACID)
	}
}
