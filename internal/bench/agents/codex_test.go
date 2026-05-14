package agents

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/bench"
)

func TestCodexDispatcher_Agent(t *testing.T) {
	d := &CodexDispatcher{}
	if d.Agent().ID != "codex-cli" {
		t.Errorf("ID = %q, want codex-cli", d.Agent().ID)
	}
}

func TestParseCodexStream_TaskCompleteMarksCompletion(t *testing.T) {
	stream := strings.NewReader(`{"type":"assistant_message","content":"reading code"}
{"type":"assistant_message","content":"final answer here"}
{"type":"task_complete"}
`)
	tr := parseCodexStream(stream)
	if !tr.CompletionAttempted {
		t.Errorf("CompletionAttempted = false, want true")
	}
	if tr.LastAssistantText != "final answer here" {
		t.Errorf("LastAssistantText = %q", tr.LastAssistantText)
	}
	if tr.ExitReason != ExitReasonCompletionClaimed {
		t.Errorf("ExitReason = %q", tr.ExitReason)
	}
}

func TestParseCodexStream_RateLimitedDetected(t *testing.T) {
	stream := strings.NewReader(`{"type":"rate_limited","reason":"daily limit"}`)
	tr := parseCodexStream(stream)
	if tr.ExitReason != ExitReasonRateLimit {
		t.Errorf("ExitReason = %q, want %q", tr.ExitReason, ExitReasonRateLimit)
	}
	if tr.CompletionAttempted {
		t.Errorf("CompletionAttempted = true, want false")
	}
}

func TestParseCodexStream_TaskFailedIsToolError(t *testing.T) {
	stream := strings.NewReader(`{"type":"task_failed","message":"sandbox died"}`)
	tr := parseCodexStream(stream)
	if tr.ExitReason != ExitReasonToolError {
		t.Errorf("ExitReason = %q, want %q", tr.ExitReason, ExitReasonToolError)
	}
}

func TestCodexDispatcher_MissingBinaryReturnsNotSupported(t *testing.T) {
	d := &CodexDispatcher{BinaryPath: "/nonexistent/codex-bin-xyz"}
	tr, err := d.Run(context.Background(), &bench.MissionConfig{ID: "x", Intent: "noop"}, t.TempDir(), time.Second)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if tr.ExitReason != ExitReasonNotSupported {
		t.Errorf("ExitReason = %q, want %q", tr.ExitReason, ExitReasonNotSupported)
	}
}

func TestCodexDispatcher_RegisteredInRegistry(t *testing.T) {
	if _, ok := Registry["codex-cli"]; !ok {
		t.Errorf("registry missing codex-cli")
	}
}
