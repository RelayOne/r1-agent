package plan

// Integration-flavored tests for the verification-descent tier ladder.
//
// These exercise the full T1→T8 pipeline with deterministic callbacks and
// real file-system side effects (a RepairFunc that edits a marker file in
// t.TempDir) so the assertions are on observable state — which tiers
// actually fired, what DescentResult was produced, what gap class was
// recorded — rather than on mocked internals.
//
// Unlike the per-feature unit tests elsewhere in this package, each case
// here walks a multi-tier traversal and asserts on the tier sequence
// captured via OnTierEvent in addition to the terminal DescentResult.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDescentIntegration_T4RepairHappyPath exercises the happy path:
// T1 intent confirmed → T2 AC fails (exit 1) → T3 classifies as code_bug
// → T4 first repair no-op, second repair creates the marker file → AC
// passes on re-run, outcome is DescentPass at T4.
//
// Asserts on:
//   - final outcome and tier
//   - number of repair attempts recorded on the result
//   - tier-event sequence T1 → T2 → T3 → T4 (only — no T5-T8)
//   - repair dispatch was called exactly twice (not capped early)
func TestDescentIntegration_T4RepairHappyPath(t *testing.T) {
	repoRoot := t.TempDir()
	markerPath := filepath.Join(repoRoot, "marker.txt")

	ac := AcceptanceCriterion{
		ID:          "AC-HAPPY-1",
		Description: "marker file exists",
		Command:     "test -f marker.txt",
	}

	var repairCalls int
	var tiers []DescentTier

	cfg := DescentConfig{
		RepoRoot:       repoRoot,
		MaxCodeRepairs: 5,
		Session: Session{
			ID:    "S-happy",
			Title: "happy path",
			Tasks: []Task{{ID: "T1", Files: []string{"marker.txt"}}},
		},
		IntentCheckFunc: func(ctx context.Context, a AcceptanceCriterion) (bool, string) {
			return true, "intent matches"
		},
		RepairFunc: func(ctx context.Context, directive string) error {
			repairCalls++
			// First dispatch is a no-op (simulates a worker whose fix
			// didn't take). Second dispatch writes the marker file,
			// so the subsequent AC re-run passes.
			if repairCalls >= 2 {
				return os.WriteFile(markerPath, []byte("ok"), 0o600)
			}
			return nil
		},
		BuildCleanFunc:        func(ctx context.Context) bool { return true },
		StubScanCleanFunc:     func(ctx context.Context) bool { return true },
		AllOtherACsPassedFunc: func(acID string) bool { return true },
		OnTierEvent: func(evt DescentTierEvent) {
			tiers = append(tiers, evt.Tier)
		},
	}

	initial := "command failed: exit status 1\ntest: marker.txt: No such file or directory"
	result := VerificationDescent(context.Background(), ac, initial, cfg)

	if result.Outcome != DescentPass {
		t.Fatalf("outcome=%s reason=%q, want DescentPass", result.Outcome, result.Reason)
	}
	if result.ResolvedAtTier != TierCodeRepair {
		t.Errorf("resolved at tier %v, want TierCodeRepair", result.ResolvedAtTier)
	}
	if result.CodeRepairAttempts != 2 {
		t.Errorf("CodeRepairAttempts=%d, want 2", result.CodeRepairAttempts)
	}
	if repairCalls != 2 {
		t.Errorf("RepairFunc called %d times, want 2", repairCalls)
	}

	// Tier event sequence check: T1 (intent) → T2 (AC failed) → T3
	// (classify) → T4 (repair attempt 1) → T4 (repair attempt 2).
	// Terminal success on the second T4 re-run means NO T5/T6/T7/T8
	// events should have been emitted. The count of T4 events equals
	// the number of repair attempts.
	want := []DescentTier{
		TierIntentMatch, TierRunAC, TierClassify, TierCodeRepair, TierCodeRepair,
	}
	if len(tiers) != len(want) {
		t.Fatalf("tier event sequence=%v, want %v", tiers, want)
	}
	for i, tier := range want {
		if tiers[i] != tier {
			t.Errorf("tier[%d]=%v, want %v", i, tiers[i], tier)
		}
	}
	// Defensive: no tier above T4 should have fired.
	for _, tier := range tiers {
		if int(tier) > int(TierCodeRepair) {
			t.Errorf("unexpected tier %v in happy-path traversal", tier)
		}
	}
}

// TestDescentIntegration_CodeBugBlocksSoftPass verifies the "CODE_BUG
// never soft-passes" invariant from the tier-ladder contract:
//
//   T1 intent confirmed → T2 AC fails with assertion stderr → T3
//   classifies as code_bug (via stderr) → T4 exhausts repair budget
//   (all dispatches succeed but AC still fails) → result MUST be
//   DescentFail, never DescentSoftPass, even though every T8 gate
//   (intent, build, stub-scan, other-ACs) would otherwise pass.
//
// This is the invariant that prevents fake-pass-through on genuine
// code defects: the reason string must explicitly mention code_bug
// and the tier sequence must NOT include T8.
func TestDescentIntegration_CodeBugBlocksSoftPass(t *testing.T) {
	ac := AcceptanceCriterion{
		ID:          "AC-CODEBUG-1",
		Description: "assertion that never passes",
		// AssertionError pattern → stderr classifier gives
		// StderrAssertionFail → IsDefiniteCodeBug() → category=code_bug.
		Command: `echo "AssertionError: expected 1 to equal 2" && exit 1`,
	}

	var repairCalls int
	var tiers []DescentTier

	cfg := DescentConfig{
		RepoRoot:       t.TempDir(),
		MaxCodeRepairs: 3,
		// Per-file cap explicitly high so it can't trip before the
		// attempt budget does; this test wants to prove the final
		// code_bug rejection, not the cap path.
		MaxRepairsPerFile: 99,
		Session: Session{
			ID:    "S-codebug",
			Title: "codebug",
			Tasks: []Task{{ID: "T1", Description: "task"}},
		},
		IntentCheckFunc: func(ctx context.Context, a AcceptanceCriterion) (bool, string) {
			return true, "intent matches"
		},
		RepairFunc: func(ctx context.Context, directive string) error {
			repairCalls++
			return nil // dispatch succeeds but AC still fails
		},
		// Supply every soft-pass gate as passing — so the ONLY thing
		// stopping soft-pass is the code_bug category.
		BuildCleanFunc:        func(ctx context.Context) bool { return true },
		StubScanCleanFunc:     func(ctx context.Context) bool { return true },
		AllOtherACsPassedFunc: func(acID string) bool { return true },
		OnTierEvent: func(evt DescentTierEvent) {
			tiers = append(tiers, evt.Tier)
		},
	}

	result := VerificationDescent(context.Background(), ac, "initial fail", cfg)

	if result.Outcome != DescentFail {
		t.Fatalf("outcome=%s, want DescentFail (code_bug must NEVER soft-pass)", result.Outcome)
	}
	if result.ResolvedAtTier != TierCodeRepair {
		t.Errorf("resolved at tier %v, want TierCodeRepair", result.ResolvedAtTier)
	}
	if result.Category != "code_bug" {
		t.Errorf("category=%q, want code_bug", result.Category)
	}
	if result.CodeRepairAttempts != 3 {
		t.Errorf("CodeRepairAttempts=%d, want 3 (MaxCodeRepairs)", result.CodeRepairAttempts)
	}
	if repairCalls != 3 {
		t.Errorf("RepairFunc called %d times, want 3", repairCalls)
	}
	if !strings.Contains(result.Reason, "code_bug") {
		t.Errorf("reason should mention code_bug, got %q", result.Reason)
	}
	if !strings.Contains(result.Reason, "repair attempt") {
		t.Errorf("reason should mention repair attempts, got %q", result.Reason)
	}

	// Tier traversal must NOT include T8 — code_bug blocks the
	// descent from even reaching the soft-pass evaluation path.
	for _, tier := range tiers {
		if tier == TierSoftPass {
			t.Errorf("T8 (soft-pass) must not fire on code_bug path; got tiers=%v", tiers)
		}
	}
	// Must include T1, T2, T3, and T4 (exactly MaxCodeRepairs times).
	seenT4 := 0
	for _, tier := range tiers {
		if tier == TierCodeRepair {
			seenT4++
		}
	}
	if seenT4 != 3 {
		t.Errorf("expected 3 T4 events, got %d (tiers=%v)", seenT4, tiers)
	}
}

// TestDescentIntegration_PerFileCapTrips verifies spec-1 item 4: if a
// single target file has already accumulated MaxRepairsPerFile T4
// attempts on a prior AC in the same session, the current AC's T4
// tier must fail FAST without calling RepairFunc, and the
// OnFileCapExceeded observer must fire with the capped file name.
//
// Seeding: FileRepairCounts["src/shared.ts"]=3 with MaxRepairsPerFile=3.
// Current AC targets that same file via ContentMatch → T4 sees the cap
// hit on the very first attempt and short-circuits.
//
// Observable state asserted:
//   - Outcome = DescentFail, resolved at TierCodeRepair
//   - RepairFunc NEVER invoked (attempts==0) because cap trips first
//   - OnFileCapExceeded called exactly once with file="src/shared.ts"
//   - result.Reason explicitly names the capped file
//   - result.CodeRepairAttempts stays at 0 (attempts counter increments
//     only when the loop enters the dispatch body)
func TestDescentIntegration_PerFileCapTrips(t *testing.T) {
	ac := AcceptanceCriterion{
		ID:          "AC-CAP-INT-1",
		Description: "cap fixture on shared file",
		Command:     `echo "AssertionError: expected x but got y" && exit 1`,
		ContentMatch: &ContentMatchCriterion{
			File:    "src/shared.ts",
			Pattern: "export",
		},
	}

	var repairCalls int
	var capEvents []struct {
		file     string
		attempts int
	}

	cfg := DescentConfig{
		RepoRoot:          t.TempDir(),
		MaxCodeRepairs:    10,
		MaxRepairsPerFile: 3,
		// Pre-seed the counter to simulate two prior ACs having already
		// burned their repair budget on the same shared file.
		FileRepairCounts: map[string]int{
			"src/shared.ts": 3,
		},
		Session: Session{
			ID:    "S-cap-int",
			Title: "cap integration",
			Tasks: []Task{{ID: "T1", Files: []string{"src/shared.ts"}}},
		},
		IntentCheckFunc: func(ctx context.Context, a AcceptanceCriterion) (bool, string) {
			return true, "intent matches"
		},
		RepairFunc: func(ctx context.Context, directive string) error {
			repairCalls++
			return nil
		},
		BuildCleanFunc:        func(ctx context.Context) bool { return true },
		StubScanCleanFunc:     func(ctx context.Context) bool { return true },
		AllOtherACsPassedFunc: func(acID string) bool { return true },
		OnFileCapExceeded: func(ac AcceptanceCriterion, file string, attempts int, lastErrors []string) {
			capEvents = append(capEvents, struct {
				file     string
				attempts int
			}{file, attempts})
		},
	}

	result := VerificationDescent(context.Background(), ac, "initial fail", cfg)

	if result.Outcome != DescentFail {
		t.Fatalf("outcome=%s reason=%q, want DescentFail (cap must fail the AC)",
			result.Outcome, result.Reason)
	}
	if result.ResolvedAtTier != TierCodeRepair {
		t.Errorf("resolved at tier %v, want TierCodeRepair", result.ResolvedAtTier)
	}
	if repairCalls != 0 {
		t.Errorf("RepairFunc called %d times, want 0 (cap must trip before dispatch)", repairCalls)
	}
	if result.CodeRepairAttempts != 0 {
		t.Errorf("CodeRepairAttempts=%d, want 0 (loop must not enter dispatch body)",
			result.CodeRepairAttempts)
	}
	if !strings.Contains(result.Reason, "per-file repair cap exceeded") {
		t.Errorf("reason should mention per-file cap, got %q", result.Reason)
	}
	if !strings.Contains(result.Reason, "src/shared.ts") {
		t.Errorf("reason should name the capped file src/shared.ts, got %q", result.Reason)
	}
	if len(capEvents) != 1 {
		t.Fatalf("OnFileCapExceeded fired %d times, want 1", len(capEvents))
	}
	if capEvents[0].file != "src/shared.ts" {
		t.Errorf("cap event file=%q, want src/shared.ts", capEvents[0].file)
	}
	if capEvents[0].attempts < 3 {
		t.Errorf("cap event attempts=%d, want >=3 (seeded to 3)", capEvents[0].attempts)
	}
}
