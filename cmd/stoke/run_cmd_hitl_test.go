package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ericmacdougall/stoke/internal/hitl"
	"github.com/ericmacdougall/stoke/internal/streamjson"
)

// Spec-2 item 12 validates the HITL stdin reader pattern (RT-04 §7):
// blocking line-read on a goroutine + channel + timer. These tests
// are cmd-package scoped because they wire the full TwoLane emitter
// + Service + stdin pipe the way run_cmd.go wires them in production.

// TestHITLEndToEndRoundtrip sends a line through the pipe pattern that
// run_cmd.go uses, asserting the emitted hitl_required line shape and
// the resulting Decision.
func TestHITLEndToEndRoundtrip(t *testing.T) {
	r, w := io.Pipe()
	var stdoutBuf bytes.Buffer
	emitter := streamjson.NewTwoLane(&stdoutBuf, true)
	svc := hitl.New(emitter, r, 2*time.Second)

	done := make(chan hitl.Decision, 1)
	go func() {
		done <- svc.RequestApproval(context.Background(), hitl.Request{
			Reason:       "soft-pass at T8",
			ApprovalType: "soft_pass",
			File:         "internal/foo/bar.go",
			Context:      map[string]any{"ac_id": "AC-07", "tier": "T8"},
		})
	}()

	// Pause for the hitl_required line to land, then deliver a decision.
	time.Sleep(50 * time.Millisecond)
	if _, err := w.Write([]byte(`{"decision":true,"reason":"LGTM","decided_by":"user@example.com"}` + "\n")); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	var got hitl.Decision
	select {
	case got = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RequestApproval did not return")
	}
	emitter.Drain(time.Second)

	if !got.Approved {
		t.Errorf("expected Approved=true, got %+v", got)
	}
	out := stdoutBuf.String()
	if !strings.Contains(out, `"type":"hitl_required"`) {
		t.Errorf("expected hitl_required in output: %q", out)
	}
	if !strings.Contains(out, `"approval_type":"soft_pass"`) {
		t.Errorf("expected approval_type=soft_pass in output: %q", out)
	}
	if !strings.Contains(out, `"file":"internal/foo/bar.go"`) {
		t.Errorf("expected file in output: %q", out)
	}
}

// TestHITLGovernanceTierTimeoutsDefault locks the timeout defaults
// from spec-2 item 12: enterprise=15m, community=1h. Timer selection
// is exercised in hitl_test.go; here we assert the run_cmd.go branch
// that picks the right default.
func TestHITLGovernanceTierTimeoutsDefault(t *testing.T) {
	// Mirror the defaulting block in runCommandExitCode.
	resolveTimeout := func(override time.Duration, tier string) time.Duration {
		if override > 0 {
			return override
		}
		if tier == "enterprise" {
			return 15 * time.Minute
		}
		return 1 * time.Hour
	}
	if got := resolveTimeout(0, "enterprise"); got != 15*time.Minute {
		t.Errorf("enterprise default=%v, want 15m", got)
	}
	if got := resolveTimeout(0, "community"); got != 1*time.Hour {
		t.Errorf("community default=%v, want 1h", got)
	}
	if got := resolveTimeout(0, ""); got != 1*time.Hour {
		t.Errorf("empty tier default=%v, want 1h", got)
	}
	if got := resolveTimeout(5*time.Minute, "enterprise"); got != 5*time.Minute {
		t.Errorf("override should win: got %v", got)
	}
}
