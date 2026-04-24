package main

import (
	"testing"
)

// TestCompleteSubtypeMapping locks the exit-code to subtype mapping
// so CloudSwarm's fixture parser can pattern-match by subtype without
// re-deriving it from the exit_code field. Spec-2 item 5 (exit code
// contract) and item 7 (subtype stability).
func TestCompleteSubtypeMapping(t *testing.T) {
	cases := map[int]string{
		ExitPass:          "success",
		ExitACFailed:      "error_ac_failed",
		ExitBudgetOrUsage: "error_budget_or_usage",
		ExitOperatorAbort: "error_operator_abort",
		ExitSIGINT:        "error_sigint",
		ExitSIGTERM:       "error_sigterm",
		99:                "error_unknown",
	}
	for code, want := range cases {
		if got := completeSubtype(code); got != want {
			t.Errorf("completeSubtype(%d)=%q, want %q", code, got, want)
		}
	}
}

// TestExitCodeConstantsMatchContract locks the numeric values of the
// exit-code contract (D11 per spec-2 item 7). CloudSwarm's Temporal
// activity embeds these values directly; drift here is a breaking
// contract change.
func TestExitCodeConstantsMatchContract(t *testing.T) {
	cases := map[int]int{
		0:   ExitPass,
		1:   ExitACFailed,
		2:   ExitBudgetOrUsage,
		3:   ExitOperatorAbort,
		130: ExitSIGINT,
		143: ExitSIGTERM,
	}
	for want, got := range cases {
		if got != want {
			t.Errorf("exit code constant=%d, contract requires %d", got, want)
		}
	}
}

// TestRunCommandUsageReturnsCode2 verifies that invoking the
// CloudSwarm entry with neither --sow nor TASK_SPEC yields exit
// code 2 (usage).
func TestRunCommandUsageReturnsCode2(t *testing.T) {
	code := runCommandExitCode([]string{"--output", "stream-json"})
	if code != ExitBudgetOrUsage {
		t.Errorf("runCommandExitCode(no work)=%d, want %d", code, ExitBudgetOrUsage)
	}
}

// TestRunCommandFreeTextQueryReturnsCode0 verifies the free-text
// query path closes out with exit 0 when chat.ClassifyIntent returns
// IntentQuery (the common case for "build a server", "explain foo").
func TestRunCommandFreeTextQueryReturnsCode0(t *testing.T) {
	code := runCommandExitCode([]string{"--output", "stream-json", "build", "a", "server"})
	if code != ExitPass {
		t.Errorf("free-text query exit=%d, want %d", code, ExitPass)
	}
}

// TestRunCommandBudgetExhausted verifies the STOKE_BUDGET_EXHAUSTED
// entry guard fires exit code 2 before any dispatch. Spec-2 item 7.
func TestRunCommandBudgetExhausted(t *testing.T) {
	t.Setenv("R1_BUDGET_EXHAUSTED", "1")
	code := runCommandExitCode([]string{"--output", "stream-json", "--sow", "does-not-matter.md"})
	if code != ExitBudgetOrUsage {
		t.Errorf("budget-exhausted exit=%d, want %d", code, ExitBudgetOrUsage)
	}
}

// TestRunCommandSOWMissingReturnsCode2 verifies that --sow pointing
// to a non-existent file returns exit 2 (usage error).
func TestRunCommandSOWMissingReturnsCode2(t *testing.T) {
	code := runCommandExitCode([]string{
		"--output", "stream-json",
		"--sow", "/nonexistent/path/does-not-exist.md",
	})
	if code != ExitBudgetOrUsage {
		t.Errorf("sow-missing exit=%d, want %d", code, ExitBudgetOrUsage)
	}
}
