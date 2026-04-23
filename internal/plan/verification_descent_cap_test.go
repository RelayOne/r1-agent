package plan

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// TestPerFileRepairCap verifies spec-1 item 4: when a target file hits
// MaxRepairsPerFile consecutive T4 failures, the engine fails the AC
// without running RepairFunc again and without falling through to T5+.
// The cap-exceeded callback must be fired with file + attempts populated.
func TestPerFileRepairCap(t *testing.T) {
	var repairCalls int
	var capExceededCalls []struct {
		file     string
		attempts int
		lastErrs []string
	}

	ac := AcceptanceCriterion{
		ID:          "AC-CAP-1",
		Description: "cap fixture",
		Command:     "bash -c 'exit 1'", // always fails
		ContentMatch: &ContentMatchCriterion{
			File:    "src/a.ts",
			Pattern: "x",
		},
	}

	cfg := DescentConfig{
		RepoRoot: t.TempDir(),
		Session: Session{
			ID:    "S1",
			Title: "cap test",
			Tasks: []Task{{ID: "T1", Files: []string{"src/a.ts"}}},
			AcceptanceCriteria: []AcceptanceCriterion{ac},
		},
		MaxCodeRepairs:    5, // higher than per-file cap to force the cap to bite first
		MaxRepairsPerFile: 3,
		RepairFunc: func(ctx context.Context, directive string) error {
			repairCalls++
			return nil // dispatch succeeds but AC still fails
		},
		IntentCheckFunc: func(ctx context.Context, a AcceptanceCriterion) (bool, string) {
			return true, "intent confirmed"
		},
		OnFileCapExceeded: func(ac AcceptanceCriterion, file string, attempts int, lastErrors []string) {
			capExceededCalls = append(capExceededCalls, struct {
				file     string
				attempts int
				lastErrs []string
			}{file, attempts, lastErrors})
		},
	}

	result := VerificationDescent(context.Background(), ac, "initial: exit status 1", cfg)

	if result.Outcome != DescentFail {
		t.Errorf("expected DescentFail, got %s", result.Outcome)
	}
	if result.ResolvedAtTier != TierCodeRepair {
		t.Errorf("expected resolved at T4, got %v", result.ResolvedAtTier)
	}
	if !strings.Contains(result.Reason, "per-file repair cap exceeded") {
		t.Errorf("reason should mention cap exceeded: %q", result.Reason)
	}
	if !strings.Contains(result.Reason, "src/a.ts") {
		t.Errorf("reason should name the capped file: %q", result.Reason)
	}
	if repairCalls != 3 {
		t.Errorf("expected RepairFunc called 3 times (cap), got %d", repairCalls)
	}
	if len(capExceededCalls) != 1 {
		t.Fatalf("expected one cap-exceeded callback, got %d", len(capExceededCalls))
	}
	if capExceededCalls[0].file != "src/a.ts" {
		t.Errorf("expected file=src/a.ts, got %q", capExceededCalls[0].file)
	}
	if capExceededCalls[0].attempts < 3 {
		t.Errorf("expected attempts >=3, got %d", capExceededCalls[0].attempts)
	}
}

// TestPerFileRepairCap_DefaultValue verifies maxRepairsPerFile defaults
// to 3 when the field is unset or zero.
func TestPerFileRepairCap_DefaultValue(t *testing.T) {
	cfg := DescentConfig{}
	if got := cfg.maxRepairsPerFile(); got != 3 {
		t.Errorf("default MaxRepairsPerFile = %d, want 3", got)
	}
	cfg.MaxRepairsPerFile = 5
	if got := cfg.maxRepairsPerFile(); got != 5 {
		t.Errorf("explicit MaxRepairsPerFile = %d, want 5", got)
	}
}

// TestPerFileRepairCap_ResetsOnPass verifies that a successful re-run
// clears the counters for targeted files so a later failure starts
// fresh.
func TestPerFileRepairCap_ResetsOnPass(t *testing.T) {
	cfg := DescentConfig{MaxRepairsPerFile: 3}
	cfg.incrementFileRepairs([]string{"a.ts"})
	cfg.incrementFileRepairs([]string{"a.ts"})
	if cfg.FileRepairCounts["a.ts"] != 2 {
		t.Fatalf("expected 2 attempts recorded, got %d", cfg.FileRepairCounts["a.ts"])
	}
	cfg.resetFileRepairs([]string{"a.ts"})
	if cfg.FileRepairCounts["a.ts"] != 0 {
		t.Errorf("expected counter zeroed after reset, got %d", cfg.FileRepairCounts["a.ts"])
	}
}

// TestCollectRepairTargets exercises the fallback path extraction.
func TestCollectRepairTargets(t *testing.T) {
	ac := AcceptanceCriterion{
		ID:           "AC-1",
		ContentMatch: &ContentMatchCriterion{File: "src/a.ts", Pattern: "foo"},
	}
	targets := collectRepairTargets(ac, "src/b.ts:42:7 error: unexpected\nat src/b.ts:42:7\n", nil)
	if len(targets) < 2 {
		t.Fatalf("expected at least 2 targets, got %v", targets)
	}
	// Must include the AC's content-match file first.
	if targets[0] != "src/a.ts" {
		t.Errorf("expected src/a.ts first, got %v", targets)
	}
	// And the stderr-parsed path (de-duplicated).
	found := false
	for _, t0 := range targets {
		if t0 == "src/b.ts" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected src/b.ts in targets, got %v", targets)
	}
}

// TestCollectRepairTargets_Dedup verifies the same path isn't listed twice.
func TestCollectRepairTargets_Dedup(t *testing.T) {
	ac := AcceptanceCriterion{FileExists: "go.mod"}
	targets := collectRepairTargets(ac, fmt.Sprintf("first: %s\nsecond: %s", "go.mod", "go.mod"), nil)
	count := 0
	for _, t0 := range targets {
		if t0 == "go.mod" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected go.mod once, got %d (targets=%v)", count, targets)
	}
}
