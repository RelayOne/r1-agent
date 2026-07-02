package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/hitl"
	"github.com/RelayOne/r1/internal/plan"
	"github.com/RelayOne/r1/internal/streamjson"
)

// Audit A032 wiring tests: the HITL approval service must be an actual
// consumer-visible gate. buildDescentConfig installs SoftPassApprovalFunc
// only for GovernanceTier=enterprise with a non-nil HITL service, and the
// installed callback must round-trip through hitl.RequestApproval.

// TestBuildSoftPassApprovalFuncGating locks the (svc, tier) truth table:
// non-nil only when both an HITL service exists and the tier is
// enterprise (case-insensitive).
func TestBuildSoftPassApprovalFuncGating(t *testing.T) {
	svc := hitl.New(nil, strings.NewReader(""), time.Second)

	cases := []struct {
		name    string
		svc     *hitl.Service
		tier    string
		wantNil bool
	}{
		{"nil service, enterprise", nil, "enterprise", true},
		{"service, community", svc, "community", true},
		{"service, empty tier", svc, "", true},
		{"service, enterprise", svc, "enterprise", false},
		{"service, Enterprise (case-insensitive)", svc, "Enterprise", false},
	}
	for _, tc := range cases {
		got := buildSoftPassApprovalFunc(tc.svc, tc.tier)
		if (got == nil) != tc.wantNil {
			t.Errorf("%s: buildSoftPassApprovalFunc nil=%v, want nil=%v", tc.name, got == nil, tc.wantNil)
		}
	}
}

// TestBuildDescentConfigSoftPassWiring asserts the full config seam:
// sowNativeConfig{HITL, GovernanceTier} -> buildDescentConfig ->
// dc.SoftPassApprovalFunc set only on the enterprise tier.
func TestBuildDescentConfigSoftPassWiring(t *testing.T) {
	svc := hitl.New(nil, strings.NewReader(""), time.Second)
	sowDoc := &plan.SOW{}
	session := plan.Session{ID: "S1"}

	build := func(cfg sowNativeConfig) plan.DescentConfig {
		return buildDescentConfig(context.Background(), sowDoc, session, session, cfg, t.TempDir(), 10, nil)
	}

	if dc := build(sowNativeConfig{HITL: svc, GovernanceTier: "enterprise"}); dc.SoftPassApprovalFunc == nil {
		t.Error("enterprise + HITL: SoftPassApprovalFunc = nil, want non-nil")
	}
	if dc := build(sowNativeConfig{HITL: svc, GovernanceTier: "community"}); dc.SoftPassApprovalFunc != nil {
		t.Error("community + HITL: SoftPassApprovalFunc non-nil, want nil (auto-grant)")
	}
	if dc := build(sowNativeConfig{HITL: svc}); dc.SoftPassApprovalFunc != nil {
		t.Error("empty tier + HITL: SoftPassApprovalFunc non-nil, want nil (auto-grant)")
	}
	if dc := build(sowNativeConfig{GovernanceTier: "enterprise"}); dc.SoftPassApprovalFunc != nil {
		t.Error("enterprise + nil HITL: SoftPassApprovalFunc non-nil, want nil")
	}
}

// TestSoftPassApprovalRoundtrip drives the installed callback through a
// real hitl.Service with a stdin pipe: the hitl_required line must land
// on the stream and the operator's decision must become the return value.
func TestSoftPassApprovalRoundtrip(t *testing.T) {
	for _, tc := range []struct {
		name     string
		decision string
		want     bool
	}{
		{"approve", `{"decision":true,"reason":"LGTM","decided_by":"op@example.com"}`, true},
		{"reject", `{"decision":false,"reason":"not convinced","decided_by":"op@example.com"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, w := io.Pipe()
			var out bytes.Buffer
			emitter := streamjson.NewTwoLane(&out, true)
			svc := hitl.New(emitter, r, 2*time.Second)

			fn := buildSoftPassApprovalFunc(svc, "enterprise")
			if fn == nil {
				t.Fatal("expected non-nil approval func")
			}

			done := make(chan bool, 1)
			go func() {
				done <- fn(context.Background(),
					plan.AcceptanceCriterion{ID: "AC-1", Description: "server responds"},
					plan.ReasoningVerdict{Category: "acceptable_as_is", Reasoning: "flaky AC"})
			}()

			time.Sleep(50 * time.Millisecond)
			if _, err := w.Write([]byte(tc.decision + "\n")); err != nil {
				t.Fatalf("write decision: %v", err)
			}
			var got bool
			select {
			case got = <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("approval func did not return")
			}
			emitter.Drain(time.Second)

			if got != tc.want {
				t.Errorf("approval = %v, want %v", got, tc.want)
			}
			if !strings.Contains(out.String(), `"type":"hitl_required"`) {
				t.Errorf("expected hitl_required on stream: %q", out.String())
			}
			if !strings.Contains(out.String(), `"reason":"soft-pass approval: AC-1"`) {
				t.Errorf("expected soft-pass reason on stream: %q", out.String())
			}
		})
	}
}

// TestSoftPassApprovalTimeoutRejects asserts the wait-ceiling contract:
// no stdin answer within the ceiling means rejection (descent FAIL).
func TestSoftPassApprovalTimeoutRejects(t *testing.T) {
	r, _ := io.Pipe() // never written
	svc := hitl.New(nil, r, 50*time.Millisecond)
	fn := buildSoftPassApprovalFunc(svc, "enterprise")
	if fn == nil {
		t.Fatal("expected non-nil approval func")
	}
	if fn(context.Background(), plan.AcceptanceCriterion{ID: "AC-1"}, plan.ReasoningVerdict{}) {
		t.Error("timed-out approval returned true, want false (rejection)")
	}
}

// TestHITLWaitCeiling locks the spec-2 item 12 tier defaults shared by
// sowCmd: override wins, enterprise=15m, community/empty=1h.
func TestHITLWaitCeiling(t *testing.T) {
	if got := hitlWaitCeiling("enterprise", 0); got != 15*time.Minute {
		t.Errorf("enterprise default = %v, want 15m", got)
	}
	if got := hitlWaitCeiling("Enterprise", 0); got != 15*time.Minute {
		t.Errorf("Enterprise (case-insensitive) default = %v, want 15m", got)
	}
	if got := hitlWaitCeiling("community", 0); got != time.Hour {
		t.Errorf("community default = %v, want 1h", got)
	}
	if got := hitlWaitCeiling("", 0); got != time.Hour {
		t.Errorf("empty tier default = %v, want 1h", got)
	}
	if got := hitlWaitCeiling("enterprise", 5*time.Minute); got != 5*time.Minute {
		t.Errorf("override should win: got %v, want 5m", got)
	}
}
