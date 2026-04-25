package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/RelayOne/r1-agent/internal/stream"
)

// --- Model Creation ---

func TestNewInteractiveModel(t *testing.T) {
	m := NewInteractiveModel("plan-1", 5)
	if m.planID != "plan-1" {
		t.Errorf("planID=%q, want plan-1", m.planID)
	}
	if m.totalTasks != 5 {
		t.Errorf("totalTasks=%d, want 5", m.totalTasks)
	}
	if m.mode != ModeDashboard {
		t.Error("initial mode should be Dashboard")
	}
	if m.cursor != 0 {
		t.Error("initial cursor should be 0")
	}
	if m.done {
		t.Error("initial done should be false")
	}
	if m.width != 80 || m.height != 24 {
		t.Error("default dimensions should be 80x24")
	}
}

// --- Task Lifecycle ---

func TestTaskStartAddsTask(t *testing.T) {
	m := NewInteractiveModel("p-1", 3)
	m, _ = applyMsg(m, taskStartMsg{id: "t-1", desc: "Build JWT auth", pool: "claude"})

	if len(m.tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(m.tasks))
	}
	task := m.tasks[0]
	if task.ID != "t-1" || task.Description != "Build JWT auth" || task.PoolID != "claude" {
		t.Errorf("task fields wrong: %+v", task)
	}
	if task.Status != "active" {
		t.Errorf("status=%q, want active", task.Status)
	}
	if m.focusTask != "t-1" {
		t.Error("first task should auto-focus")
	}
	if m.taskIndex["t-1"] != 0 {
		t.Error("taskIndex should map t-1 to 0")
	}
}

func TestTaskStartMultiple(t *testing.T) {
	m := NewInteractiveModel("p-1", 3)
	m, _ = applyMsg(m, taskStartMsg{id: "t-1", desc: "Task A", pool: "claude"})
	m, _ = applyMsg(m, taskStartMsg{id: "t-2", desc: "Task B", pool: "codex"})

	if len(m.tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(m.tasks))
	}
	// First task remains focused
	if m.focusTask != "t-1" {
		t.Error("focus should stay on first task")
	}
}

func TestTaskEventAddsToHistory(t *testing.T) {
	m := NewInteractiveModel("p-1", 1)
	m, _ = applyMsg(m, taskStartMsg{id: "t-1", desc: "Build", pool: "claude"})

	ev := stream.Event{
		Type: "assistant",
		ToolUses: []stream.ToolUse{{ID: "tu-1", Name: "Read", Input: map[string]interface{}{"file": "main.go"}}},
	}
	m, _ = applyMsg(m, taskEventMsg{id: "t-1", ev: ev})

	task := m.tasks[0]
	if len(task.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(task.Events))
	}
	if task.LastTool != "Read" {
		t.Errorf("LastTool=%q, want Read", task.LastTool)
	}
}

func TestTaskEventUnknownIDIgnored(t *testing.T) {
	m := NewInteractiveModel("p-1", 1)
	ev := stream.Event{Type: "assistant"}
	m, _ = applyMsg(m, taskEventMsg{id: "nonexistent", ev: ev})
	if len(m.tasks) != 0 {
		t.Error("should not add task for unknown ID")
	}
}

func TestTaskCompleteSuccess(t *testing.T) {
	m := NewInteractiveModel("p-1", 1)
	m, _ = applyMsg(m, taskStartMsg{id: "t-1", desc: "Build", pool: "claude"})
	m, _ = applyMsg(m, taskCompleteMsg{id: "t-1", success: true, cost: 0.05, dur: 12.5, attempt: 1, verdict: "Claimed: done\nVerified: pass"})

	task := m.tasks[0]
	if task.Status != "done" {
		t.Errorf("status=%q, want done", task.Status)
	}
	if task.CostUSD != 0.05 {
		t.Errorf("cost=%f, want 0.05", task.CostUSD)
	}
	if task.Verdict == "" {
		t.Error("verdict should be set")
	}
	if m.totalCost != 0.05 {
		t.Errorf("totalCost=%f, want 0.05", m.totalCost)
	}
}

func TestTaskCompleteFailure(t *testing.T) {
	m := NewInteractiveModel("p-1", 1)
	m, _ = applyMsg(m, taskStartMsg{id: "t-1", desc: "Build", pool: "claude"})
	m, _ = applyMsg(m, taskCompleteMsg{id: "t-1", success: false, err: "test failed"})

	task := m.tasks[0]
	if task.Status != "failed" {
		t.Errorf("status=%q, want failed", task.Status)
	}
	if task.Error != "test failed" {
		t.Errorf("error=%q, want 'test failed'", task.Error)
	}
}

func TestInteractiveCostAccumulation(t *testing.T) {
	m := NewInteractiveModel("p-1", 2)
	m, _ = applyMsg(m, taskStartMsg{id: "t-1", desc: "A", pool: "c"})
	m, _ = applyMsg(m, taskStartMsg{id: "t-2", desc: "B", pool: "c"})
	m, _ = applyMsg(m, taskCompleteMsg{id: "t-1", success: true, cost: 0.10})
	m, _ = applyMsg(m, taskCompleteMsg{id: "t-2", success: true, cost: 0.25})

	if m.totalCost != 0.35 {
		t.Errorf("totalCost=%f, want 0.35", m.totalCost)
	}
}

// --- Mode Transitions ---

func TestModeSwitching(t *testing.T) {
	m := NewInteractiveModel("p-1", 1)
	m, _ = applyMsg(m, taskStartMsg{id: "t-1", desc: "Build", pool: "c"})

	// Dashboard → Focus via 'f'
	m, _ = applyKey(m, "f")
	if m.mode != ModeFocus {
		t.Errorf("mode=%d, want Focus (%d)", m.mode, ModeFocus)
	}
	if m.focusTask != "t-1" {
		t.Error("focus should auto-select active task")
	}

	// Focus → Dashboard via 'd'
	m, _ = applyKey(m, "d")
	if m.mode != ModeDashboard {
		t.Errorf("mode=%d, want Dashboard", m.mode)
	}

	// Dashboard → Detail via 'enter'
	m, _ = applyKey(m, "enter")
	if m.mode != ModeDetail {
		t.Errorf("mode=%d, want Detail", m.mode)
	}
	if m.detailTask != "t-1" {
		t.Error("detail should show task at cursor")
	}

	// Detail → Dashboard via 'esc'
	m, _ = applyKey(m, "esc")
	if m.mode != ModeDashboard {
		t.Errorf("mode=%d, want Dashboard after esc", m.mode)
	}
}

func TestFocusAutoSelectsActiveTask(t *testing.T) {
	m := NewInteractiveModel("p-1", 2)
	m, _ = applyMsg(m, taskStartMsg{id: "t-1", desc: "A", pool: "c"})
	m, _ = applyMsg(m, taskCompleteMsg{id: "t-1", success: true, cost: 0.01})
	m, _ = applyMsg(m, taskStartMsg{id: "t-2", desc: "B", pool: "c"})

	m.focusTask = "" // clear focus
	m, _ = applyKey(m, "f")
	if m.focusTask != "t-2" {
		t.Errorf("focusTask=%q, should auto-select t-2 (the active task)", m.focusTask)
	}
}

func TestEnterOnEmptyDashboard(t *testing.T) {
	m := NewInteractiveModel("p-1", 0)
	m, _ = applyKey(m, "enter")
	// Should not crash or switch to Detail with no tasks
	if m.mode == ModeDetail {
		t.Error("enter on empty dashboard should not switch to Detail")
	}
}

// --- Navigation ---

func TestCursorNavigation(t *testing.T) {
	m := NewInteractiveModel("p-1", 3)
	m, _ = applyMsg(m, taskStartMsg{id: "t-1", desc: "A", pool: "c"})
	m, _ = applyMsg(m, taskStartMsg{id: "t-2", desc: "B", pool: "c"})
	m, _ = applyMsg(m, taskStartMsg{id: "t-3", desc: "C", pool: "c"})

	// Down
	m, _ = applyKey(m, "down")
	if m.cursor != 1 {
		t.Errorf("cursor=%d, want 1 after down", m.cursor)
	}
	m, _ = applyKey(m, "j")
	if m.cursor != 2 {
		t.Errorf("cursor=%d, want 2 after j", m.cursor)
	}
	// Boundary: can't go past last
	m, _ = applyKey(m, "down")
	if m.cursor != 2 {
		t.Errorf("cursor=%d, want 2 (boundary)", m.cursor)
	}

	// Up
	m, _ = applyKey(m, "up")
	if m.cursor != 1 {
		t.Errorf("cursor=%d, want 1 after up", m.cursor)
	}
	m, _ = applyKey(m, "k")
	if m.cursor != 0 {
		t.Errorf("cursor=%d, want 0 after k", m.cursor)
	}
	// Boundary: can't go above 0
	m, _ = applyKey(m, "up")
	if m.cursor != 0 {
		t.Errorf("cursor=%d, want 0 (boundary)", m.cursor)
	}
}

func TestTabCyclesFocus(t *testing.T) {
	m := NewInteractiveModel("p-1", 2)
	m, _ = applyMsg(m, taskStartMsg{id: "t-1", desc: "A", pool: "c"})
	m, _ = applyMsg(m, taskStartMsg{id: "t-2", desc: "B", pool: "c"})
	m.focusTask = "t-1"

	m, _ = applyKey(m, "tab")
	if m.focusTask != "t-2" {
		t.Errorf("tab should cycle focus to t-2, got %q", m.focusTask)
	}
}

// --- Pool Updates ---

func TestPoolUpdateMsg(t *testing.T) {
	m := NewInteractiveModel("p-1", 1)
	pools := []PoolInfo{
		{ID: "claude", Utilization: 75.0},
		{ID: "codex", Utilization: 30.0},
	}
	m, _ = applyMsg(m, poolUpdateMsg{pools: pools})

	if len(m.pools) != 2 {
		t.Fatalf("expected 2 pools, got %d", len(m.pools))
	}
	if m.pools[0].ID != "claude" || m.pools[0].Utilization != 75.0 {
		t.Errorf("pool 0 wrong: %+v", m.pools[0])
	}
}

// --- Done Message ---

func TestDoneMsgQuitsProgram(t *testing.T) {
	m := NewInteractiveModel("p-1", 1)
	m2, cmd := applyMsg(m, doneMsg{})
	if !m2.done {
		t.Error("done should be true after doneMsg")
	}
	if cmd == nil {
		t.Error("doneMsg should return tea.Quit command")
	}
}

// --- Window Resize ---

func TestWindowResize(t *testing.T) {
	m := NewInteractiveModel("p-1", 1)
	m, _ = applyMsg(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if m.width != 120 || m.height != 40 {
		t.Errorf("size=%dx%d, want 120x40", m.width, m.height)
	}
}

// --- View Rendering ---

func TestDashboardViewContainsPlanID(t *testing.T) {
	m := NewInteractiveModel("plan-jwt", 2)
	m, _ = applyMsg(m, taskStartMsg{id: "t-1", desc: "JWT Auth", pool: "claude"})
	view := m.View()

	if !strings.Contains(view, "plan-jwt") {
		t.Error("dashboard should show plan ID")
	}
	if !strings.Contains(view, "JWT Auth") {
		t.Error("dashboard should show task description")
	}
	if !strings.Contains(view, "t-1") {
		t.Error("dashboard should show task ID")
	}
}

func TestDashboardViewShowsCounts(t *testing.T) {
	m := NewInteractiveModel("p-1", 3)
	m, _ = applyMsg(m, taskStartMsg{id: "t-1", desc: "A", pool: "c"})
	m, _ = applyMsg(m, taskStartMsg{id: "t-2", desc: "B", pool: "c"})
	m, _ = applyMsg(m, taskCompleteMsg{id: "t-1", success: true, cost: 0.01})

	view := m.View()
	if !strings.Contains(view, "1/3 done") {
		t.Errorf("dashboard should show '1/3 done', got: %s", view)
	}
	if !strings.Contains(view, "1 active") {
		t.Errorf("dashboard should show '1 active'")
	}
}

func TestDashboardViewShowsPoolBars(t *testing.T) {
	m := NewInteractiveModel("p-1", 1)
	m, _ = applyMsg(m, poolUpdateMsg{pools: []PoolInfo{{ID: "claude", Utilization: 50.0}}})

	view := m.View()
	if !strings.Contains(view, "claude") {
		t.Error("dashboard should show pool ID")
	}
	if !strings.Contains(view, "50%") {
		t.Error("dashboard should show utilization percentage")
	}
}

func TestDashboardViewShowsHelpKeys(t *testing.T) {
	m := NewInteractiveModel("p-1", 0)
	view := m.View()
	for _, key := range []string{"d=dashboard", "f=focus", "enter=detail", "q=quit"} {
		if !strings.Contains(view, key) {
			t.Errorf("dashboard should show help key %q", key)
		}
	}
}

func TestFocusViewShowsTaskDetail(t *testing.T) {
	m := NewInteractiveModel("p-1", 1)
	m, _ = applyMsg(m, taskStartMsg{id: "t-1", desc: "Build JWT", pool: "claude"})
	m, _ = applyMsg(m, taskEventMsg{id: "t-1", ev: stream.Event{
		Type:     "assistant",
		ToolUses: []stream.ToolUse{{Name: "Edit", Input: map[string]interface{}{"file": "auth.go"}}},
	}})
	m, _ = applyKey(m, "f")

	view := m.View()
	if !strings.Contains(view, "FOCUS") {
		t.Error("focus view should show FOCUS header")
	}
	if !strings.Contains(view, "Build JWT") {
		t.Error("focus view should show task description")
	}
	if !strings.Contains(view, "Edit") {
		t.Error("focus view should show tool use")
	}
}

func TestFocusViewNoActiveTask(t *testing.T) {
	m := NewInteractiveModel("p-1", 0)
	m.mode = ModeFocus
	m.focusTask = "nonexistent"
	view := m.View()
	if !strings.Contains(view, "No active task") {
		t.Error("focus view should handle missing task gracefully")
	}
}

func TestDetailViewShowsVerdict(t *testing.T) {
	m := NewInteractiveModel("p-1", 1)
	m, _ = applyMsg(m, taskStartMsg{id: "t-1", desc: "Build", pool: "c"})
	m, _ = applyMsg(m, taskCompleteMsg{id: "t-1", success: true, cost: 0.05, dur: 10.0, verdict: "Claimed: complete\nVerified: 3/3 criteria"})
	m.mode = ModeDetail
	m.detailTask = "t-1"

	view := m.View()
	if !strings.Contains(view, "Claimed vs Verified") {
		t.Error("detail view should show Claimed vs Verified header")
	}
	if !strings.Contains(view, "3/3 criteria") {
		t.Error("detail view should show verdict content")
	}
}

func TestDetailViewShowsError(t *testing.T) {
	m := NewInteractiveModel("p-1", 1)
	m, _ = applyMsg(m, taskStartMsg{id: "t-1", desc: "Build", pool: "c"})
	m, _ = applyMsg(m, taskCompleteMsg{id: "t-1", success: false, err: "compilation failed"})
	m.mode = ModeDetail
	m.detailTask = "t-1"

	view := m.View()
	if !strings.Contains(view, "compilation failed") {
		t.Error("detail view should show error message")
	}
}

func TestDetailViewShowsToolCounts(t *testing.T) {
	m := NewInteractiveModel("p-1", 1)
	m, _ = applyMsg(m, taskStartMsg{id: "t-1", desc: "Build", pool: "c"})
	for i := 0; i < 3; i++ {
		m, _ = applyMsg(m, taskEventMsg{id: "t-1", ev: stream.Event{
			Type:     "assistant",
			ToolUses: []stream.ToolUse{{Name: "Edit"}},
		}})
	}
	m, _ = applyMsg(m, taskEventMsg{id: "t-1", ev: stream.Event{
		Type:     "assistant",
		ToolUses: []stream.ToolUse{{Name: "Read"}},
	}})
	m.mode = ModeDetail
	m.detailTask = "t-1"

	view := m.View()
	if !strings.Contains(view, "Edit: 3") {
		t.Error("detail view should show Edit: 3 tool count")
	}
	if !strings.Contains(view, "Read: 1") {
		t.Error("detail view should show Read: 1 tool count")
	}
}

func TestDetailViewMissingTask(t *testing.T) {
	m := NewInteractiveModel("p-1", 0)
	m.mode = ModeDetail
	m.detailTask = "nonexistent"
	view := m.View()
	if !strings.Contains(view, "Task not found") {
		t.Error("detail view should handle missing task")
	}
}

// --- Helper Functions ---

func TestCountsHelper(t *testing.T) {
	m := NewInteractiveModel("p-1", 4)
	m, _ = applyMsg(m, taskStartMsg{id: "t-1", desc: "A", pool: "c"})
	m, _ = applyMsg(m, taskStartMsg{id: "t-2", desc: "B", pool: "c"})
	m, _ = applyMsg(m, taskStartMsg{id: "t-3", desc: "C", pool: "c"})
	m, _ = applyMsg(m, taskCompleteMsg{id: "t-1", success: true})
	m, _ = applyMsg(m, taskCompleteMsg{id: "t-2", success: false})

	done, failed, active := m.counts()
	if done != 1 {
		t.Errorf("done=%d, want 1", done)
	}
	if failed != 1 {
		t.Errorf("failed=%d, want 1", failed)
	}
	if active != 1 {
		t.Errorf("active=%d, want 1", active)
	}
}

func TestTaskIconFunction(t *testing.T) {
	tests := []struct {
		status string
		icon   string
	}{
		{"done", "✓"},
		{"failed", "✗"},
		{"active", "▸"},
		{"pending", "○"},
	}
	for _, tt := range tests {
		icon, _ := taskIcon(tt.status)
		if icon != tt.icon {
			t.Errorf("taskIcon(%q)=%q, want %q", tt.status, icon, tt.icon)
		}
	}
}

func TestRenderBar(t *testing.T) {
	// 0% should be all empty
	bar := renderBar(0, 10)
	if !strings.Contains(bar, "░") {
		t.Error("0% bar should contain empty blocks")
	}

	// 100% should be all full
	bar = renderBar(100, 10)
	if !strings.Contains(bar, "█") {
		t.Error("100% bar should contain full blocks")
	}

	// Out of bounds should clamp
	bar = renderBar(150, 5)
	if bar == "" {
		t.Error("bar should handle >100%")
	}
	bar = renderBar(-10, 5)
	if bar == "" {
		t.Error("bar should handle negative values")
	}
}

func TestTruncStr(t *testing.T) {
	if truncStr("hello", 10) != "hello" {
		t.Error("short string should not be truncated")
	}
	if truncStr("hello world this is long", 10) != "hello w..." {
		t.Errorf("truncated=%q", truncStr("hello world this is long", 10))
	}
	if truncStr("ab", 2) != "ab" {
		t.Error("exact length should not truncate")
	}
}

// --- Test Helpers ---

func applyMsg(m *InteractiveModel, msg tea.Msg) (*InteractiveModel, tea.Cmd) {
	model, cmd := m.Update(msg)
	//nolint:forcetypeassert // test helper; panic on surprise is acceptable
	return model.(*InteractiveModel), cmd
}

func applyKey(m *InteractiveModel, key string) (*InteractiveModel, tea.Cmd) {
	return applyMsg(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
}
