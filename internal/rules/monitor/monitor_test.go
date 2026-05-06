package monitor

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestMonitorRecordAndList(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	m := NewRepo(repo)
	if err := m.Record(Decision{
		ToolName: "delete_branch",
		Verdict:  "BLOCK",
		Reason:   "rule blocked branch deletion",
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	decisions, err := m.List(10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(decisions) != 1 {
		t.Fatalf("len(List) = %d, want 1", len(decisions))
	}
	if decisions[0].ToolName != "delete_branch" {
		t.Fatalf("tool_name = %q, want delete_branch", decisions[0].ToolName)
	}
}

func TestMonitorTailLastWithoutFollow(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	m := NewRepo(repo)
	for _, tool := range []string{"bash", "delete_branch"} {
		if err := m.Record(Decision{
			Timestamp: time.Unix(1710000000, 0).UTC(),
			ToolName:  tool,
			Verdict:   "WARN",
			Reason:    "logged for test",
		}); err != nil {
			t.Fatalf("Record %s: %v", tool, err)
		}
	}

	var out bytes.Buffer
	if err := m.Tail(context.Background(), &out, false, 1); err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if strings.Contains(out.String(), "bash") {
		t.Fatalf("tail output should not include older decision: %q", out.String())
	}
	if !strings.Contains(out.String(), "delete_branch") {
		t.Fatalf("tail output missing latest decision: %q", out.String())
	}
}
