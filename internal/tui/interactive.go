package tui

import (
	"fmt"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/RelayOne/r1-agent/internal/stream"
	"github.com/RelayOne/r1-agent/internal/viewport"
)

// Mode controls what the TUI displays.
type Mode int

const (
	ModeDashboard Mode = iota // overview of all tasks
	ModeFocus                 // follow one active task's tool use
	ModeDetail                // drill into a completed/failed task
)

// Task lifecycle status strings. Reused across status checks, status
// updates, and rendering so the TUI has one source of truth.
const (
	statusActive = "active"
	statusDone   = "done"
	statusFailed = "failed"
)

// TaskState tracks one task in the TUI.
type TaskState struct {
	ID          string
	Description string
	Status      string // pending, active, done, failed, retrying
	PoolID      string
	Attempt     int
	CostUSD     float64
	DurationSec float64
	LastTool    string
	Error       string
	Verdict     string // Claimed vs Verified (anti-deception display)
	Events      []stream.Event
}

// InteractiveModel is the Bubble Tea model for the Stoke TUI.
type InteractiveModel struct {
	mu          sync.Mutex
	tasks       []TaskState
	taskIndex   map[string]int
	mode        Mode
	focusTask   string
	detailTask  string
	cursor      int
	totalCost   float64
	startedAt   time.Time
	planID      string
	totalTasks  int
	pools       []PoolInfo
	width       int
	height      int
	done        bool
}

// NewInteractiveModel creates the TUI model.
func NewInteractiveModel(planID string, totalTasks int) *InteractiveModel {
	return &InteractiveModel{
		taskIndex:  make(map[string]int),
		startedAt:  time.Now(),
		planID:     planID,
		totalTasks: totalTasks,
		width:      80,
		height:     24,
	}
}

// --- Messages ---

type taskStartMsg struct{ id, desc, pool string }
type taskEventMsg struct {
	id string
	ev stream.Event
}
type taskCompleteMsg struct {
	id      string
	success bool
	cost    float64
	dur     float64
	attempt int
	err     string
	verdict string // Claimed vs Verified display (anti-deception)
}
type poolUpdateMsg struct{ pools []PoolInfo }
type tickMsg time.Time
type doneMsg struct{}

// --- Tea interface ---

func (m *InteractiveModel) Init() tea.Cmd {
	return tickCmd()
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *InteractiveModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "d":
			m.mu.Lock()
			m.mode = ModeDashboard
			m.mu.Unlock()
		case "f":
			m.mu.Lock()
			m.mode = ModeFocus
			if m.focusTask == "" {
				for _, t := range m.tasks {
					if t.Status == statusActive {
						m.focusTask = t.ID
						break
					}
				}
			}
			m.mu.Unlock()
		case "enter":
			m.mu.Lock()
			if m.mode == ModeDashboard && m.cursor < len(m.tasks) {
				m.detailTask = m.tasks[m.cursor].ID
				m.mode = ModeDetail
			}
			m.mu.Unlock()
		case "esc":
			m.mu.Lock()
			m.mode = ModeDashboard
			m.mu.Unlock()
		case "up", "k":
			m.mu.Lock()
			if m.cursor > 0 { m.cursor-- }
			m.mu.Unlock()
		case "down", "j":
			m.mu.Lock()
			if m.cursor < len(m.tasks)-1 { m.cursor++ }
			m.mu.Unlock()
		case "tab":
			m.mu.Lock()
			// Cycle focus to next active task
			for i, t := range m.tasks {
				if t.Status == statusActive && t.ID != m.focusTask {
					m.focusTask = t.ID
					m.cursor = i
					break
				}
			}
			m.mu.Unlock()
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case taskStartMsg:
		m.mu.Lock()
		idx := len(m.tasks)
		m.tasks = append(m.tasks, TaskState{
			ID: msg.id, Description: msg.desc, Status: statusActive,
			PoolID: msg.pool, Attempt: 1,
		})
		m.taskIndex[msg.id] = idx
		if m.focusTask == "" { m.focusTask = msg.id }
		m.mu.Unlock()

	case taskEventMsg:
		m.mu.Lock()
		if idx, ok := m.taskIndex[msg.id]; ok {
			t := &m.tasks[idx]
			t.Events = append(t.Events, msg.ev)
			if len(msg.ev.ToolUses) > 0 {
				t.LastTool = msg.ev.ToolUses[0].Name
			}
		}
		m.mu.Unlock()

	case taskCompleteMsg:
		m.mu.Lock()
		if idx, ok := m.taskIndex[msg.id]; ok {
			t := &m.tasks[idx]
			if msg.success {
				t.Status = statusDone
			} else {
				t.Status = statusFailed
				t.Error = msg.err
			}
			t.CostUSD = msg.cost
			t.DurationSec = msg.dur
			t.Attempt = msg.attempt
			t.Verdict = msg.verdict
			m.totalCost += msg.cost
		}
		m.mu.Unlock()

	case poolUpdateMsg:
		m.mu.Lock()
		m.pools = msg.pools
		m.mu.Unlock()

	case tickMsg:
		return m, tickCmd()

	case doneMsg:
		m.done = true
		return m, tea.Quit
	}

	return m, nil
}

func (m *InteractiveModel) View() string {
	m.mu.Lock()
	defer m.mu.Unlock()

	switch m.mode {
	case ModeFocus:
		return m.viewFocus()
	case ModeDetail:
		return m.viewDetail()
	case ModeDashboard:
		return m.viewDashboard()
	default:
		return m.viewDashboard()
	}
}

// --- Views ---

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("86"))
	activeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	doneStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("35"))
	failStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	barFull     = lipgloss.NewStyle().Foreground(lipgloss.Color("86"))
	barEmpty    = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
)

func (m *InteractiveModel) viewDashboard() string {
	var sb strings.Builder
	elapsed := time.Since(m.startedAt).Round(time.Second)
	done, failed, active := m.counts()

	sb.WriteString(titleStyle.Render(fmt.Sprintf("⚡ STOKE %s", m.planID)) + "\n")
	sb.WriteString(fmt.Sprintf("  %d/%d done  %d failed  %d active  $%.2f  %s\n\n",
		done, m.totalTasks, failed, active, m.totalCost, elapsed))

	// Pool bars
	if len(m.pools) > 0 {
		for _, p := range m.pools {
			sb.WriteString(fmt.Sprintf("  [%s] %s %.0f%%\n", p.ID, renderBar(p.Utilization, 15), p.Utilization))
		}
		sb.WriteString("\n")
	}

	// Task list
	for i, t := range m.tasks {
		cursor := "  "
		if i == m.cursor { cursor = "> " }
		icon, style := taskIcon(t.Status)
		line := fmt.Sprintf("%s%s %s: %s", cursor, icon, t.ID, truncStr(t.Description, 45))
		if t.Status == statusActive && t.LastTool != "" {
			line += dimStyle.Render(fmt.Sprintf(" [%s]", t.LastTool))
		}
		if t.CostUSD > 0 {
			line += dimStyle.Render(fmt.Sprintf(" $%.4f", t.CostUSD))
		}
		sb.WriteString(style.Render(line) + "\n")
	}

	sb.WriteString(dimStyle.Render("\n  d=dashboard  f=focus  enter=detail  tab=next  q=quit"))
	return sb.String()
}

func (m *InteractiveModel) viewFocus() string {
	var sb strings.Builder
	sb.WriteString(titleStyle.Render(fmt.Sprintf("⚡ FOCUS: %s", m.focusTask)) + "\n\n")

	idx, ok := m.taskIndex[m.focusTask]
	if !ok {
		sb.WriteString("  No active task\n")
		return sb.String()
	}
	t := m.tasks[idx]

	sb.WriteString(fmt.Sprintf("  Task: %s\n  Pool: %s  Attempt: %d\n\n", t.Description, t.PoolID, t.Attempt))

	// Show last N events
	start := 0
	maxEvents := m.height - 10
	if maxEvents < 5 { maxEvents = 5 }
	if len(t.Events) > maxEvents { start = len(t.Events) - maxEvents }

	for _, ev := range t.Events[start:] {
		switch ev.Type {
		case "assistant":
			for _, tu := range ev.ToolUses {
				sb.WriteString(activeStyle.Render(fmt.Sprintf("  ⚙ %s %s\n", tu.Name, toolInput(tu))))
			}
		case "user":
			for _, tr := range ev.ToolResults {
				icon := doneStyle.Render("✓")
				if tr.IsError { icon = failStyle.Render("✗") }
				sb.WriteString(fmt.Sprintf("  %s %s\n", icon, truncStr(tr.Content, 70)))
			}
		case "result":
			sb.WriteString(dimStyle.Render(fmt.Sprintf("  → $%.4f, %d turns\n", ev.CostUSD, ev.NumTurns)))
		}
	}

	sb.WriteString(dimStyle.Render("\n  d=dashboard  tab=next  q=quit"))
	return sb.String()
}

func (m *InteractiveModel) viewDetail() string {
	var sb strings.Builder

	idx, ok := m.taskIndex[m.detailTask]
	if !ok {
		sb.WriteString("  Task not found\n")
		return sb.String()
	}
	t := m.tasks[idx]

	icon, style := taskIcon(t.Status)
	sb.WriteString(titleStyle.Render(fmt.Sprintf("⚡ DETAIL: %s %s", icon, t.ID)) + "\n\n")
	sb.WriteString(fmt.Sprintf("  Description: %s\n", t.Description))
	sb.WriteString(fmt.Sprintf("  Status:      %s\n", style.Render(t.Status)))
	sb.WriteString(fmt.Sprintf("  Pool:        %s\n", t.PoolID))
	sb.WriteString(fmt.Sprintf("  Attempt:     %d\n", t.Attempt))
	sb.WriteString(fmt.Sprintf("  Cost:        $%.4f\n", t.CostUSD))
	sb.WriteString(fmt.Sprintf("  Duration:    %.1fs\n", t.DurationSec))
	if t.Error != "" {
		sb.WriteString(failStyle.Render(fmt.Sprintf("\n  Error: %s\n", t.Error)))
	}

	// Claimed vs Verified (anti-deception display)
	if t.Verdict != "" {
		sb.WriteString("\n" + dimStyle.Render("  ─── Claimed vs Verified ───") + "\n")
		for _, line := range strings.Split(t.Verdict, "\n") {
			sb.WriteString("  " + line + "\n")
		}
	}

	// Tool use summary
	toolCounts := map[string]int{}
	for _, ev := range t.Events {
		for _, tu := range ev.ToolUses {
			toolCounts[tu.Name]++
		}
	}
	if len(toolCounts) > 0 {
		sb.WriteString("\n  Tools used:\n")
		for name, count := range toolCounts {
			sb.WriteString(fmt.Sprintf("    %s: %d\n", name, count))
		}
	}

	// Use viewport for constrained error/output display when content is long.
	if t.Error != "" && len(t.Error) > 200 {
		vp := viewport.FromString(t.Error, viewport.Config{Height: 10})
		sb.WriteString("\n  " + dimStyle.Render("─── Error (viewport) ───") + "\n")
		sb.WriteString("  " + strings.ReplaceAll(vp.View(), "\n", "\n  ") + "\n")
	}

	sb.WriteString(dimStyle.Render("\n  esc=back  d=dashboard  q=quit"))
	return sb.String()
}

// --- Helpers ---

func (m *InteractiveModel) counts() (done, failed, active int) {
	for _, t := range m.tasks {
		switch t.Status {
		case statusDone:
			done++
		case statusFailed:
			failed++
		case statusActive:
			active++
		}
	}
	return
}

func taskIcon(status string) (string, lipgloss.Style) {
	switch status {
	case statusDone:
		return "✓", doneStyle
	case statusFailed:
		return "✗", failStyle
	case statusActive:
		return "▸", activeStyle
	default:
		return "○", dimStyle
	}
}

func renderBar(pct float64, w int) string {
	n := int(pct / 100 * float64(w))
	if n > w { n = w }
	if n < 0 { n = 0 }
	return barFull.Render(strings.Repeat("█", n)) + barEmpty.Render(strings.Repeat("░", w-n))
}

func truncStr(s string, n int) string {
	if len(s) <= n { return s }
	if n <= 3 { return s[:n] }
	return s[:n-3] + "..."
}

// --- Public API for main.go to send messages ---

// SendTaskStart sends a task start event to the TUI program.
func SendTaskStart(p *tea.Program, id, desc, pool string) {
	p.Send(taskStartMsg{id: id, desc: desc, pool: pool})
}

// SendTaskEvent sends a stream event to the TUI program.
func SendTaskEvent(p *tea.Program, id string, ev stream.Event) {
	p.Send(taskEventMsg{id: id, ev: ev})
}

// SendTaskComplete sends a task completion event to the TUI program.
func SendTaskComplete(p *tea.Program, id string, success bool, cost, dur float64, attempt int, errStr string, verdict string) {
	p.Send(taskCompleteMsg{id: id, success: success, cost: cost, dur: dur, attempt: attempt, err: errStr, verdict: verdict})
}

// SendPoolUpdate sends pool utilization data to the TUI program.
func SendPoolUpdate(p *tea.Program, pools []PoolInfo) {
	p.Send(poolUpdateMsg{pools: pools})
}

// SendDone tells the TUI the build is complete.
func SendDone(p *tea.Program) {
	p.Send(doneMsg{})
}
