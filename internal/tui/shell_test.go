package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestShell_Init(t *testing.T) {
	sh := NewShell(ShellConfig{Version: "test", RepoRoot: "/tmp/x"}, nil)
	cmd := sh.Init()
	if cmd == nil {
		t.Error("Init should return a tick command")
	}
}

func TestShell_AppendLog(t *testing.T) {
	sh := NewShell(ShellConfig{}, nil)
	// Without a program attached, Append should not panic.
	sh.Append("hello %s", "world")

	// Directly push a shellLogMsg via Update
	_, _ = sh.Update(shellLogMsg{lines: []string{"first", "second", "third"}})
	if len(sh.logBuf) != 3 {
		t.Errorf("logBuf=%v", sh.logBuf)
	}
}

func TestShell_SessionUpdate(t *testing.T) {
	sh := NewShell(ShellConfig{}, nil)
	_, _ = sh.Update(shellSessionsMsg{sessions: []SessionDisplay{
		{ID: "S1", Title: "First", Status: "running", TasksDone: 1, TasksTotal: 3},
		{ID: "S2", Title: "Second", Status: "pending"},
	}})
	if len(sh.sessions) != 2 {
		t.Errorf("sessions=%d", len(sh.sessions))
	}

	// Update existing session
	_, _ = sh.Update(shellSessionUpdateMsg{session: SessionDisplay{
		ID: "S1", Title: "First", Status: "done", TasksDone: 3, TasksTotal: 3,
	}})
	if sh.sessions[0].Status != "done" {
		t.Errorf("S1 status=%q", sh.sessions[0].Status)
	}

	// Upsert new session
	_, _ = sh.Update(shellSessionUpdateMsg{session: SessionDisplay{
		ID: "S3", Title: "Third", Status: "running",
	}})
	if len(sh.sessions) != 3 || sh.sessions[2].ID != "S3" {
		t.Errorf("sessions=%+v", sh.sessions)
	}
}

func TestShell_InputTyping(t *testing.T) {
	sh := NewShell(ShellConfig{}, nil)
	_, _ = sh.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	// Type "hello"
	for _, r := range "hello" {
		_, _ = sh.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if sh.input != "hello" {
		t.Errorf("input=%q", sh.input)
	}

	// Backspace
	_, _ = sh.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if sh.input != "hell" {
		t.Errorf("input=%q", sh.input)
	}
}

func TestShell_SubmitQuitCommand(t *testing.T) {
	sh := NewShell(ShellConfig{}, nil)
	sh.input = "/quit"
	_, cmd := sh.submitLine()
	if cmd == nil {
		t.Error("expected tea.Quit cmd")
	}
}

func TestShell_SubmitDispatchesHandler(t *testing.T) {
	called := make(chan string, 1)
	handler := func(sh *Shell, input string) string {
		called <- input
		return "done"
	}
	sh := NewShell(ShellConfig{}, handler)
	sh.input = "/ship Add auth"
	_, _ = sh.submitLine()
	if !sh.busy {
		t.Error("expected shell to be busy after submit")
	}

	select {
	case got := <-called:
		if got != "/ship Add auth" {
			t.Errorf("handler got %q", got)
		}
	case <-sh.stopCh:
		t.Error("shell closed before handler called")
	}
}

func TestShell_BusyRejectsNewCommand(t *testing.T) {
	sh := NewShell(ShellConfig{}, func(sh *Shell, input string) string { return "" })
	sh.busy = true
	sh.input = "/run x"
	_, _ = sh.submitLine()
	// Last log line should mention busy
	found := false
	for _, l := range sh.logBuf {
		if strings.Contains(l, "busy") {
			found = true
		}
	}
	if !found {
		t.Errorf("busy rejection not logged: %v", sh.logBuf)
	}
}

func TestShell_History(t *testing.T) {
	sh := NewShell(ShellConfig{}, func(sh *Shell, input string) string { return "" })
	sh.input = "first cmd"
	_, _ = sh.submitLine()
	sh.input = "second cmd"
	_, _ = sh.submitLine()

	// UP should recall "second cmd"
	_, _ = sh.Update(tea.KeyMsg{Type: tea.KeyUp})
	if sh.input != "second cmd" {
		t.Errorf("history up 1 = %q", sh.input)
	}
	// UP again -> "first cmd"
	_, _ = sh.Update(tea.KeyMsg{Type: tea.KeyUp})
	if sh.input != "first cmd" {
		t.Errorf("history up 2 = %q", sh.input)
	}
	// DOWN -> "second cmd"
	_, _ = sh.Update(tea.KeyMsg{Type: tea.KeyDown})
	if sh.input != "second cmd" {
		t.Errorf("history down 1 = %q", sh.input)
	}
}

func TestShell_LogCappedAt2000(t *testing.T) {
	sh := NewShell(ShellConfig{}, nil)
	var lines []string
	for i := 0; i < 2500; i++ {
		lines = append(lines, "line")
	}
	_, _ = sh.Update(shellLogMsg{lines: lines})
	if len(sh.logBuf) > 2000 {
		t.Errorf("logBuf should be capped at 2000, got %d", len(sh.logBuf))
	}
}

func TestShell_ViewRenders(t *testing.T) {
	sh := NewShell(ShellConfig{
		RepoRoot: "/test", Version: "v1.0", Runner: "native",
		BaseURL: "http://localhost:8000", Model: "claude-sonnet-4-6",
		Supervisor: "boulder",
	}, nil)
	_, _ = sh.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	_, _ = sh.Update(shellLogMsg{lines: []string{"log line 1", "log line 2"}})
	_, _ = sh.Update(shellSessionsMsg{sessions: []SessionDisplay{
		{ID: "S1", Title: "Setup", Status: "done", TasksDone: 2, TasksTotal: 2},
	}})
	sh.input = "/help"

	view := sh.View()
	for _, want := range []string{"Stoke v1.0", "native", "Sessions", "S1", "Setup", "Log", "log line", "/help"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
}

func TestShell_ScrollLog(t *testing.T) {
	sh := NewShell(ShellConfig{}, nil)
	_, _ = sh.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	var lines []string
	for i := 0; i < 100; i++ {
		lines = append(lines, "L")
	}
	_, _ = sh.Update(shellLogMsg{lines: lines})

	_, _ = sh.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	if sh.logScroll == 0 {
		t.Error("PgUp should increase scroll")
	}
	_, _ = sh.Update(tea.KeyMsg{Type: tea.KeyEnd})
	if sh.logScroll != 0 {
		t.Error("End should reset scroll to tail")
	}
}

func TestShell_TabFocusSwitches(t *testing.T) {
	sh := NewShell(ShellConfig{}, nil)
	if sh.focus != focusInput {
		t.Error("initial focus should be input")
	}
	_, _ = sh.Update(tea.KeyMsg{Type: tea.KeyTab})
	if sh.focus != focusLog {
		t.Error("after tab focus should be log")
	}
	_, _ = sh.Update(tea.KeyMsg{Type: tea.KeyTab})
	if sh.focus != focusInput {
		t.Error("after second tab focus should be input")
	}
}
