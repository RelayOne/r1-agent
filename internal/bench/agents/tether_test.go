package agents

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/bench"
)

// fakeInner is a deterministic Dispatcher for Tether unit tests.
type fakeInner struct {
	id    string
	trace Trace
	err   error
}

func (f *fakeInner) Agent() Agent {
	return Agent{ID: f.id, DisplayName: f.id, Version: "test"}
}
func (f *fakeInner) Run(_ context.Context, _ *bench.MissionConfig, _ string, _ time.Duration) (Trace, error) {
	return f.trace, f.err
}

func TestTetherDispatcher_AgentReflectsInnerID(t *testing.T) {
	inner := &fakeInner{id: "cline"}
	d := &TetherDispatcher{Inner: inner}
	got := d.Agent()
	if got.ID != "tether+cline" {
		t.Errorf("Agent().ID = %q, want tether+cline", got.ID)
	}
}

func TestTetherDispatcher_NilInnerReportsUnwired(t *testing.T) {
	d := &TetherDispatcher{}
	got := d.Agent()
	if got.ID != "tether(unwired)" {
		t.Errorf("Agent().ID = %q, want tether(unwired)", got.ID)
	}
}

func TestTetherDispatcher_SilentInnerStaysSilent(t *testing.T) {
	inner := &fakeInner{
		id:    "fake",
		trace: Trace{CompletionAttempted: false, LastAssistantText: "I got tired"},
	}
	d := &TetherDispatcher{Inner: inner}
	mission := &bench.MissionConfig{ID: "x", Plan: []bench.PlanItem{{ID: "P1", Description: "do thing"}}}
	tr, err := d.Run(context.Background(), mission, t.TempDir(), time.Second)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if tr.CompletionAttempted {
		t.Errorf("CompletionAttempted = true, want false (Tether should not invent completion)")
	}
	if tr.ExitReason == ExitReasonTetherRefused {
		t.Errorf("ExitReason = %q, Tether should not refuse silent runs", tr.ExitReason)
	}
}

func TestTetherDispatcher_CleanInnerPassesThrough(t *testing.T) {
	innerTrace := Trace{
		CompletionAttempted: true,
		LastAssistantText:   "shipped everything",
		UnifiedDiff:         "diff --git a/handler.go b/handler.go\n+func F() {}\n",
		ExitReason:          ExitReasonCompletionClaimed,
	}
	inner := &fakeInner{id: "fake", trace: innerTrace}
	d := &TetherDispatcher{Inner: inner}
	mission := &bench.MissionConfig{
		ID: "x",
		Plan: []bench.PlanItem{
			{ID: "P1", Description: "add F", RequiredSymbols: []string{"func F"}, ChangedFiles: []string{"handler.go"}},
		},
	}
	tr, err := d.Run(context.Background(), mission, t.TempDir(), time.Second)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if tr.ExitReason != ExitReasonCompletionClaimed {
		t.Errorf("ExitReason = %q, want %q (no truncation, plan touched)", tr.ExitReason, ExitReasonCompletionClaimed)
	}
	if strings.Contains(tr.LastAssistantText, "[tether]") {
		t.Errorf("clean trace got tether annotation: %q", tr.LastAssistantText)
	}
}

func TestTetherDispatcher_UnreachedPlanItemRefuses(t *testing.T) {
	innerTrace := Trace{
		CompletionAttempted: true,
		LastAssistantText:   "all done",
		UnifiedDiff:         "diff --git a/handler.go b/handler.go\n+func F() {}\n",
		ExitReason:          ExitReasonCompletionClaimed,
	}
	inner := &fakeInner{id: "fake", trace: innerTrace}
	d := &TetherDispatcher{Inner: inner}
	mission := &bench.MissionConfig{
		ID: "x",
		Plan: []bench.PlanItem{
			{ID: "P1", Description: "add F", RequiredSymbols: []string{"func F"}, ChangedFiles: []string{"handler.go"}},
			{ID: "P2", Description: "add G", RequiredSymbols: []string{"func G"}, ChangedFiles: []string{"other.go"}},
		},
	}
	tr, err := d.Run(context.Background(), mission, t.TempDir(), time.Second)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if tr.ExitReason != ExitReasonTetherRefused {
		t.Errorf("ExitReason = %q, want %q (P2 untouched)", tr.ExitReason, ExitReasonTetherRefused)
	}
	if !strings.Contains(tr.LastAssistantText, "[tether]") {
		t.Errorf("tether annotation missing from LastAssistantText: %q", tr.LastAssistantText)
	}
	if !strings.Contains(tr.LastAssistantText, "1/2 plan items unchecked") {
		t.Errorf("expected '1/2 plan items unchecked' annotation; got %q", tr.LastAssistantText)
	}
}

func TestTetherDispatcher_InnerErrorPropagates(t *testing.T) {
	wantErr := errors.New("inner failed")
	inner := &fakeInner{id: "fake", err: wantErr}
	d := &TetherDispatcher{Inner: inner}
	_, err := d.Run(context.Background(), &bench.MissionConfig{ID: "x"}, t.TempDir(), time.Second)
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
}

func TestTetherDispatcher_NilMissionErrors(t *testing.T) {
	d := &TetherDispatcher{Inner: &fakeInner{id: "fake"}}
	_, err := d.Run(context.Background(), nil, t.TempDir(), time.Second)
	if err == nil {
		t.Errorf("nil mission should error")
	}
}

func TestEvaluateTether_TruncationPhraseRefuses(t *testing.T) {
	// "ready to merge" is in the phrases catalog (premature_stop family).
	trace := Trace{
		CompletionAttempted: true,
		LastAssistantText:   "Core functionality is ready to merge.",
		UnifiedDiff:         "+func X() {}",
	}
	plan := []bench.PlanItem{{ID: "P1", Description: "add X", RequiredSymbols: []string{"func X"}}}
	d := evaluateTether(trace, plan)
	if !d.Refused {
		t.Errorf("evaluateTether did not refuse on truncation phrase; reasons=%v", d.Reasons)
	}
}

func TestTetherDispatcher_RegistryContainsCanonicalCombos(t *testing.T) {
	for _, id := range []string{"tether+aider", "tether+cline", "tether+cursor", "tether+codex-cli", "tether+claude-code-default"} {
		t.Run(id, func(t *testing.T) {
			if _, ok := Registry[id]; !ok {
				t.Errorf("registry missing %q", id)
			}
		})
	}
}
