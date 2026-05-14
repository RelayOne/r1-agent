package agents

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/bench"
)

// stubInvoker is a deterministic R1ModelInvoker for tests.
type stubInvoker struct {
	result R1InvocationResult
	err    error
}

func (s *stubInvoker) Invoke(ctx context.Context, m *bench.MissionConfig, wd string, enforce bool) (R1InvocationResult, error) {
	return s.result, s.err
}

func TestR1Dispatcher_Agent_IDReflectsEnforce(t *testing.T) {
	cases := []struct {
		enforce bool
		wantID  string
	}{
		{false, "r1"},
		{true, "r1-antitrunc"},
	}
	for _, tc := range cases {
		d := &R1Dispatcher{EnforceAntiTrunc: tc.enforce}
		got := d.Agent()
		if got.ID != tc.wantID {
			t.Errorf("enforce=%v: Agent().ID = %q, want %q", tc.enforce, got.ID, tc.wantID)
		}
	}
}

func TestR1Dispatcher_RunHelloWorldFakeInvoker(t *testing.T) {
	workDir := t.TempDir()
	mission := &bench.MissionConfig{
		ID:   "hello",
		Plan: []bench.PlanItem{{ID: "P1", Description: "do the thing"}},
	}
	d := &R1Dispatcher{
		EnforceAntiTrunc: true,
		ModelInvoker: &stubInvoker{
			result: R1InvocationResult{
				CompletionAttempted: true,
				LastAssistantText:   "shipped",
				ExitReason:          ExitReasonCompletionClaimed,
				WallClockMs:         42,
				EstimatedBytes:      128,
			},
		},
	}
	tr, err := d.Run(context.Background(), mission, workDir, time.Minute)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !tr.CompletionAttempted {
		t.Errorf("CompletionAttempted = false, want true")
	}
	if tr.LastAssistantText != "shipped" {
		t.Errorf("LastAssistantText = %q, want %q", tr.LastAssistantText, "shipped")
	}
	if tr.ExitReason != ExitReasonCompletionClaimed {
		t.Errorf("ExitReason = %q, want %q", tr.ExitReason, ExitReasonCompletionClaimed)
	}
	if tr.WallClockMs == 0 {
		t.Errorf("WallClockMs is 0; expected non-zero")
	}
}

func TestR1Dispatcher_RunNilModelInvokerReturnsExitOther(t *testing.T) {
	d := &R1Dispatcher{EnforceAntiTrunc: true}
	tr, err := d.Run(context.Background(), &bench.MissionConfig{ID: "x"}, t.TempDir(), time.Minute)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if tr.CompletionAttempted {
		t.Errorf("CompletionAttempted = true, want false (no invoker wired)")
	}
	if tr.ExitReason != ExitReasonOther {
		t.Errorf("ExitReason = %q, want %q", tr.ExitReason, ExitReasonOther)
	}
	if !strings.Contains(tr.RawLog, "no ModelInvoker") {
		t.Errorf("RawLog should explain the not-wired state; got %q", tr.RawLog)
	}
}

func TestR1Dispatcher_RunRespectsTimeout(t *testing.T) {
	d := &R1Dispatcher{
		EnforceAntiTrunc: false,
		ModelInvoker: &stubInvoker{
			result: R1InvocationResult{
				CompletionAttempted: false,
				ExitReason:          ExitReasonTimeout,
			},
		},
	}
	// Stub returns ExitReasonTimeout; just confirm Run propagates it.
	tr, err := d.Run(context.Background(), &bench.MissionConfig{ID: "x"}, t.TempDir(), time.Second)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if tr.ExitReason != ExitReasonTimeout {
		t.Errorf("ExitReason = %q, want %q", tr.ExitReason, ExitReasonTimeout)
	}
}

func TestR1Dispatcher_NilMissionReturnsError(t *testing.T) {
	d := &R1Dispatcher{}
	_, err := d.Run(context.Background(), nil, t.TempDir(), time.Second)
	if err == nil {
		t.Errorf("Run with nil mission should return error")
	}
}

func TestR1Dispatcher_RegistryContainsBothVariants(t *testing.T) {
	for _, id := range []string{"r1", "r1-antitrunc"} {
		t.Run(id, func(t *testing.T) {
			d, ok := Registry[id]
			if !ok {
				t.Fatalf("registry missing %q", id)
			}
			if d.Agent().ID != id {
				t.Errorf("registered dispatcher's Agent().ID = %q, want %q", d.Agent().ID, id)
			}
		})
	}
}
