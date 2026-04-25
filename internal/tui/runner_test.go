package tui

import (
	"testing"

	"github.com/RelayOne/r1-agent/internal/stream"
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

	// Glob/Grep extract pattern
	tu3 := stream.ToolUse{Name: "Glob", Input: map[string]interface{}{"pattern": "**/*.go"}}
	if toolInput(tu3) != "**/*.go" {
		t.Errorf("toolInput(Glob)=%q", toolInput(tu3))
	}

	tu4 := stream.ToolUse{Name: "Grep", Input: map[string]interface{}{"pattern": "func main"}}
	if toolInput(tu4) != "func main" {
		t.Errorf("toolInput(Grep)=%q", toolInput(tu4))
	}

	// Write extracts file_path
	tu5 := stream.ToolUse{Name: "Write", Input: map[string]interface{}{"file_path": "new.go"}}
	if toolInput(tu5) != "new.go" {
		t.Errorf("toolInput(Write)=%q", toolInput(tu5))
	}

	// Unknown tool returns empty
	tu6 := stream.ToolUse{Name: "Unknown", Input: map[string]interface{}{"foo": "bar"}}
	if toolInput(tu6) != "" {
		t.Errorf("toolInput(Unknown)=%q, want empty", toolInput(tu6))
	}
}

// --- Runner methods that were previously untested ---

func TestTaskRetryOutput(t *testing.T) {
	r := NewRunner()
	// TaskRetry should not panic and should not change counters
	r.TaskRetry("T1", 2, "test compilation failed")
	if r.tasksDone != 0 || r.tasksFailed != 0 {
		t.Error("TaskRetry should not change done/failed counts")
	}
}

func TestTaskEscalateOutput(t *testing.T) {
	r := NewRunner()
	r.TaskEscalate("T1", "exceeded max retries")
	if r.tasksDone != 0 || r.tasksFailed != 0 {
		t.Error("TaskEscalate should not change done/failed counts")
	}
}

func TestStatusOutput(t *testing.T) {
	r := NewRunner()
	r.TaskComplete("T1", true, 1.0, 0.10, 1)
	// Status should not panic
	r.Status(5, 2)
	r.mu.Lock()
	if r.tasksDone != 1 {
		t.Error("Status should not modify tasksDone")
	}
	r.mu.Unlock()
}

func TestSummaryOutput(t *testing.T) {
	r := NewRunner()
	r.TaskComplete("T1", true, 1.0, 0.10, 1)
	r.TaskComplete("T2", false, 2.0, 0.05, 1)
	// Summary should not panic
	r.Summary(3)
	r.mu.Lock()
	if r.tasksDone != 1 || r.tasksFailed != 1 {
		t.Error("Summary should not modify counters")
	}
	r.mu.Unlock()
}

func TestPoolStatusOutput(t *testing.T) {
	// PoolStatus is a package-level function, not a method
	pools := []PoolInfo{
		{ID: "claude", Label: "Claude 3.5", Utilization: 75.0},
		{ID: "codex", Label: "Codex", Utilization: 0.0},
	}
	// Should not panic
	PoolStatus(pools)
}

func TestBarEdgeCases(t *testing.T) {
	// 0% — all empty
	b := bar(0, 10)
	if b != "░░░░░░░░░░" {
		t.Errorf("0%% bar=%q", b)
	}
	// 100% — all full
	b = bar(100, 10)
	if b != "██████████" {
		t.Errorf("100%% bar=%q", b)
	}
	// Over 100% — clamped
	b = bar(150, 5)
	if b != "█████" {
		t.Errorf("150%% bar=%q", b)
	}
	// Negative — clamped to 0
	b = bar(-10, 5)
	if b != "░░░░░" {
		t.Errorf("-10%% bar=%q", b)
	}
}

func TestEventWithToolResults(t *testing.T) {
	r := NewRunner()
	r.TaskStart("T1", "Build", "claude")

	// Success result
	r.Event("T1", stream.Event{
		Type: "user",
		ToolResults: []stream.ToolResult{
			{Content: "file written", IsError: false},
		},
	})

	// Error result
	r.Event("T1", stream.Event{
		Type: "user",
		ToolResults: []stream.ToolResult{
			{Content: "permission denied", IsError: true},
		},
	})

	// Should not panic and counts should be unaffected
	if r.tasksDone != 0 {
		t.Error("Event should not change done count")
	}
}
