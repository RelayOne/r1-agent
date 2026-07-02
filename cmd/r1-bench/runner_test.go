package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/bench"
	"github.com/RelayOne/r1/internal/bench/agents"
)

// fakeDispatcher is a deterministic Dispatcher for RunOne tests.
type fakeDispatcher struct {
	id    string
	trace agents.Trace
	err   error
}

func (f *fakeDispatcher) Agent() agents.Agent {
	return agents.Agent{ID: f.id, DisplayName: f.id}
}
func (f *fakeDispatcher) Run(_ context.Context, _ *bench.MissionConfig, _ string, _ time.Duration) (agents.Trace, error) {
	return f.trace, f.err
}

func TestRunOne_HappyPath_TruthfulCompletion(t *testing.T) {
	mission := &bench.MissionConfig{
		ID:    "x",
		Title: "x",
		Plan: []bench.PlanItem{
			{ID: "P1", Description: "add F", RequiredSymbols: []string{"func F"}},
		},
		CompletionCriteria: bench.CompletionCriteria{
			PlanCompletionThreshold: 1.0,
			DeliveryRatioMin:        0,
		},
	}
	d := &fakeDispatcher{
		id: "fake",
		trace: agents.Trace{
			CompletionAttempted: true,
			LastAssistantText:   "all done",
			UnifiedDiff:         "diff --git a/f.go b/f.go\n+func F() {}\n",
			WallClockMs:         42,
			ExitReason:          agents.ExitReasonCompletionClaimed,
		},
	}
	result, err := RunOne(context.Background(), RunSpec{
		Mission:    mission,
		Dispatcher: d,
		WorkDir:    t.TempDir(),
		Timeout:    time.Second,
	})
	if err != nil {
		t.Fatalf("RunOne: %v", err)
	}
	if result.MissionID != "x" {
		t.Errorf("MissionID = %q, want x", result.MissionID)
	}
	if !result.CompletionAttempted {
		t.Errorf("CompletionAttempted = false, want true")
	}
	if !result.CompletionTruthful {
		t.Errorf("CompletionTruthful = false, want true (plan items satisfied)")
	}
	if result.WallTimeMs != 42 {
		t.Errorf("WallTimeMs = %d, want 42", result.WallTimeMs)
	}
}

func TestRunOne_SilentFailureNotTruthful(t *testing.T) {
	mission := &bench.MissionConfig{
		ID:   "x",
		Plan: []bench.PlanItem{{ID: "P1", Description: "do thing"}},
	}
	d := &fakeDispatcher{
		id: "fake",
		trace: agents.Trace{
			CompletionAttempted: false,
			LastAssistantText:   "I gave up",
			ExitReason:          agents.ExitReasonOther,
		},
	}
	result, err := RunOne(context.Background(), RunSpec{
		Mission:    mission,
		Dispatcher: d,
		WorkDir:    t.TempDir(),
		Timeout:    time.Second,
	})
	if err != nil {
		t.Fatalf("RunOne: %v", err)
	}
	if result.CompletionTruthful {
		t.Errorf("CompletionTruthful = true, want false (silent failure)")
	}
	if !result.CompletionSilentlyFailed {
		t.Errorf("CompletionSilentlyFailed = false, want true")
	}
}

func TestRunOne_NilMissionErrors(t *testing.T) {
	_, err := RunOne(context.Background(), RunSpec{
		Dispatcher: &fakeDispatcher{id: "fake"},
		WorkDir:    t.TempDir(),
	})
	if err == nil {
		t.Errorf("nil mission should error")
	}
}

func TestRunOne_NilDispatcherErrors(t *testing.T) {
	_, err := RunOne(context.Background(), RunSpec{
		Mission: &bench.MissionConfig{ID: "x"},
		WorkDir: t.TempDir(),
	})
	if err == nil {
		t.Errorf("nil dispatcher should error")
	}
}

func TestRunOne_EmptyWorkDirErrors(t *testing.T) {
	_, err := RunOne(context.Background(), RunSpec{
		Mission:    &bench.MissionConfig{ID: "x"},
		Dispatcher: &fakeDispatcher{id: "fake"},
	})
	if err == nil {
		t.Errorf("empty WorkDir should error")
	}
}

func TestDefaultExecCommand_Exit0OnSuccess(t *testing.T) {
	code, err := defaultExecCommand(context.Background(), t.TempDir(), "true")
	if err != nil {
		t.Fatalf("defaultExecCommand: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}

func TestDefaultExecCommand_PropagatesNonZero(t *testing.T) {
	code, err := defaultExecCommand(context.Background(), t.TempDir(), "false")
	if err != nil {
		t.Fatalf("defaultExecCommand: %v", err)
	}
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
}

func TestDefaultExecCommand_EmptyCmdErrors(t *testing.T) {
	_, err := defaultExecCommand(context.Background(), t.TempDir(), "   ")
	if err == nil {
		t.Errorf("empty command should error")
	}
}

// helper used elsewhere — keep symbol visible
var _ = bytes.NewBuffer
var _ = strings.TrimSpace

// TestRunOne_SurfacesRewardHackFlags covers SOTA gap #5 (runner half):
// a trajectory that reads the reference solution from git history is
// flagged on the RunResult so a gamed score is visible.
func TestRunOne_SurfacesRewardHackFlags(t *testing.T) {
	mission := &bench.MissionConfig{
		ID:    "rh",
		Title: "rh",
		Plan:  []bench.PlanItem{{ID: "P1", Description: "add F", RequiredSymbols: []string{"func F"}}},
	}
	d := &fakeDispatcher{
		id: "fake",
		trace: agents.Trace{
			CompletionAttempted: true,
			LastAssistantText:   "done",
			UnifiedDiff:         "diff --git a/f.go b/f.go\n+++ b/f.go\n+func F() {}\n",
			RawLog:              "tool=bash git log --all -p\ntool=bash go build ./...\n",
		},
	}
	res, err := RunOne(context.Background(), RunSpec{
		Mission: mission, Dispatcher: d, WorkDir: t.TempDir(), Timeout: time.Minute,
	})
	if err != nil {
		t.Fatalf("RunOne: %v", err)
	}
	if len(res.RewardHackFlags) == 0 {
		t.Fatal("git-history read in the trajectory was not flagged on the result")
	}
	joined := strings.Join(res.RewardHackFlags, " ")
	if !strings.Contains(joined, "git_history_read") {
		t.Errorf("expected a git_history_read flag, got %v", res.RewardHackFlags)
	}
}
