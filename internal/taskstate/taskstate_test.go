package taskstate

import (
	"strings"
	"testing"
	"time"
)

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}

func TestHappyPath(t *testing.T) {
	ts := NewTaskState("T1")
	if ts.Phase() != Pending { t.Fatal("should start Pending") }

	// Harness claims task
	if err := ts.Advance(Claimed, "dispatched to pool claude-1"); err != nil {
		t.Fatal(err)
	}
	if ts.Phase() != Claimed { t.Fatal("should be Claimed") }

	// Agent returns (model CLAIMS done -- not verified yet)
	if err := ts.Advance(Executed, "agent returned success"); err != nil {
		t.Fatal(err)
	}
	if ts.Phase() != Executed { t.Fatal("should be Executed") }

	// Harness verifies (build+test+lint)
	if err := ts.Advance(Verified, "build pass, test pass, lint pass"); err != nil {
		t.Fatal(err)
	}

	// Cross-model review passes
	if err := ts.Advance(Reviewed, "codex review: LGTM"); err != nil {
		t.Fatal(err)
	}

	// Record attempt with full passing evidence (CanCommit now requires this)
	ts.RecordAttempt(Attempt{
		Number: 1, Engine: "claude",
		Evidence: Evidence{
			BuildPass: true, BuildOutput: "ok",
			TestPass: true, TestOutput: "all pass",
			LintPass: true, LintOutput: "clean",
			ScopeClean: true, ProtectedClean: true,
			ReviewEngine: "codex", ReviewPass: true, ReviewOutput: `{"pass":true}`,
			DiffSummary: "+++ files changed",
		},
	})
	if !ts.CanCommit() { t.Fatal("should be committable after review with passing evidence") }

	// Merge to main
	if err := ts.Advance(Committed, "merged to main"); err != nil {
		t.Fatal(err)
	}
	if ts.Phase() != Committed { t.Fatal("should be Committed") }
	if !ts.IsTerminal() { t.Fatal("Committed is terminal") }
}

func TestCannotSkipFromClaimedToCommitted(t *testing.T) {
	ts := NewTaskState("T1")
	ts.Advance(Claimed, "dispatched")

	err := ts.Advance(Committed, "model says it's done")
	if err == nil {
		t.Fatal("MUST NOT be able to skip from Claimed to Committed")
	}
}

func TestCannotSkipFromExecutedToCommitted(t *testing.T) {
	ts := NewTaskState("T1")
	ts.Advance(Claimed, "dispatched")
	ts.Advance(Executed, "agent returned")

	err := ts.Advance(Committed, "model says verified")
	if err == nil {
		t.Fatal("MUST NOT skip verification")
	}
}

func TestCannotSkipFromVerifiedToCommitted(t *testing.T) {
	ts := NewTaskState("T1")
	ts.Advance(Claimed, "dispatched")
	ts.Advance(Executed, "returned")
	ts.Advance(Verified, "build+test+lint pass")

	err := ts.Advance(Committed, "skip review")
	if err == nil {
		t.Fatal("MUST NOT skip cross-model review")
	}
}

func TestCannotSelfGrantDone(t *testing.T) {
	ts := NewTaskState("T1")
	// Try to go directly from Pending to Committed
	err := ts.Advance(Committed, "model declares victory")
	if err == nil {
		t.Fatal("MUST NOT go from Pending to Committed")
	}
}

func TestCannotTransitionOutOfTerminal(t *testing.T) {
	ts := NewTaskState("T1")
	ts.Advance(Claimed, "dispatched")
	ts.Advance(Failed, "crashed")

	// Failed can go to HumanNeeded (escalation)
	if err := ts.Advance(HumanNeeded, "escalating"); err != nil {
		t.Fatal("Failed -> HumanNeeded should be valid")
	}
	// But HumanNeeded is not terminal -- operator decides
}

func TestUserSkippedRequiresHumanNeeded(t *testing.T) {
	ts := NewTaskState("T1")
	ts.Advance(Claimed, "dispatched")

	// Cannot skip directly from Claimed
	err := ts.Advance(UserSkipped, "model wants to skip")
	if err == nil {
		t.Fatal("MUST NOT skip from Claimed -- only operator via HumanNeeded")
	}
}

func TestOperatorSkipPath(t *testing.T) {
	ts := NewTaskState("T1")
	ts.Advance(Claimed, "dispatched")
	ts.Advance(Failed, "build failed")
	ts.Advance(HumanNeeded, "escalating")
	ts.Advance(UserSkipped, "operator: skip this, known issue")

	if !ts.IsTerminal() { t.Fatal("UserSkipped should be terminal") }
	if ts.Phase() != UserSkipped { t.Fatal("should be UserSkipped") }
}

func TestOperatorRetryPath(t *testing.T) {
	ts := NewTaskState("T1")
	ts.Advance(Claimed, "dispatched")
	ts.Advance(Failed, "tests failed")
	ts.Advance(HumanNeeded, "escalating")
	// Operator says retry
	if err := ts.Advance(Claimed, "operator: retry with adjusted scope"); err != nil {
		t.Fatal("HumanNeeded -> Claimed should be valid for retry")
	}
	if ts.Phase() != Claimed { t.Fatal("should be back to Claimed") }
}

func TestEvidenceMandatory(t *testing.T) {
	ts := NewTaskState("T1")
	err := ts.RecordAttempt(Attempt{Number: 1})
	if err == nil {
		t.Fatal("MUST reject attempt with empty evidence")
	}
}

func TestEvidenceWithData(t *testing.T) {
	ts := NewTaskState("T1")
	err := ts.RecordAttempt(Attempt{
		Number:    1,
		StartedAt: time.Now(),
		Duration:  30 * time.Second,
		CostUSD:   0.05,
		Engine:    "claude",
		Evidence: Evidence{
			BuildOutput: "ok", BuildPass: true,
			TestOutput: "3 passed", TestPass: true,
			LintOutput: "clean", LintPass: true,
			ScopeClean: true, ProtectedClean: true,
			ReviewEngine: "codex", ReviewOutput: "LGTM", ReviewPass: true,
		},
	})
	if err != nil { t.Fatal(err) }
	if ts.LatestAttempt() == nil { t.Fatal("should have attempt") }
}

func TestAllGatesPass(t *testing.T) {
	e := Evidence{
		BuildPass: true, TestPass: true, LintPass: true,
		ScopeClean: true, ProtectedClean: true, ReviewPass: true,
	}
	if !e.AllGatesPass() { t.Fatal("all gates should pass") }
}

func TestFailedGates(t *testing.T) {
	e := Evidence{
		BuildPass: true, TestPass: false, LintPass: true,
		ScopeClean: false, ProtectedClean: true, ReviewPass: true,
	}
	if e.AllGatesPass() { t.Fatal("should not pass") }
	failed := e.FailedGates()
	if len(failed) != 2 { t.Fatalf("failed=%v, want 2", failed) }
}

func TestCanCommitOnlyFromReviewed(t *testing.T) {
	for _, phase := range []Phase{Pending, Claimed, Executed, Verified, Committed, Failed, Blocked} {
		ts := &TaskState{TaskID: "T", phase: phase}
		if ts.CanCommit() {
			t.Errorf("CanCommit should be false for phase %s", phase)
		}
	}
	// Reviewed phase but no attempts = not committable
	ts := &TaskState{TaskID: "T", phase: Reviewed}
	if ts.CanCommit() { t.Error("CanCommit should be false with no attempts") }

	// Reviewed with passing evidence = committable
	ts.Attempts = []Attempt{{
		Number: 1, Engine: "claude",
		Evidence: Evidence{
			BuildPass: true, TestPass: true, LintPass: true,
			ScopeClean: true, ProtectedClean: true,
			ReviewEngine: "codex", ReviewPass: true,
			DiffSummary: "+++ changed",
		},
	}}
	if !ts.CanCommit() { t.Error("CanCommit should be true with passing evidence") }

	// Reviewed with failure codes = not committable
	ts.Attempts = []Attempt{{
		Number: 1, Engine: "claude",
		Evidence: Evidence{
			BuildPass: true, TestPass: false, LintPass: true,
			ScopeClean: true, ProtectedClean: true,
			ReviewEngine: "codex", ReviewPass: true,
		},
		FailureCodes: []FailureCode{FailureTestsFailed},
	}}
	if ts.CanCommit() { t.Error("CanCommit should be false with failure codes") }
}

func TestAuditTrail(t *testing.T) {
	ts := NewTaskState("T1")
	ts.Advance(Claimed, "dispatched")
	ts.Advance(Executed, "returned")
	ts.Advance(Verified, "gates pass")
	ts.Advance(Reviewed, "review pass")
	ts.Advance(Committed, "merged")

	if len(ts.Transitions) != 5 { t.Fatalf("transitions=%d, want 5", len(ts.Transitions)) }
	if ts.Transitions[0].From != Pending || ts.Transitions[0].To != Claimed {
		t.Error("first transition should be Pending -> Claimed")
	}
	if ts.Transitions[4].To != Committed {
		t.Error("last transition should be -> Committed")
	}
	for _, tr := range ts.Transitions {
		if tr.Reason == "" { t.Error("every transition must have a reason") }
		if tr.Timestamp.IsZero() { t.Error("every transition must have a timestamp") }
	}
}

func TestPlanStateSummary(t *testing.T) {
	ps := NewPlanState([]string{"A", "B", "C"})
	ps.Get("A").Advance(Claimed, "go")
	ps.Get("A").Advance(Executed, "done")
	ps.Get("B").Advance(Blocked, "dep failed")

	summary := ps.Summary()
	if summary[Pending] != 1 { t.Errorf("pending=%d", summary[Pending]) }
	if summary[Executed] != 1 { t.Errorf("executed=%d", summary[Executed]) }
	if summary[Blocked] != 1 { t.Errorf("blocked=%d", summary[Blocked]) }
}

func TestPlanStateAllTerminal(t *testing.T) {
	ps := NewPlanState([]string{"A", "B"})
	if ps.AllTerminal() { t.Fatal("should not be all terminal") }

	ps.Get("A").Advance(Claimed, "x")
	ps.Get("A").Advance(Failed, "x")
	ps.Get("B").Advance(Blocked, "x")
	if !ps.AllTerminal() { t.Fatal("should be all terminal") }
}

func TestBlockedIsTerminal(t *testing.T) {
	ts := NewTaskState("T1")
	ts.Advance(Blocked, "dependency failed")
	if !ts.IsTerminal() { t.Fatal("Blocked should be terminal") }
}

func TestClaimedVsVerified(t *testing.T) {
	ts := NewTaskState("T1")
	ts.Advance(Claimed, "dispatched")
	ts.RecordAttempt(Attempt{
		Number: 1,
		Engine: "claude",
		ProposedSummary: "Implemented rate limiting and added tests",
		Evidence: Evidence{
			BuildPass: true, BuildOutput: "ok",
			TestPass: false, TestOutput: "1 failed",
			LintPass: true, LintOutput: "clean",
			ScopeClean: true, ProtectedClean: true,
			ReviewEngine: "codex", ReviewPass: false,
			ReviewOutput: "missing edge case",
			DiffSummary: "+++ rate_limit.ts",
		},
		FailureCodes: []FailureCode{FailureTestsFailed, FailureReviewRejected},
	})

	display := ts.ClaimedVsVerified()

	// Must show the model's claim
	if !contains(display, "Implemented rate limiting") {
		t.Error("should show agent's proposed summary")
	}
	// Must show the verified truth
	if !contains(display, "FAIL") {
		t.Error("should show FAIL for failed gates")
	}
	if !contains(display, "TESTS_FAILED") {
		t.Error("should show failure codes")
	}
}

func TestFingerprintDedup(t *testing.T) {
	ts := NewTaskState("T1")
	ts.RecordAttempt(Attempt{
		Number: 1, Engine: "claude",
		Evidence: Evidence{BuildPass: true, TestPass: false, TestOutput: "fail",
			LintPass: true, ScopeClean: true, ProtectedClean: true,
			ReviewEngine: "codex", ReviewPass: true},
		FailureCodes: []FailureCode{FailureTestsFailed},
		FailureDetails: []FailureDetail{{Code: FailureTestsFailed, File: "auth.ts", Message: "expected 401"}},
	})
	ts.RecordAttempt(Attempt{
		Number: 2, Engine: "claude",
		Evidence: Evidence{BuildPass: true, TestPass: false, TestOutput: "fail",
			LintPass: true, ScopeClean: true, ProtectedClean: true,
			ReviewEngine: "codex", ReviewPass: true},
		FailureCodes: []FailureCode{FailureTestsFailed},
		FailureDetails: []FailureDetail{{Code: FailureTestsFailed, File: "auth.ts", Message: "expected 401"}},
	})

	a1 := ts.Attempts[0]
	a2 := ts.Attempts[1]
	if a1.Fingerprint != a2.Fingerprint {
		t.Errorf("same failure should produce same fingerprint: %q vs %q", a1.Fingerprint, a2.Fingerprint)
	}
	// Same fingerprint twice = should escalate to human
}

func TestConcurrentAdvance(t *testing.T) {
	ts := NewTaskState("T1")
	ts.Advance(Claimed, "go")

	// Two goroutines race to advance from Claimed -> Executed.
	// Only one should succeed: the winner changes state to Executed,
	// then the loser tries Executed -> Executed which is invalid.
	errs := make(chan error, 2)
	go func() { errs <- ts.Advance(Executed, "agent 1") }()
	go func() { errs <- ts.Advance(Executed, "agent 2") }()

	e1 := <-errs
	e2 := <-errs

	// Exactly one should succeed, one should fail
	successes := 0
	if e1 == nil { successes++ }
	if e2 == nil { successes++ }
	if successes != 1 {
		t.Errorf("exactly one concurrent advance should succeed, got %d", successes)
	}
}
