package agents

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/bench"
)

func TestClaudeCodeDispatcher_Agent(t *testing.T) {
	d := &ClaudeCodeDispatcher{}
	a := d.Agent()
	if a.ID != "claude-code-default" {
		t.Errorf("ID = %q, want claude-code-default", a.ID)
	}
	if a.DisplayName == "" {
		t.Errorf("DisplayName empty")
	}
}

func TestParseClaudeCodeStream_StopEventMarksCompletion(t *testing.T) {
	stream := strings.NewReader(`{"event":"assistant_message","content":"working on it"}
{"event":"assistant_message","content":"all done"}
{"event":"stop","stop_hook_active":false}
`)
	tr := parseClaudeCodeStream(stream)
	if !tr.CompletionAttempted {
		t.Errorf("CompletionAttempted = false, want true (stop with hook inactive)")
	}
	if tr.LastAssistantText != "all done" {
		t.Errorf("LastAssistantText = %q, want %q", tr.LastAssistantText, "all done")
	}
	if tr.ExitReason != ExitReasonCompletionClaimed {
		t.Errorf("ExitReason = %q, want %q", tr.ExitReason, ExitReasonCompletionClaimed)
	}
}

func TestParseClaudeCodeStream_StopHookActiveApproveCompletes(t *testing.T) {
	stream := strings.NewReader(`{"event":"assistant_message","content":"done"}
{"event":"stop","stop_hook_active":true,"decision":"approve"}
`)
	tr := parseClaudeCodeStream(stream)
	if !tr.CompletionAttempted {
		t.Errorf("CompletionAttempted = false, want true (hook approved)")
	}
}

func TestParseClaudeCodeStream_RateLimitErrorCategorized(t *testing.T) {
	stream := strings.NewReader(`{"event":"assistant_message","content":"trying"}
{"event":"error","message":"rate_limit_exceeded: try again in 60s"}
`)
	tr := parseClaudeCodeStream(stream)
	if tr.ExitReason != ExitReasonRateLimit {
		t.Errorf("ExitReason = %q, want %q", tr.ExitReason, ExitReasonRateLimit)
	}
	if tr.CompletionAttempted {
		t.Errorf("CompletionAttempted = true, want false (rate-limited)")
	}
}

func TestParseClaudeCodeStream_TolerateNonJSONBanners(t *testing.T) {
	stream := strings.NewReader(`Claude Code v1.0.0
Starting headless mode...
{"event":"assistant_message","content":"hello"}
not JSON either
{"event":"stop","stop_hook_active":false}
`)
	tr := parseClaudeCodeStream(stream)
	if !tr.CompletionAttempted {
		t.Errorf("non-JSON banner lines should not block completion parsing")
	}
}

func TestClaudeCodeDispatcher_MissingBinaryReturnsNotSupported(t *testing.T) {
	d := &ClaudeCodeDispatcher{BinaryPath: "/nonexistent/path/to/claude-bin-xyz"}
	tr, err := d.Run(context.Background(), &bench.MissionConfig{ID: "x", Intent: "noop"}, t.TempDir(), time.Second)
	if err != nil {
		t.Fatalf("Run returned error for missing binary; should report NotSupported via Trace: %v", err)
	}
	if tr.ExitReason != ExitReasonNotSupported {
		t.Errorf("ExitReason = %q, want %q", tr.ExitReason, ExitReasonNotSupported)
	}
}

func TestClaudeCodeDispatcher_NilMissionErrors(t *testing.T) {
	d := &ClaudeCodeDispatcher{}
	_, err := d.Run(context.Background(), nil, t.TempDir(), time.Second)
	if err == nil {
		t.Errorf("nil mission should error")
	}
}

func TestClaudeCodeDispatcher_RegisteredInRegistry(t *testing.T) {
	d, ok := Registry["claude-code-default"]
	if !ok {
		t.Fatalf("registry missing claude-code-default")
	}
	if d.Agent().ID != "claude-code-default" {
		t.Errorf("registered dispatcher ID mismatch: %q", d.Agent().ID)
	}
}
