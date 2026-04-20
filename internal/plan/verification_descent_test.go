package plan

import (
	"context"
	"fmt"
	"os/exec"
	"testing"
)

// ---------------------------------------------------------------------------
// StderrClass tests
// ---------------------------------------------------------------------------

func TestClassifyStderr(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		exitCode int
		want     StderrClass
	}{
		{
			name:     "exit 127 command not found",
			output:   "bash: tsc: command not found",
			exitCode: 127,
			want:     StderrCommandNotFound,
		},
		{
			name:     "command not found in stderr",
			output:   "command failed: exit status 127\ntsc: command not found",
			exitCode: 127,
			want:     StderrCommandNotFound,
		},
		{
			name:     "module not found",
			output:   "Error: Cannot find module '@acme/shared'\nRequire stack:\n- /app/index.js",
			exitCode: 1,
			want:     StderrModuleNotFound,
		},
		{
			name:     "ERR_MODULE_NOT_FOUND",
			output:   "Error [ERR_MODULE_NOT_FOUND]: Cannot find package 'zod'",
			exitCode: 1,
			want:     StderrModuleNotFound,
		},
		{
			name:     "typescript compile error",
			output:   "src/index.ts(5,1): error TS2304: Cannot find name 'foo'.",
			exitCode: 1,
			want:     StderrCompileError,
		},
		{
			name:     "rust compile error",
			output:   "error[E0433]: failed to resolve: use of undeclared crate",
			exitCode: 1,
			want:     StderrCompileError,
		},
		{
			name:     "assertion failure",
			output:   "AssertionError: expected 1 to equal 2",
			exitCode: 1,
			want:     StderrAssertionFail,
		},
		{
			name:     "vitest assertion",
			output:   "FAIL  src/index.test.ts\n expected [...] to have a length of 1 but got 2",
			exitCode: 1,
			want:     StderrAssertionFail,
		},
		{
			name:     "syntax error",
			output:   "SyntaxError: Unexpected token 'import'",
			exitCode: 1,
			want:     StderrSyntaxError,
		},
		{
			name:     "env missing",
			output:   "Error: required environment variable DATABASE_URL not set",
			exitCode: 1,
			want:     StderrEnvMissing,
		},
		{
			name:     "timeout",
			output:   "context deadline exceeded",
			exitCode: -1,
			want:     StderrTimeout,
		},
		{
			name:     "generic failure",
			output:   "something went wrong",
			exitCode: 1,
			want:     StderrUnclassified,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyStderr(tt.output, tt.exitCode)
			if got != tt.want {
				t.Errorf("ClassifyStderr(%q, %d) = %s, want %s",
					tt.output, tt.exitCode, got, tt.want)
			}
		})
	}
}

func TestStderrClassIsEnvironmentProblem(t *testing.T) {
	envClasses := []StderrClass{StderrCommandNotFound, StderrModuleNotFound, StderrEnvMissing}
	nonEnvClasses := []StderrClass{StderrSyntaxError, StderrAssertionFail, StderrCompileError, StderrTimeout, StderrUnclassified}

	for _, c := range envClasses {
		if !c.IsEnvironmentProblem() {
			t.Errorf("%s.IsEnvironmentProblem() = false, want true", c)
		}
	}
	for _, c := range nonEnvClasses {
		if c.IsEnvironmentProblem() {
			t.Errorf("%s.IsEnvironmentProblem() = true, want false", c)
		}
	}
}

func TestStderrClassIsDefiniteCodeBug(t *testing.T) {
	codeClasses := []StderrClass{StderrAssertionFail, StderrCompileError}
	nonCodeClasses := []StderrClass{StderrCommandNotFound, StderrModuleNotFound, StderrSyntaxError, StderrEnvMissing, StderrTimeout, StderrUnclassified}

	for _, c := range codeClasses {
		if !c.IsDefiniteCodeBug() {
			t.Errorf("%s.IsDefiniteCodeBug() = false, want true", c)
		}
	}
	for _, c := range nonCodeClasses {
		if c.IsDefiniteCodeBug() {
			t.Errorf("%s.IsDefiniteCodeBug() = true, want false", c)
		}
	}
}

// ---------------------------------------------------------------------------
// extractExitCode tests
// ---------------------------------------------------------------------------

func TestExtractExitCode(t *testing.T) {
	tests := []struct {
		output string
		want   int
	}{
		{"command failed: exit status 127\nbash: tsc: command not found", 127},
		{"command failed: exit status 1\ntest failed", 1},
		{"command failed: exit status 2\n", 2},
		{"context deadline exceeded", -1},
		{"signal: killed", -1},
		{"some random failure", 1}, // default
	}
	for _, tt := range tests {
		got := extractExitCode(tt.output)
		if got != tt.want {
			t.Errorf("extractExitCode(%q) = %d, want %d", tt.output[:min(len(tt.output), 40)], got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// VerificationDescent unit tests (no LLM, deterministic callbacks)
// ---------------------------------------------------------------------------

func TestDescentPassesOnMechanicalSuccess(t *testing.T) {
	// Simulate an AC that passes immediately.
	ac := AcceptanceCriterion{
		ID:          "AC1",
		Description: "echo true passes",
		Command:     "true",
	}

	cfg := DescentConfig{
		RepoRoot: t.TempDir(),
		Session:  Session{ID: "S1", Title: "test"},
	}

	result := VerificationDescent(context.Background(), ac, "initial fail", cfg)

	if result.Outcome != DescentPass {
		t.Errorf("expected PASS, got %s: %s", result.Outcome, result.Reason)
	}
	if result.ResolvedAtTier != TierRunAC {
		t.Errorf("expected tier T2, got %s", result.ResolvedAtTier)
	}
}

func TestDescentFailsOnCodeBugWithoutRepairFunc(t *testing.T) {
	// AC that always fails with an assertion error.
	ac := AcceptanceCriterion{
		ID:          "AC1",
		Description: "test assertion",
		Command:     `echo "expected foo but got bar" && exit 1`,
	}

	cfg := DescentConfig{
		RepoRoot: t.TempDir(),
		Session:  Session{ID: "S1", Title: "test"},
		// No RepairFunc — T4 can't fix code_bug.
	}

	result := VerificationDescent(context.Background(), ac, "initial fail", cfg)

	if result.Outcome != DescentFail {
		t.Errorf("expected FAIL, got %s: %s", result.Outcome, result.Reason)
	}
	// stderr classifier sees assertion failure → code_bug → no repair → fail
	if result.Category != "code_bug" {
		t.Errorf("expected category code_bug, got %q", result.Category)
	}
}

func TestDescentSoftPassOnEnvironmentFailure(t *testing.T) {
	// AC that fails with "command not found" for a binary NOT in
	// checkOneCriterion's H-77 system-binary allow-list. Using a
	// non-allowlisted name ensures H-77 doesn't auto-pass this
	// before the descent engine reaches it.
	ac := AcceptanceCriterion{
		ID:          "AC1",
		Description: "run custom binary check",
		Command:     `echo "my-custom-verifier: command not found" && exit 127`,
	}

	cfg := DescentConfig{
		RepoRoot: t.TempDir(),
		Session: Session{
			ID:    "S1",
			Title: "test",
			Tasks: []Task{{ID: "T1", Description: "test task"}},
		},
		// No RepairFunc, no EnvFixFunc — but env fix attempted = false.
		// For soft-pass, we need at least one active attempt.
		// Supply a no-op env fix that "tries" but fails.
		EnvFixFunc: func(ctx context.Context, rootCause, stderr string) bool {
			return false // tried but couldn't fix
		},
		IntentCheckFunc: func(ctx context.Context, ac AcceptanceCriterion) (bool, string) {
			return true, "code looks good"
		},
		BuildCleanFunc:    func(ctx context.Context) bool { return true },
		StubScanCleanFunc: func(ctx context.Context) bool { return true },
		AllOtherACsPassedFunc: func(acID string) bool { return true },
	}

	result := VerificationDescent(context.Background(), ac, "initial fail", cfg)

	if result.Outcome != DescentSoftPass {
		t.Errorf("expected SOFT-PASS, got %s: %s", result.Outcome, result.Reason)
	}
	if result.Category != "environment" {
		t.Errorf("expected category environment, got %q", result.Category)
	}
	if !result.EnvFixAttempted {
		t.Error("expected EnvFixAttempted=true")
	}
}

func TestDescentCodeBugNeverSoftPasses(t *testing.T) {
	// AC with assertion failure. Even with all soft-pass prerequisites
	// met, code_bug must fail.
	ac := AcceptanceCriterion{
		ID:          "AC1",
		Description: "test assertion",
		Command:     `echo "FAIL: expected 1 to equal 2" && exit 1`,
	}

	repairAttempts := 0
	cfg := DescentConfig{
		RepoRoot:       t.TempDir(),
		MaxCodeRepairs: 2,
		Session:        Session{ID: "S1", Title: "test"},
		RepairFunc: func(ctx context.Context, directive string) error {
			repairAttempts++
			return nil // "repair" runs but AC will still fail
		},
		IntentCheckFunc: func(ctx context.Context, ac AcceptanceCriterion) (bool, string) {
			return true, "intent confirmed"
		},
		BuildCleanFunc:    func(ctx context.Context) bool { return true },
		StubScanCleanFunc: func(ctx context.Context) bool { return true },
		AllOtherACsPassedFunc: func(acID string) bool { return true },
	}

	result := VerificationDescent(context.Background(), ac, "initial fail", cfg)

	if result.Outcome != DescentFail {
		t.Errorf("expected FAIL (code_bug never soft-passes), got %s: %s",
			result.Outcome, result.Reason)
	}
	if repairAttempts != 2 {
		t.Errorf("expected 2 repair attempts, got %d", repairAttempts)
	}
}

func TestDescentPassesAfterRepair(t *testing.T) {
	// AC fails first time, passes after repair.
	callCount := 0
	ac := AcceptanceCriterion{
		ID:          "AC1",
		Description: "test that passes after repair",
		Command:     `test -f /tmp/stoke-descent-test-marker`,
	}

	cfg := DescentConfig{
		RepoRoot:       t.TempDir(),
		MaxCodeRepairs: 3,
		Session:        Session{ID: "S1", Title: "test"},
		IntentCheckFunc: func(ctx context.Context, ac AcceptanceCriterion) (bool, string) {
			return true, "ok"
		},
		RepairFunc: func(ctx context.Context, directive string) error {
			callCount++
			if callCount >= 2 {
				// Create the marker file on second repair.
				return exec.Command("touch", "/tmp/stoke-descent-test-marker").Run()
			}
			return nil
		},
	}

	// Clean up marker.
	defer exec.Command("rm", "-f", "/tmp/stoke-descent-test-marker").Run()

	result := VerificationDescent(context.Background(), ac, "initial fail", cfg)

	if result.Outcome != DescentPass {
		t.Errorf("expected PASS, got %s: %s", result.Outcome, result.Reason)
	}
	if result.ResolvedAtTier != TierCodeRepair {
		t.Errorf("expected tier T4, got %s", result.ResolvedAtTier)
	}
	if result.CodeRepairAttempts != 2 {
		t.Errorf("expected 2 repair attempts, got %d", result.CodeRepairAttempts)
	}
}

func TestDescentNoSoftPassWithoutActiveAttempt(t *testing.T) {
	// Environment failure but no env fix function and no repair function.
	// Cannot soft-pass without having tried anything.
	// Use a non-allowlisted binary so H-77 doesn't auto-pass.
	ac := AcceptanceCriterion{
		ID:          "AC1",
		Description: "run custom tool check",
		Command:     `echo "my-custom-tool: command not found" && exit 127`,
	}

	cfg := DescentConfig{
		RepoRoot: t.TempDir(),
		Session:  Session{ID: "S1", Title: "test"},
		IntentCheckFunc: func(ctx context.Context, ac AcceptanceCriterion) (bool, string) {
			return true, "intent ok"
		},
		BuildCleanFunc:    func(ctx context.Context) bool { return true },
		StubScanCleanFunc: func(ctx context.Context) bool { return true },
		AllOtherACsPassedFunc: func(acID string) bool { return true },
		// No RepairFunc, no EnvFixFunc — zero active attempts.
	}

	result := VerificationDescent(context.Background(), ac, "initial fail", cfg)

	if result.Outcome != DescentFail {
		t.Errorf("expected FAIL (no active attempts), got %s: %s",
			result.Outcome, result.Reason)
	}
}

func TestDescentIntentFailShortCircuits(t *testing.T) {
	ac := AcceptanceCriterion{
		ID:          "AC1",
		Description: "test",
		Command:     "false",
	}

	cfg := DescentConfig{
		RepoRoot: t.TempDir(),
		Session:  Session{ID: "S1", Title: "test"},
		IntentCheckFunc: func(ctx context.Context, ac AcceptanceCriterion) (bool, string) {
			return false, "code doesn't implement the login page at all"
		},
	}

	result := VerificationDescent(context.Background(), ac, "initial fail", cfg)

	if result.Outcome != DescentFail {
		t.Errorf("expected FAIL, got %s", result.Outcome)
	}
	if result.ResolvedAtTier != TierIntentMatch {
		t.Errorf("expected T1, got %s", result.ResolvedAtTier)
	}
}

// ---------------------------------------------------------------------------
// DescentSessionSummary tests
// ---------------------------------------------------------------------------

func TestDescentSessionSummaryAllResolved(t *testing.T) {
	s := DescentSessionSummary{Total: 3, Passed: 2, SoftPass: 1, Failed: 0}
	if !s.AllResolved() {
		t.Error("expected AllResolved() = true")
	}
	s.Failed = 1
	s.SoftPass = 0
	if s.AllResolved() {
		t.Error("expected AllResolved() = false")
	}
}

func TestDescentOutcomeString(t *testing.T) {
	tests := []struct {
		o    DescentOutcome
		want string
	}{
		{DescentPass, "PASS"},
		{DescentSoftPass, "SOFT-PASS"},
		{DescentFail, "FAIL"},
	}
	for _, tt := range tests {
		if got := tt.o.String(); got != tt.want {
			t.Errorf("DescentOutcome(%d).String() = %q, want %q", tt.o, got, tt.want)
		}
	}
}

func TestDescentTierString(t *testing.T) {
	if s := TierIntentMatch.String(); s != "T1-intent-match" {
		t.Errorf("got %q", s)
	}
	if s := TierSoftPass.String(); s != "T8-soft-pass" {
		t.Errorf("got %q", s)
	}
}

func TestDescentSessionSummaryFormatBanner(t *testing.T) {
	s := DescentSessionSummary{
		Total:    2,
		Passed:   1,
		SoftPass: 1,
		Failed:   0,
		ACIDs:    []string{"AC1", "AC2"},
		Results: []DescentResult{
			{Outcome: DescentPass, ResolvedAtTier: TierRunAC, Reason: "AC passed mechanically"},
			{Outcome: DescentSoftPass, ResolvedAtTier: TierSoftPass, Reason: "env blocks"},
		},
	}
	banner := s.FormatBanner()
	if banner == "" {
		t.Error("expected non-empty banner")
	}
	if !descentContains(banner, "1/2 passed") {
		t.Errorf("banner missing pass count: %s", banner)
	}
	if !descentContains(banner, "1 soft-pass") {
		t.Errorf("banner missing soft-pass count: %s", banner)
	}
}

// descentContains is a local helper to avoid colliding with
// contains() declared in plan_test.go.
func descentContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// Verify the preflight function signature compiles and handles empty input.
func TestPreflightACCommandsEmpty(t *testing.T) {
	broken := PreflightACCommands(context.Background(), t.TempDir(), nil)
	if len(broken) != 0 {
		t.Errorf("expected empty map for nil criteria, got %d entries", len(broken))
	}
}

func TestPreflightACCommandsSkipsGroundTruth(t *testing.T) {
	criteria := []AcceptanceCriterion{
		{ID: "AC1", Description: "build", Command: "pnpm build"},      // ground truth — skip
		{ID: "AC2", Description: "check", Command: "nonexistent_tool"}, // should catch
	}
	broken := PreflightACCommands(context.Background(), t.TempDir(), criteria)
	if _, ok := broken["AC1"]; ok {
		t.Error("ground truth command should be skipped in preflight")
	}
	// AC2 may or may not be flagged depending on whether bash returns 127.
	// The key invariant is AC1 is NOT flagged.
	_ = fmt.Sprintf("broken: %v", broken)
}
