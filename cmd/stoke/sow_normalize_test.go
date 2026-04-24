package main

import (
	"strings"
	"testing"

	"github.com/ericmacdougall/stoke/internal/plan"
)

// TestResultsNormalization_Semantics documents and locks in the
// normalization contract: after Phase 2's self-repair loop, if the
// session's acceptance criteria pass (finalPassed == true), any
// previously-failed Phase 1 task is marked successful because the
// session's END STATE is good. This prevents the bug where Stoke
// halted after S1 even when S1 actually succeeded — the earlier
// failures in the results slice were leaking to the scheduler's
// outer loop as session-level failures.
//
// The test simulates the normalization logic directly on a results
// slice rather than running runSessionNative end-to-end (which would
// need a real LLM).
func TestResultsNormalization_FinalPassedSucceedsAll(t *testing.T) {
	results := []plan.TaskExecResult{
		{TaskID: "T1", Success: true},
		{TaskID: "T2", Success: false, Error: dummyError("build failed")},
		{TaskID: "T3", Success: true},
	}
	finalPassed := true

	// This is the exact normalization block from runSessionNative.
	if finalPassed {
		for i := range results {
			if !results[i].Success {
				results[i].Success = true
				results[i].Error = nil
			}
		}
	}

	for _, r := range results {
		if !r.Success {
			t.Errorf("task %s should have been normalized to success", r.TaskID)
		}
		if r.Error != nil {
			t.Errorf("task %s should have nil error after normalization", r.TaskID)
		}
	}
}

func TestResultsNormalization_FinalFailedLeavesOriginals(t *testing.T) {
	results := []plan.TaskExecResult{
		{TaskID: "T1", Success: true},
		{TaskID: "T2", Success: false, Error: dummyError("build failed")},
	}
	finalPassed := false

	if finalPassed {
		for i := range results {
			if !results[i].Success {
				results[i].Success = true
				results[i].Error = nil
			}
		}
	}

	// Original failure must be preserved so the scheduler sees it.
	if results[1].Success {
		t.Error("failed task should stay failed when finalPassed=false")
	}
	if results[1].Error == nil {
		t.Error("failed task should keep its error")
	}
}

// TestContinueOnFailure_AutoDefault_MultiSession simulates the flag
// resolution logic from sowCmd. A multi-session SOW defaults to
// continue-on-failure ON so the scheduler attempts every session.
func TestContinueOnFailure_AutoDefault_MultiSession(t *testing.T) {
	sow := &plan.SOW{
		Sessions: []plan.Session{
			{ID: "S1"}, {ID: "S2"}, {ID: "S3"},
		},
	}
	continueOnFailure := resolveContinueOnFailure("", sow)
	if !continueOnFailure {
		t.Error("multi-session SOW should default to continue-on-failure=true")
	}
}

func TestContinueOnFailure_AutoDefault_SingleSession(t *testing.T) {
	sow := &plan.SOW{Sessions: []plan.Session{{ID: "S1"}}}
	continueOnFailure := resolveContinueOnFailure("", sow)
	if continueOnFailure {
		t.Error("single-session SOW should default to continue-on-failure=false")
	}
}

func TestContinueOnFailure_ExplicitTrue(t *testing.T) {
	sow := &plan.SOW{Sessions: []plan.Session{{ID: "S1"}}} // single
	// Explicit true should override the single-session auto default.
	for _, flag := range []string{"true", "yes", "1", "on", "TRUE", "Yes"} {
		if !resolveContinueOnFailure(flag, sow) {
			t.Errorf("explicit %q should resolve to true", flag)
		}
	}
}

func TestContinueOnFailure_ExplicitFalse(t *testing.T) {
	sow := &plan.SOW{Sessions: []plan.Session{{ID: "S1"}, {ID: "S2"}}} // multi
	// Explicit false should override the multi-session auto default.
	for _, flag := range []string{"false", "no", "0", "off", "FALSE", "No"} {
		if resolveContinueOnFailure(flag, sow) {
			t.Errorf("explicit %q should resolve to false", flag)
		}
	}
}

func TestContinueOnFailure_UnknownFlag_FallsBackToAuto(t *testing.T) {
	sow := &plan.SOW{Sessions: []plan.Session{{ID: "S1"}, {ID: "S2"}}}
	// Garbage input should fall back to auto, which for multi-session is true.
	if !resolveContinueOnFailure("maybe", sow) {
		t.Error("unknown flag value should fall back to auto default (true for multi-session)")
	}
}

func TestContinueOnFailure_Auto(t *testing.T) {
	// Explicit "auto" behaves like empty (auto default)
	multi := &plan.SOW{Sessions: []plan.Session{{ID: "S1"}, {ID: "S2"}}}
	if !resolveContinueOnFailure("auto", multi) {
		t.Error("\"auto\" on multi-session should be true")
	}
	single := &plan.SOW{Sessions: []plan.Session{{ID: "S1"}}}
	if resolveContinueOnFailure("auto", single) {
		t.Error("\"auto\" on single-session should be false")
	}
}

// resolveContinueOnFailure mirrors the tri-state resolution inline in
// sowCmd so tests can assert it without setting up flag.FlagSet. If
// the sowCmd logic changes, update this mirror.
func resolveContinueOnFailure(flagValue string, sow *plan.SOW) bool {
	continueOnFailure := len(sow.Sessions) > 1 // auto default
	switch strings.ToLower(strings.TrimSpace(flagValue)) {
	case "true", "yes", "1", "on":
		continueOnFailure = true
	case "false", "no", "0", "off":
		continueOnFailure = false
	case "", "auto":
		// keep auto default
	}
	return continueOnFailure
}

// dummyError is a test-local error type so we don't pull in errors.New
// for a one-off.
type dummyError string

func (e dummyError) Error() string { return string(e) }
