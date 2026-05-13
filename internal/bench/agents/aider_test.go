package agents

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/bench"
)

func TestAiderDispatcher_Agent(t *testing.T) {
	d := &AiderDispatcher{}
	if d.Agent().ID != "aider" {
		t.Errorf("ID = %q, want aider", d.Agent().ID)
	}
}

func TestParseAiderOutput_AppliedEditMarksCompletion(t *testing.T) {
	out := `Loaded model. Reading repository.
Aider response: I will refactor the handler.
Applied edit to internal/foo/foo.go
`
	attempt, last := parseAiderOutput(out)
	if !attempt {
		t.Errorf("CompletionAttempted = false, want true (Applied edit found)")
	}
	if !strings.Contains(last, "refactor the handler") {
		t.Errorf("LastAssistantText = %q, want substring 'refactor the handler'", last)
	}
}

func TestParseAiderOutput_CommittedChangeMarksCompletion(t *testing.T) {
	out := `Aider response: shipped it.
Committed change.
`
	attempt, _ := parseAiderOutput(out)
	if !attempt {
		t.Errorf("CompletionAttempted = false, want true (Committed change.)")
	}
}

func TestParseAiderOutput_NoEditNoCompletion(t *testing.T) {
	out := `Aider response: I'm thinking about it.
Hmm, this is hard.
`
	attempt, _ := parseAiderOutput(out)
	if attempt {
		t.Errorf("CompletionAttempted = true, want false (no Applied edit / Committed)")
	}
}

func TestAiderDispatcher_MissingBinaryReturnsNotSupported(t *testing.T) {
	d := &AiderDispatcher{BinaryPath: "/nonexistent/aider-bin-xyz"}
	tr, err := d.Run(context.Background(), &bench.MissionConfig{ID: "x", Intent: "noop"}, t.TempDir(), time.Second)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if tr.ExitReason != ExitReasonNotSupported {
		t.Errorf("ExitReason = %q, want %q", tr.ExitReason, ExitReasonNotSupported)
	}
}

func TestAiderDispatcher_RegisteredInRegistry(t *testing.T) {
	if _, ok := Registry["aider"]; !ok {
		t.Errorf("registry missing aider")
	}
}
