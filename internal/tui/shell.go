// Full-screen Bubble Tea shell for Stoke. Fuses the slash-command REPL with
// live mission monitoring, log tail, and a text input pane.
//
// Layout (80x24 default; adapts to window size):
//
//   ┌─ Stoke ────────────────────────────── runner: native · super: boulder ─┐
//   │ repo: /path/to/project                                                  │
//   │ base: http://localhost:8000  model: claude-sonnet-4-6                   │
//   ├─ Sessions ──────────────────────────────────────────────────────────────┤
//   │ ✓ S1 Setup            (2/2 tasks, 3/3 criteria)            $0.12  42s  │
//   │ ▸ S2 Auth             (1/3 tasks, 0/4 criteria)            $0.08  12s  │
//   │ ○ S3 API              pending                                           │
//   ├─ Log ───────────────────────────────────────────────────────────────────┤
//   │ [S2] dispatching 3 tasks to worker pool                                │
//   │ [S2 T5] Write src/auth/token.ts                                        │
//   │ [S2 T5]   ✓ token.ts created                                           │
//   ├─ Input ─────────────────────────────────────────────────────────────────┤
//   │ > /build-from-scope                                                    │
//   └─────────────────────────────────────────────────────────────────────────┘
//
// Key bindings: Enter submits, Ctrl+C quits, PgUp/PgDn scrolls the log,
// Tab switches focus between input and log panes.
package tui

import (
	"fmt"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ShellConfig controls what's displayed in the banner and how commands run.
type ShellConfig struct {
	RepoRoot    string
	Version     string
	Runner      string
	BaseURL     string
	Model       string
	Supervisor  string // e.g. "boulder"
	Notes       []string
}

// CommandHandler is invoked when the user submits a command. It runs in a
// background goroutine; the handler should write progress via Append() and
// update sessions via SetSessions / UpdateSession. Return value is a short
// status message ("done", "failed: ...") shown after completion.
type CommandHandler func(shell *Shell, input string) string

// SessionDisplay is a one-line summary the shell renders in the Sessions pane.
type SessionDisplay struct {
	ID              string
	Title           string
	Status          string // pending | running | done | failed | skipped
	TasksDone       int
	TasksTotal      int
	CriteriaDone    int
	CriteriaTotal   int
	CostUSD         float64
	DurationSec     float64
	LastError       string
}

// Shell is a Bubble Tea full-screen shell model.
type Shell struct {
	cfg ShellConfig

	mu       sync.Mutex
	sessions []SessionDisplay
	logBuf   []string
	logScroll int // 0 = follow tail, positive = scrolled up N lines
	input    string
	history  []string
	histPos  int

	width, height int
	focus         focusPane
	status        string // one-line status after command completion
	busy          bool

	handler CommandHandler
	program *tea.Program
	// stopCh closes when the shell quits. Command goroutines should honor it.
	stopCh chan struct{}

	startedAt time.Time
}

type focusPane int

const (
	focusInput focusPane = iota
	focusLog
)

// NewShell creates a new full-screen TUI shell.
func NewShell(cfg ShellConfig, handler CommandHandler) *Shell {
	return &Shell{
		cfg:       cfg,
		width:     100,
		height:    32,
		focus:     focusInput,
		handler:   handler,
		stopCh:    make(chan struct{}),
		startedAt: time.Now(),
	}
}

// Run launches the Bubble Tea program. Blocks until the user quits.
func (sh *Shell) Run() error {
	p := tea.NewProgram(sh, tea.WithAltScreen(), tea.WithMouseCellMotion())
	sh.program = p
	_, err := p.Run()
	close(sh.stopCh)
	return err
}

// StopCh returns a channel that closes when the shell quits. Handlers should
// select on it to abort long-running work cleanly.
func (sh *Shell) StopCh() <-chan struct{} { return sh.stopCh }

// --- Tea messages ---

type shellLogMsg struct{ lines []string }
type shellSessionsMsg struct{ sessions []SessionDisplay }
type shellSessionUpdateMsg struct{ session SessionDisplay }
type shellStatusMsg struct{ text string }
type shellCommandDoneMsg struct{ status string }
type shellTickMsg time.Time

func shellTick() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg { return shellTickMsg(t) })
}

// --- Public API for handlers to feed the shell from goroutines ---

// Append adds a line to the log pane.
func (sh *Shell) Append(format string, args ...interface{}) {
	line := fmt.Sprintf(format, args...)
	if sh.program != nil {
		sh.program.Send(shellLogMsg{lines: []string{line}})
	}
}

// AppendLines adds multiple lines at once (preserves newlines inside a block).
func (sh *Shell) AppendLines(block string) {
	lines := strings.Split(strings.TrimRight(block, "\n"), "\n")
	if sh.program != nil {
		sh.program.Send(shellLogMsg{lines: lines})
	}
}

// SetSessions replaces the sessions display with a fresh list.
func (sh *Shell) SetSessions(sessions []SessionDisplay) {
	if sh.program != nil {
		sh.program.Send(shellSessionsMsg{sessions: sessions})
	}
}

// UpdateSession replaces or appends a single session by ID.
func (sh *Shell) UpdateSession(s SessionDisplay) {
	if sh.program != nil {
		sh.program.Send(shellSessionUpdateMsg{session: s})
	}
}

// SetStatus sets the bottom status line (shown next to the prompt).
func (sh *Shell) SetStatus(format string, args ...interface{}) {
	text := fmt.Sprintf(format, args...)
	if sh.program != nil {
		sh.program.Send(shellStatusMsg{text: text})
	}
}

// --- Tea interface ---

func (sh *Shell) Init() tea.Cmd {
	return shellTick()
}

func (sh *Shell) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		sh.width = m.Width
		sh.height = m.Height
		return sh, nil

	case tea.KeyMsg:
		return sh.handleKey(m)

	case shellLogMsg:
		sh.mu.Lock()
		sh.logBuf = append(sh.logBuf, m.lines...)
		if len(sh.logBuf) > 2000 {
			sh.logBuf = sh.logBuf[len(sh.logBuf)-1500:]
		}
		sh.mu.Unlock()
		return sh, nil

	case shellSessionsMsg:
		sh.mu.Lock()
		sh.sessions = m.sessions
		sh.mu.Unlock()
		return sh, nil

	case shellSessionUpdateMsg:
		sh.mu.Lock()
		found := false
		for i, s := range sh.sessions {
			if s.ID == m.session.ID {
				sh.sessions[i] = m.session
				found = true
				break
			}
		}
		if !found {
			sh.sessions = append(sh.sessions, m.session)
		}
		sh.mu.Unlock()
		return sh, nil

	case shellStatusMsg:
		sh.mu.Lock()
		sh.status = m.text
		sh.mu.Unlock()
		return sh, nil

	case shellCommandDoneMsg:
		sh.mu.Lock()
		sh.busy = false
		sh.status = m.status
		sh.mu.Unlock()
		return sh, nil

	case shellTickMsg:
		return sh, shellTick()
	}
	return sh, nil
}

func (sh *Shell) handleKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	sh.mu.Lock()
	defer sh.mu.Unlock()

	switch m.String() {
	case "ctrl+c":
		return sh, tea.Quit
	case "ctrl+d":
		if sh.input == "" {
			return sh, tea.Quit
		}
	case "tab":
		if sh.focus == focusInput {
			sh.focus = focusLog
		} else {
			sh.focus = focusInput
		}
		return sh, nil
	case "pgup":
		sh.logScroll += 10
		return sh, nil
	case "pgdown":
		sh.logScroll -= 10
		if sh.logScroll < 0 {
			sh.logScroll = 0
		}
		return sh, nil
	case "home":
		sh.logScroll = len(sh.logBuf)
		return sh, nil
	case "end":
		sh.logScroll = 0
		return sh, nil
	}

	if sh.focus != focusInput {
		return sh, nil
	}

	switch m.Type {
	case tea.KeyEnter:
		return sh.submitLine()
	case tea.KeyBackspace:
		if len(sh.input) > 0 {
			sh.input = sh.input[:len(sh.input)-1]
		}
	case tea.KeyUp:
		if len(sh.history) > 0 && sh.histPos > 0 {
			sh.histPos--
			sh.input = sh.history[sh.histPos]
		}
	case tea.KeyDown:
		if sh.histPos < len(sh.history)-1 {
			sh.histPos++
			sh.input = sh.history[sh.histPos]
		} else if sh.histPos == len(sh.history)-1 {
			sh.histPos = len(sh.history)
			sh.input = ""
		}
	case tea.KeyRunes, tea.KeySpace:
		sh.input += string(m.Runes)
	}
	return sh, nil
}

func (sh *Shell) submitLine() (tea.Model, tea.Cmd) {
	line := strings.TrimSpace(sh.input)
	sh.input = ""
	if line == "" {
		return sh, nil
	}
	// History
	sh.history = append(sh.history, line)
	sh.histPos = len(sh.history)

	// Built-in /quit
	if line == "/quit" || line == "/exit" || line == "/q" {
		return sh, tea.Quit
	}

	// Echo to log
	sh.logBuf = append(sh.logBuf, fmt.Sprintf("› %s", line))

	if sh.busy {
		sh.logBuf = append(sh.logBuf, "  (busy — command already running; wait for it to finish)")
		return sh, nil
	}

	if sh.handler == nil {
		sh.logBuf = append(sh.logBuf, "  (no handler registered)")
		return sh, nil
	}

	sh.busy = true
	sh.status = "running..."

	// Run handler in a goroutine so the UI stays responsive
	go func() {
		status := sh.handler(sh, line)
		if sh.program != nil {
			sh.program.Send(shellCommandDoneMsg{status: status})
		}
	}()

	return sh, nil
}

// --- Rendering ---

var (
	shellBorder       = lipgloss.NewStyle().BorderStyle(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("240"))
	shellBannerTitle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("86"))
	shellBannerSub    = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	shellSectionTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	shellSessionDone  = lipgloss.NewStyle().Foreground(lipgloss.Color("35"))
	shellSessionRun   = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	shellSessionFail  = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	shellSessionPend  = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	shellPrompt       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("86"))
	shellStatusStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Italic(true)
	shellBusyStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
)

func (sh *Shell) View() string {
	sh.mu.Lock()
	defer sh.mu.Unlock()

	width := sh.width
	if width < 40 {
		width = 40
	}
	height := sh.height
	if height < 10 {
		height = 10
	}

	var b strings.Builder
	b.WriteString(sh.renderBanner(width))
	b.WriteString("\n")
	b.WriteString(sh.renderSessions(width))
	b.WriteString("\n")

	// Log pane takes remaining space above the input (3 lines for input+status)
	logHeight := height - sh.bannerHeight() - sh.sessionsHeight() - 3
	if logHeight < 3 {
		logHeight = 3
	}
	b.WriteString(sh.renderLog(width, logHeight))
	b.WriteString("\n")
	b.WriteString(sh.renderInput(width))
	return b.String()
}

func (sh *Shell) bannerHeight() int {
	n := 3 // 1 title + 1 sub + 1 gap
	if len(sh.cfg.Notes) > 0 {
		n += len(sh.cfg.Notes)
	}
	return n
}

func (sh *Shell) sessionsHeight() int {
	n := len(sh.sessions) + 2 // title + border
	if n < 4 {
		n = 4
	}
	if n > 10 {
		n = 10
	}
	return n
}

func (sh *Shell) renderBanner(width int) string {
	var b strings.Builder
	title := fmt.Sprintf("⚡ Stoke %s  %s", sh.cfg.Version, truncStr(sh.cfg.RepoRoot, width-20))
	b.WriteString(shellBannerTitle.Render(title) + "\n")

	var parts []string
	if sh.cfg.Runner != "" {
		parts = append(parts, "runner: "+sh.cfg.Runner)
	}
	if sh.cfg.BaseURL != "" {
		parts = append(parts, "base: "+sh.cfg.BaseURL)
	}
	if sh.cfg.Model != "" {
		parts = append(parts, "model: "+sh.cfg.Model)
	}
	if sh.cfg.Supervisor != "" {
		parts = append(parts, "super: "+sh.cfg.Supervisor)
	}
	b.WriteString(shellBannerSub.Render(strings.Join(parts, " · ")))
	for _, note := range sh.cfg.Notes {
		b.WriteString("\n" + shellBannerSub.Render("  "+note))
	}
	return b.String()
}

func (sh *Shell) renderSessions(width int) string {
	var b strings.Builder
	b.WriteString(shellSectionTitle.Render("Sessions") + "\n")
	if len(sh.sessions) == 0 {
		b.WriteString(shellSessionPend.Render("  (no active SOW — type /build-from-scope to start)"))
		return shellBorder.Width(width - 2).Render(b.String())
	}
	// Render up to 8 most recent
	start := 0
	if len(sh.sessions) > 8 {
		start = len(sh.sessions) - 8
	}
	for _, s := range sh.sessions[start:] {
		b.WriteString("  " + renderSessionLine(s, width-4) + "\n")
	}
	return shellBorder.Width(width - 2).Render(b.String())
}

func renderSessionLine(s SessionDisplay, width int) string {
	icon := "○"
	style := shellSessionPend
	switch s.Status {
	case "done":
		icon = "✓"
		style = shellSessionDone
	case "running":
		icon = "▸"
		style = shellSessionRun
	case "failed":
		icon = "✗"
		style = shellSessionFail
	case "skipped":
		icon = "·"
		style = shellSessionPend
	}
	progress := ""
	if s.TasksTotal > 0 {
		progress = fmt.Sprintf(" %d/%d tasks", s.TasksDone, s.TasksTotal)
	}
	if s.CriteriaTotal > 0 {
		progress += fmt.Sprintf(", %d/%d criteria", s.CriteriaDone, s.CriteriaTotal)
	}
	cost := ""
	if s.CostUSD > 0 {
		cost = fmt.Sprintf("  $%.3f", s.CostUSD)
	}
	dur := ""
	if s.DurationSec > 0 {
		dur = fmt.Sprintf("  %.0fs", s.DurationSec)
	}
	line := fmt.Sprintf("%s %s: %s%s%s%s", icon, s.ID, truncStr(s.Title, 40), progress, cost, dur)
	if s.LastError != "" && s.Status == "failed" {
		line += "  " + truncStr(s.LastError, 30)
	}
	return style.Render(truncStr(line, width))
}

func (sh *Shell) renderLog(width, height int) string {
	var b strings.Builder
	b.WriteString(shellSectionTitle.Render("Log") + "\n")
	start := 0
	end := len(sh.logBuf)
	// Scrolling: logScroll > 0 means scrolled up N lines from the tail
	if sh.logScroll > 0 && sh.logScroll < len(sh.logBuf) {
		end = len(sh.logBuf) - sh.logScroll
	}
	available := height - 1
	if available < 1 {
		available = 1
	}
	if end > available {
		start = end - available
	}
	maxLine := width - 4
	if maxLine < 10 {
		maxLine = 10
	}
	for i := start; i < end; i++ {
		b.WriteString(truncStr(sh.logBuf[i], maxLine) + "\n")
	}
	// Fill remaining lines so the log pane is a fixed size
	blanks := available - (end - start)
	for i := 0; i < blanks; i++ {
		b.WriteString("\n")
	}
	return shellBorder.Width(width - 2).Height(height).Render(b.String())
}

func (sh *Shell) renderInput(width int) string {
	prompt := "> "
	if sh.busy {
		prompt = "…  "
	}
	line := shellPrompt.Render(prompt) + sh.input
	if sh.focus == focusInput {
		line += "▎" // cursor indicator
	}
	status := sh.status
	if status == "" {
		if sh.busy {
			status = shellBusyStyle.Render("working — Ctrl+C to cancel")
		} else {
			status = shellStatusStyle.Render("/help · Tab=focus · PgUp/PgDn=scroll · Ctrl+C=quit")
		}
	} else {
		status = shellStatusStyle.Render(status)
	}
	inputLine := truncStr(line, width-2)
	return inputLine + "\n" + status
}
