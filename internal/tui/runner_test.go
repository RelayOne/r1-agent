package tui

import (
	"testing"

	"github.com/ericmacdougall/stoke/internal/stream"
)

func TestTaskStartComplete(t *testing.T) {
	r := NewRunner()
	r.TaskStart("T1", "Add auth middleware", "claude-1")
	if r.activeTask != "T1" {
		t.Errorf("activeTask=%q", r.activeTask)
	}
	r.TaskComplete("T1", true, 5.2, 0.05, 1)
	if r.tasksDone != 1 {
		t.Errorf("tasksDone=%d", r.tasksDone)
	}
	if r.totalCost != 0.05 {
		t.Errorf("totalCost=%f", r.totalCost)
	}
}

func TestTaskFailed(t *testing.T) {
	r := NewRunner()
	r.TaskComplete("T1", false, 3.0, 0.02, 1)
	if r.tasksFailed != 1 {
		t.Errorf("tasksFailed=%d", r.tasksFailed)
	}
}

func TestCostAccumulation(t *testing.T) {
	r := NewRunner()
	r.TaskComplete("T1", true, 1.0, 0.10, 1)
	r.TaskComplete("T2", true, 1.0, 0.15, 1)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.totalCost != 0.25 {
		t.Errorf("totalCost=%f, want 0.25", r.totalCost)
	}
}

func TestTrunc(t *testing.T) {
	if trunc("hello", 10) != "hello" {
		t.Error("short string should pass through")
	}
	if trunc("a very long string that should be truncated", 20) != "a very long strin..." {
		t.Errorf("got %q", trunc("a very long string that should be truncated", 20))
	}
}

func TestBar(t *testing.T) {
	b := bar(50, 10)
	runeCount := 0
	for range b { runeCount++ }
	if runeCount != 10 {
		t.Errorf("bar rune count=%d, want 10", runeCount)
	}
	if b != "█████░░░░░" {
		t.Errorf("bar=%q", b)
	}
}

func TestToolInput(t *testing.T) {
	tu := stream.ToolUse{Name: "Read", Input: map[string]interface{}{"file_path": "main.go"}}
	if toolInput(tu) != "main.go" {
		t.Errorf("toolInput=%q", toolInput(tu))
	}

	tu2 := stream.ToolUse{Name: "Bash", Input: map[string]interface{}{"command": "npm test"}}
	if toolInput(tu2) != "npm test" {
		t.Errorf("toolInput=%q", toolInput(tu2))
	}
}
