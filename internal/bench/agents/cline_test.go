package agents

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/bench"
)

func TestClineDispatcher_Agent(t *testing.T) {
	d := &ClineDispatcher{}
	if d.Agent().ID != "cline" {
		t.Errorf("ID = %q, want cline", d.Agent().ID)
	}
}

func TestParseClineStream_AttemptCompletionMarksDone(t *testing.T) {
	stream := strings.NewReader(`{"event":"say","text":"reading repo"}
{"event":"say","text":"editing file"}
{"event":"attempt_completion","result":"all changes shipped"}
`)
	tr := parseClineStream(stream)
	if !tr.CompletionAttempted {
		t.Errorf("CompletionAttempted = false, want true")
	}
	if tr.LastAssistantText != "all changes shipped" {
		t.Errorf("LastAssistantText = %q, want %q", tr.LastAssistantText, "all changes shipped")
	}
	if tr.ExitReason != ExitReasonCompletionClaimed {
		t.Errorf("ExitReason = %q, want %q", tr.ExitReason, ExitReasonCompletionClaimed)
	}
}

func TestParseClineStream_NoAttemptCompletionMeansSilentFail(t *testing.T) {
	stream := strings.NewReader(`{"event":"say","text":"thinking"}
{"event":"say","text":"more thinking"}
`)
	tr := parseClineStream(stream)
	if tr.CompletionAttempted {
		t.Errorf("CompletionAttempted = true, want false (no attempt_completion event)")
	}
}

func TestParseClineStream_RateLimitDetected(t *testing.T) {
	stream := strings.NewReader(`{"event":"error","message":"rate limit reached"}`)
	tr := parseClineStream(stream)
	if tr.ExitReason != ExitReasonRateLimit {
		t.Errorf("ExitReason = %q, want %q", tr.ExitReason, ExitReasonRateLimit)
	}
}

func TestClineDispatcher_MissingBinaryReturnsNotSupported(t *testing.T) {
	d := &ClineDispatcher{BinaryPath: "/nonexistent/cline-bin-xyz"}
	tr, err := d.Run(context.Background(), &bench.MissionConfig{ID: "x", Intent: "noop"}, t.TempDir(), time.Second)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if tr.ExitReason != ExitReasonNotSupported {
		t.Errorf("ExitReason = %q, want %q", tr.ExitReason, ExitReasonNotSupported)
	}
}

func TestClineDispatcher_RegisteredInRegistry(t *testing.T) {
	if _, ok := Registry["cline"]; !ok {
		t.Errorf("registry missing cline")
	}
}
