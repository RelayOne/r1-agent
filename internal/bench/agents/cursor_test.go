package agents

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/bench"
)

func TestCursorDispatcher_Agent(t *testing.T) {
	d := &CursorDispatcher{}
	if d.Agent().ID != "cursor" {
		t.Errorf("ID = %q, want cursor", d.Agent().ID)
	}
}

func TestParseCursorOutput_FinishMarkerMarksCompletion(t *testing.T) {
	out := `[cursor-agent] starting
[cursor-agent] assistant: I'll edit the file
[cursor-agent] tool_call: edit
[cursor-agent] assistant: All done.
[cursor-agent] task finished
`
	attempt, last := parseCursorOutput(out)
	if !attempt {
		t.Errorf("CompletionAttempted = false, want true (finish marker present)")
	}
	if !strings.Contains(last, "All done") {
		t.Errorf("LastAssistantText = %q, want trailing 'All done'", last)
	}
}

func TestParseCursorOutput_NoFinishMeansNoCompletion(t *testing.T) {
	out := `[cursor-agent] starting
[cursor-agent] assistant: thinking out loud
`
	attempt, _ := parseCursorOutput(out)
	if attempt {
		t.Errorf("CompletionAttempted = true, want false (no finish marker)")
	}
}

func TestCursorDispatcher_MissingBinaryReturnsNotSupported(t *testing.T) {
	d := &CursorDispatcher{BinaryPath: "/nonexistent/cursor-agent-bin-xyz"}
	tr, err := d.Run(context.Background(), &bench.MissionConfig{ID: "x", Intent: "noop"}, t.TempDir(), time.Second)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if tr.ExitReason != ExitReasonNotSupported {
		t.Errorf("ExitReason = %q, want %q", tr.ExitReason, ExitReasonNotSupported)
	}
}

func TestCursorDispatcher_RegisteredInRegistry(t *testing.T) {
	if _, ok := Registry["cursor"]; !ok {
		t.Errorf("registry missing cursor")
	}
}
