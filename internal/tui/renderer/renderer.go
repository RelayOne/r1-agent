// Package renderer implements specs/tui-renderer.md — a pure-local
// Bubble Tea TUI that consumes the streamjson event stream and renders
// a live session tree, descent ticker, cost gauge, event scroll, and
// HITL modal. It is a passive consumer: it does not own cost math,
// HITL timing, sessionctl transport, or the event bus. It reads typed
// events and mutates a Model that a lipgloss View renders at up to
// 60 FPS.
//
// Scope of this file (spec §Implementation Checklist items 2–6, 12–13):
//   - Event struct + ParseEvent (§Data Models)
//   - Model, SessionNode, TaskNode, ACNode, DescentTick (§Data Models)
//   - Ring buffer policy (events cap 64, descent cap 8)
//   - Update logic for every subtype in §Event → UI component mapping
//   - View rendering: header, session tree, descent ticker, events
//     scroll, status bar, HITL modal, compact single-line mode
//   - Monochrome fallback (§Icon legend) + ASCII glyph map
//   - Key bindings (§Key bindings)
//
// Deliberately deferred to later commits (one-commit constraint):
//   - streamjson.TwoLane.Tee() helper (spec item 1)
//   - run.go + tea.NewProgram wiring (spec item 7)
//   - cmd/r1 flag wiring --tui/--no-tui (spec item 8)
//   - sessionctl client goroutine dispatch (spec item 9)
//   - interactive.go thin-wrapper refactor (spec item 10)
//   - integration tests with fake PTY (spec item 15)
//
// The renderer.Model here is self-contained and exercised directly by
// the accompanying tests, which prove the headline acceptance criteria
// for local state transitions, ring-buffer policy, HITL modal flow,
// unknown-subtype forward-compat, small-terminal compact mode, and
// NO_COLOR monochrome rendering.
package renderer

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ---------------------------------------------------------------------
// Event model (§Data Models → renderer.Event)
// ---------------------------------------------------------------------

// Event is the normalized form the Update loop consumes. JSON parsing
// happens once at tee ingest so the render path never touches maps.
type Event struct {
	Type      string
	Subtype   string
	SessionID string
	TaskID    string
	ACID      string
	Tier      string
	Category  string
	Attempt   int
	Verdict   string
	Reason    string
	File      string
	Title     string
	CostUSD   float64
	Ts        time.Time
	Raw       map[string]any
}

// ParseEvent converts an emitter payload to a typed Event. Unknown
// fields are preserved in Raw; unknown subtypes still parse cleanly so
// they can land in the event scroll without tree/gauge side-effects.
func ParseEvent(m map[string]any) (Event, bool) {
	if m == nil {
		return Event{}, false
	}
	e := Event{Raw: m}
	e.Type, _ = m["type"].(string)
	if e.Type == "" {
		return e, false
	}
	e.Subtype, _ = m["subtype"].(string)

	// _stoke.dev/* namespace carries the structured fields we drive UI
	// from. We also accept flat top-level keys for tests / forward compat.
	get := func(key, nsKey string) string {
		if v, ok := m[nsKey].(string); ok && v != "" {
			return v
		}
		if v, ok := m[key].(string); ok {
			return v
		}
		return ""
	}
	e.SessionID = get("session", "_stoke.dev/session")
	if e.SessionID == "" {
		e.SessionID, _ = m["session_id"].(string)
	}
	e.TaskID = get("task_id", "_stoke.dev/task_id")
	e.ACID = get("ac_id", "_stoke.dev/ac_id")
	e.Tier = get("tier", "_stoke.dev/tier")
	e.Category = get("category", "_stoke.dev/category")
	e.Verdict = get("verdict", "_stoke.dev/verdict")
	e.Reason = get("reason", "_stoke.dev/reason")
	e.File = get("file", "_stoke.dev/file")
	e.Title = get("title", "_stoke.dev/title")

	if v, ok := m["_stoke.dev/attempt"].(int); ok {
		e.Attempt = v
	} else if v, ok := m["_stoke.dev/attempt"].(float64); ok {
		e.Attempt = int(v)
	} else if v, ok := m["attempt"].(int); ok {
		e.Attempt = v
	} else if v, ok := m["attempt"].(float64); ok {
		e.Attempt = int(v)
	}

	if v, ok := m["_stoke.dev/cost_usd"].(float64); ok {
		e.CostUSD = v
	} else if v, ok := m["total_cost_usd"].(float64); ok {
		e.CostUSD = v
	}

	if ts, ok := m["ts"].(string); ok {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			e.Ts = t
		}
	}
	if e.Ts.IsZero() {
		e.Ts = time.Now()
	}
	return e, true
}

// ---------------------------------------------------------------------
// Tree model (§Data Models → Model / SessionNode / TaskNode / ACNode)
// ---------------------------------------------------------------------

type Status int

const (
	StatusPending Status = iota
	StatusRunning
	StatusDone
	StatusFailed
	StatusBlocked
)

type ACStatus int

const (
	ACPending ACStatus = iota
	ACInDescent
	ACPass
	ACFail
	ACSoftPass
)

// Verdict string aliases recognised across session.complete, task.complete,
// ac.result and descent.resolve events.
const (
	verdictSoftpass         = "softpass"
	verdictSoftpassDashed   = "soft-pass"
	verdictSoftpassSnake    = "soft_pass"
)

type SessionNode struct {
	ID, Title, Reason                string
	Status                           Status
	Attempt, MaxAttempts, SoftPasses int
	Tasks                            []TaskNode
	StartedAt, FinishedAt            time.Time
}

type TaskNode struct {
	ID, Title, FocusKey string
	Status              Status
	Attempt             int
	ACs                 []ACNode
}

type ACNode struct {
	ID, Title, Verdict, Tier string
	Status                   ACStatus
	AttemptN, AttemptMax     int
}

type DescentTick struct {
	Ts                                        time.Time
	FromTier, ToTier, Label, Category, Detail string
	DurMs                                     int64
}

// HITLRequest is the payload we mirror into the modal. Derived from
// the top-level `hitl_required` event; timeout is owned by spec-2.
type HITLRequest struct {
	AskID    string
	Reason   string
	ACID     string
	Tier     string
	Category string
	Evidence string
	Raw      map[string]any
}

// Model is the Bubble Tea model. Mutations go through the Update loop
// (tea.Msg dispatch) and must take mu. The View method is read-only
// under the same mutex. Ring buffer sizes are spec-mandated.
type Model struct {
	mu sync.Mutex

	sessions               []SessionNode
	sessionIdx             map[string]int
	order                  []string
	focusSession, focusTask int

	costSpent, costBudget float64
	costByKind            map[string]float64

	descent     []DescentTick // ring cap 8
	events      []Event       // ring cap 64
	eventOffset int
	dropped     uint64

	hitlOpen   bool
	hitlReq    HITLRequest
	hitlChoice int // 0=Approve 1=Reject 2=See full
	hitlQueue  []HITLRequest
	hitlToast  string

	width, height         int
	tooSmall              bool
	monochrome            bool
	helpOpen, statusOpen  bool
	done                  bool
	quitReason            string
	title                 string

	maxWorkers int
}

const (
	ringEventsCap  = 64
	ringDescentCap = 8
	minWidth       = 80
	minHeight      = 20
)

// NewModel constructs an initialized Model. Title is shown in the
// header bar. Width/height default to the 80×20 minimum; the first
// tea.WindowSizeMsg will correct it.
func NewModel(title string) *Model {
	return &Model{
		title:      title,
		sessionIdx: map[string]int{},
		costByKind: map[string]float64{},
		width:      minWidth,
		height:     minHeight,
		monochrome: monoFromEnv(),
	}
}

// monoFromEnv consults NO_COLOR — if set to anything non-empty, we
// disable colors entirely per the spec's monochrome fallback.
func monoFromEnv() bool {
	return strings.TrimSpace(os.Getenv("NO_COLOR")) != ""
}

// SetSize applies a new terminal size and recomputes tooSmall. Exposed
// for tests that bypass the Bubble Tea program loop.
func (m *Model) SetSize(w, h int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.width = w
	m.height = h
	m.tooSmall = w < minWidth || h < minHeight
}

// SetMonochrome forces the color-free rendering path. NO_COLOR=1 at
// construction time also triggers this.
func (m *Model) SetMonochrome(on bool) {
	m.mu.Lock()
	m.monochrome = on
	m.mu.Unlock()
}

// SetBudget seeds the cost gauge's ceiling for the header bar.
func (m *Model) SetBudget(usd float64) {
	m.mu.Lock()
	m.costBudget = usd
	m.mu.Unlock()
}

// Snapshot copies the state the tests assert against. Caller owns the
// returned slices.
type Snapshot struct {
	Sessions   []SessionNode
	Descent    []DescentTick
	Events     []Event
	CostSpent  float64
	CostBudget float64
	HITLOpen   bool
	HITLChoice int
	HITLToast  string
	Dropped    uint64
	TooSmall   bool
	Monochrome bool
}

func (m *Model) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := Snapshot{
		CostSpent:  m.costSpent,
		CostBudget: m.costBudget,
		HITLOpen:   m.hitlOpen,
		HITLChoice: m.hitlChoice,
		HITLToast:  m.hitlToast,
		Dropped:    m.dropped,
		TooSmall:   m.tooSmall,
		Monochrome: m.monochrome,
	}
	s.Sessions = append(s.Sessions, m.sessions...)
	s.Descent = append(s.Descent, m.descent...)
	s.Events = append(s.Events, m.events...)
	return s
}

// ---------------------------------------------------------------------
// Bubble Tea plumbing
// ---------------------------------------------------------------------

// eventMsg delivers a parsed Event through the tea runtime.
type eventMsg struct{ ev Event }

// toastMsg shows an ephemeral toast line (e.g. "session control
// unavailable"). Cleared on next key press.
type toastMsg struct{ text string }

// EventCmd returns a tea.Cmd that delivers the given event to a running
// program. Consumers that bridge a chan<-Event to Bubble Tea can use
// program.Send(eventMsg{ev}) directly instead.
func EventCmd(ev Event) tea.Cmd {
	return func() tea.Msg { return eventMsg{ev: ev} }
}

func (m *Model) Init() tea.Cmd { return nil }

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.KeyMsg:
		return m.handleKey(v)
	case tea.WindowSizeMsg:
		m.mu.Lock()
		m.width = v.Width
		m.height = v.Height
		m.tooSmall = v.Width < minWidth || v.Height < minHeight
		m.mu.Unlock()
		return m, nil
	case eventMsg:
		m.applyEvent(v.ev)
		return m, nil
	case toastMsg:
		m.mu.Lock()
		m.hitlToast = v.text
		m.mu.Unlock()
		return m, nil
	}
	return m, nil
}

// Apply is the test entry point: a direct, synchronous mutation of the
// model by a single event. Mirrors what Update would do for eventMsg.
func (m *Model) Apply(ev Event) {
	m.applyEvent(ev)
}

// handleKey implements §Key bindings. sessionctl-backed actions emit a
// toast when the session socket is not wired through — the renderer is
// nil-safe and the parent caller threads the client in later.
func (m *Model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Modal keys take precedence.
	if m.hitlOpen {
		switch k.String() {
		case "left":
			if m.hitlChoice > 0 {
				m.hitlChoice--
			}
		case "right":
			if m.hitlChoice < 2 {
				m.hitlChoice++
			}
		case "enter":
			m.closeHITL()
		case "esc", "ctrl+c":
			m.closeHITL()
		}
		return m, nil
	}

	switch k.String() {
	case "q", "ctrl+c":
		m.done = true
		m.quitReason = "user"
		return m, tea.Quit
	case "?":
		m.helpOpen = !m.helpOpen
	case "esc":
		m.helpOpen = false
		m.statusOpen = false
	case "s":
		m.statusOpen = !m.statusOpen
	case "tab":
		m.cycleFocus(+1)
	case "shift+tab":
		m.cycleFocus(-1)
	case "up", "k":
		if m.eventOffset < len(m.events)-1 {
			m.eventOffset++
		}
	case "down", "j":
		if m.eventOffset > 0 {
			m.eventOffset--
		}
	case "a":
		// Approve oldest pending hitl (spec §Key bindings 'a' in main mode).
		m.hitlToast = "session control unavailable"
	case "p", "r":
		m.hitlToast = "session control unavailable"
	}
	return m, nil
}

func (m *Model) cycleFocus(delta int) {
	if len(m.sessions) == 0 {
		return
	}
	n := len(m.sessions)
	m.focusSession = (m.focusSession + delta + n) % n
	m.focusTask = 0
}

func (m *Model) closeHITL() {
	m.hitlOpen = false
	m.hitlChoice = 0
	m.hitlReq = HITLRequest{}
	if len(m.hitlQueue) > 0 {
		m.hitlReq = m.hitlQueue[0]
		m.hitlQueue = m.hitlQueue[1:]
		m.hitlOpen = true
	}
}

// ---------------------------------------------------------------------
// Event → model mutation (§Event → UI component mapping)
// ---------------------------------------------------------------------

func (m *Model) applyEvent(ev Event) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Top-level events first.
	switch ev.Type {
	case "hitl_required":
		req := HITLRequest{
			AskID:    stringFrom(ev.Raw, "ask_id", "_stoke.dev/ask_id"),
			Reason:   ev.Reason,
			ACID:     ev.ACID,
			Tier:     ev.Tier,
			Category: ev.Category,
			Evidence: stringFrom(ev.Raw, "evidence", "_stoke.dev/evidence"),
			Raw:      ev.Raw,
		}
		if m.hitlOpen {
			m.hitlQueue = append(m.hitlQueue, req)
		} else {
			m.hitlReq = req
			m.hitlOpen = true
			m.hitlChoice = 0
		}
		m.pushEvent(ev)
		return
	case "complete":
		m.done = true
		m.quitReason = "stream_eof"
		m.pushEvent(ev)
		return
	case "error":
		m.pushEvent(ev)
		return
	case "mission.aborted":
		m.done = true
		m.pushEvent(ev)
		return
	}

	// system/* subtypes.
	switch ev.Subtype {
	case "session.start":
		m.ensureSession(ev.SessionID, ev.Title)
		if len(m.sessions) == 1 {
			m.focusSession = 0
		}
	case "session.complete":
		if idx, ok := m.sessionIdx[ev.SessionID]; ok {
			s := &m.sessions[idx]
			s.FinishedAt = ev.Ts
			switch ev.Verdict {
			case "pass", "done":
				s.Status = StatusDone
			case verdictSoftpass, verdictSoftpassDashed, verdictSoftpassSnake:
				s.Status = StatusDone
				s.SoftPasses++
			default:
				s.Status = StatusFailed
				s.Reason = ev.Reason
			}
		}
	case "plan.ready":
		// Forward-compat preload hook. Tests assert no crash.
	case "task.dispatch":
		s := m.ensureSession(ev.SessionID, "")
		t := m.ensureTask(s, ev.TaskID, ev.Title)
		t.Status = StatusRunning
		if ev.Attempt > 0 {
			t.Attempt = ev.Attempt
		} else {
			t.Attempt++
		}
		m.writeTask(ev.SessionID, t)
	case "task.complete":
		if t := m.getTask(ev.SessionID, ev.TaskID); t != nil {
			switch ev.Verdict {
			case "pass", "done":
				t.Status = StatusDone
			default:
				t.Status = StatusFailed
			}
			m.writeTask(ev.SessionID, t)
		}
	case "ac.result":
		if ac := m.getAC(ev.SessionID, ev.TaskID, ev.ACID); ac != nil {
			switch ev.Verdict {
			case "pass":
				ac.Status = ACPass
			case verdictSoftpass, verdictSoftpassDashed, verdictSoftpassSnake:
				ac.Status = ACSoftPass
			case "fail":
				ac.Status = ACFail
			}
			ac.Verdict = ev.Verdict
			m.writeAC(ev.SessionID, ev.TaskID, ac)
		} else {
			// Synthesize AC node so late-binding events still render.
			ac := &ACNode{ID: ev.ACID, Title: ev.Title, Verdict: ev.Verdict}
			switch ev.Verdict {
			case "pass":
				ac.Status = ACPass
			case verdictSoftpass, verdictSoftpassDashed, verdictSoftpassSnake:
				ac.Status = ACSoftPass
			case "fail":
				ac.Status = ACFail
			}
			s := m.ensureSession(ev.SessionID, "")
			t := m.ensureTask(s, ev.TaskID, "")
			t.ACs = append(t.ACs, *ac)
			m.writeTask(ev.SessionID, t)
		}
	case "descent.start":
		if ac := m.getAC(ev.SessionID, ev.TaskID, ev.ACID); ac != nil {
			ac.Status = ACInDescent
			ac.Tier = "T1"
			ac.AttemptN = 0
			m.writeAC(ev.SessionID, ev.TaskID, ac)
		}
	case "descent.tier":
		m.pushDescent(DescentTick{
			Ts:       ev.Ts,
			FromTier: stringFrom(ev.Raw, "from_tier", "_stoke.dev/from_tier"),
			ToTier:   ev.Tier,
			Label:    ev.Reason,
		})
		if ac := m.getAC(ev.SessionID, ev.TaskID, ev.ACID); ac != nil {
			ac.Tier = ev.Tier
			if ev.Attempt > 0 {
				ac.AttemptN = ev.Attempt
			}
			m.writeAC(ev.SessionID, ev.TaskID, ac)
		}
	case "descent.classify":
		if ac := m.getAC(ev.SessionID, ev.TaskID, ev.ACID); ac != nil {
			ac.Verdict = fmt.Sprintf("classify: %s (%s)", ev.Category, ev.Reason)
			m.writeAC(ev.SessionID, ev.TaskID, ac)
		}
	case "descent.resolve":
		if ac := m.getAC(ev.SessionID, ev.TaskID, ev.ACID); ac != nil {
			switch ev.Verdict {
			case "pass":
				ac.Status = ACPass
			case verdictSoftpass, verdictSoftpassDashed, verdictSoftpassSnake:
				ac.Status = ACSoftPass
			case "fail":
				ac.Status = ACFail
			}
			m.writeAC(ev.SessionID, ev.TaskID, ac)
		}
	case "cost.update":
		m.costSpent = ev.CostUSD
		if kind := stringFrom(ev.Raw, "kind", "_stoke.dev/kind"); kind != "" {
			m.costByKind[kind] = ev.CostUSD
		}
	case "concurrency.cap":
		if v, ok := ev.Raw["_stoke.dev/max_workers"].(float64); ok {
			m.maxWorkers = int(v)
		} else if v, ok := ev.Raw["max_workers"].(float64); ok {
			m.maxWorkers = int(v)
		}
	case "stream.dropped":
		if v, ok := ev.Raw["_stoke.dev/count"].(float64); ok {
			m.dropped += uint64(v)
		} else if v, ok := ev.Raw["count"].(float64); ok {
			m.dropped += uint64(v)
		}
	case "hitl.timeout":
		m.closeHITL()
	case "progress":
		// progress.md renderer owns this; intentional no-op per §Boundaries.
		return
	}

	// Every known-or-unknown subtype also lands in the scroll for
	// forward-compat (§Event → UI component mapping / last column).
	m.pushEvent(ev)
}

func (m *Model) ensureSession(id, title string) *SessionNode {
	if id == "" {
		id = "(unknown)"
	}
	if idx, ok := m.sessionIdx[id]; ok {
		if title != "" && m.sessions[idx].Title == "" {
			m.sessions[idx].Title = title
		}
		return &m.sessions[idx]
	}
	n := SessionNode{
		ID:        id,
		Title:     title,
		Status:    StatusRunning,
		StartedAt: time.Now(),
	}
	m.sessions = append(m.sessions, n)
	m.sessionIdx[id] = len(m.sessions) - 1
	m.order = append(m.order, id)
	return &m.sessions[len(m.sessions)-1]
}

func (m *Model) ensureTask(s *SessionNode, id, title string) *TaskNode {
	if id == "" {
		id = "(unknown-task)"
	}
	for i := range s.Tasks {
		if s.Tasks[i].ID == id {
			if title != "" && s.Tasks[i].Title == "" {
				s.Tasks[i].Title = title
			}
			t := s.Tasks[i]
			return &t
		}
	}
	t := TaskNode{ID: id, Title: title, Status: StatusRunning}
	s.Tasks = append(s.Tasks, t)
	return &s.Tasks[len(s.Tasks)-1]
}

func (m *Model) writeTask(sessID string, t *TaskNode) {
	idx, ok := m.sessionIdx[sessID]
	if !ok {
		return
	}
	s := &m.sessions[idx]
	for i := range s.Tasks {
		if s.Tasks[i].ID == t.ID {
			s.Tasks[i] = *t
			return
		}
	}
	s.Tasks = append(s.Tasks, *t)
}

func (m *Model) getTask(sessID, taskID string) *TaskNode {
	idx, ok := m.sessionIdx[sessID]
	if !ok {
		return nil
	}
	s := &m.sessions[idx]
	for i := range s.Tasks {
		if s.Tasks[i].ID == taskID {
			t := s.Tasks[i]
			return &t
		}
	}
	return nil
}

func (m *Model) getAC(sessID, taskID, acID string) *ACNode {
	idx, ok := m.sessionIdx[sessID]
	if !ok {
		return nil
	}
	s := &m.sessions[idx]
	for i := range s.Tasks {
		if s.Tasks[i].ID != taskID {
			continue
		}
		for j := range s.Tasks[i].ACs {
			if s.Tasks[i].ACs[j].ID == acID {
				ac := s.Tasks[i].ACs[j]
				return &ac
			}
		}
	}
	return nil
}

func (m *Model) writeAC(sessID, taskID string, ac *ACNode) {
	idx, ok := m.sessionIdx[sessID]
	if !ok {
		return
	}
	s := &m.sessions[idx]
	for i := range s.Tasks {
		if s.Tasks[i].ID != taskID {
			continue
		}
		for j := range s.Tasks[i].ACs {
			if s.Tasks[i].ACs[j].ID == ac.ID {
				s.Tasks[i].ACs[j] = *ac
				return
			}
		}
		s.Tasks[i].ACs = append(s.Tasks[i].ACs, *ac)
		return
	}
}

func (m *Model) pushEvent(ev Event) {
	m.events = append(m.events, ev)
	if len(m.events) > ringEventsCap {
		m.events = m.events[len(m.events)-ringEventsCap:]
	}
}

func (m *Model) pushDescent(t DescentTick) {
	m.descent = append(m.descent, t)
	if len(m.descent) > ringDescentCap {
		m.descent = m.descent[len(m.descent)-ringDescentCap:]
	}
}

// stringFrom picks the first non-empty string value from a series of
// keys on a raw event map. Used to handle both flat and _stoke.dev/
// namespaced wire formats.
func stringFrom(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// ---------------------------------------------------------------------
// View rendering (§Layout + §Icon legend + compact mode)
// ---------------------------------------------------------------------

// Glyph returns the icon for a status, honoring monochrome mode per
// §Icon legend. Exposed for tests.
func Glyph(mono bool, run, pending, done, failed, softpass, descent bool) string {
	switch {
	case descent:
		if mono {
			return "*"
		}
		return "✦"
	case softpass:
		if mono {
			return "~"
		}
		return "⚖"
	case done:
		if mono {
			return "v"
		}
		return "✓"
	case failed:
		if mono {
			return "x"
		}
		return "✗"
	case run:
		if mono {
			return ">"
		}
		return "▶"
	case pending:
		if mono {
			return "o"
		}
		return "○"
	}
	return " "
}

// glyphFor produces the status icon for a session/task row.
func glyphFor(status Status, mono bool) string {
	switch status {
	case StatusDone:
		return Glyph(mono, false, false, true, false, false, false)
	case StatusFailed:
		return Glyph(mono, false, false, false, true, false, false)
	case StatusRunning:
		return Glyph(mono, true, false, false, false, false, false)
	case StatusBlocked, StatusPending:
		return Glyph(mono, false, true, false, false, false, false)
	}
	return " "
}

// glyphForAC produces the AC icon.
func glyphForAC(status ACStatus, mono bool) string {
	switch status {
	case ACPass:
		return Glyph(mono, false, false, true, false, false, false)
	case ACFail:
		return Glyph(mono, false, false, false, true, false, false)
	case ACSoftPass:
		return Glyph(mono, false, false, false, false, true, false)
	case ACInDescent:
		return Glyph(mono, false, false, false, false, false, true)
	case ACPending:
		return Glyph(mono, false, true, false, false, false, false)
	}
	return Glyph(mono, false, true, false, false, false, false)
}

// progressBar returns e.g. `▰▰▰▱▱` or `###..` depending on mono.
func progressBar(done, total, width int, mono bool) string {
	if width <= 0 || total <= 0 {
		return ""
	}
	filled := done * width / total
	if filled > width {
		filled = width
	}
	full, empty := "▰", "▱"
	if mono {
		full, empty = "#", "."
	}
	return strings.Repeat(full, filled) + strings.Repeat(empty, width-filled)
}

// View is the top-level Bubble Tea render function.
func (m *Model) View() string {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.tooSmall {
		return m.renderCompactLocked()
	}
	if m.hitlOpen {
		return m.renderMainLocked() + "\n" + m.renderHITLModalLocked()
	}
	return m.renderMainLocked()
}

// renderCompactLocked is §Layout compact mode — single-line header
// overwritten in place with \r. Caller must hold mu.
func (m *Model) renderCompactLocked() string {
	var focusID, focusTier string
	var ACsDone, ACsTotal int
	if len(m.sessions) > 0 && m.focusSession < len(m.sessions) {
		s := m.sessions[m.focusSession]
		focusID = s.ID
		for _, t := range s.Tasks {
			for _, ac := range t.ACs {
				ACsTotal++
				if ac.Status == ACPass || ac.Status == ACSoftPass {
					ACsDone++
				}
				if ac.Status == ACInDescent && focusTier == "" {
					focusTier = ac.Tier
				}
			}
		}
	}
	tier := focusTier
	if tier == "" {
		tier = "T1"
	}
	return fmt.Sprintf("\r%s %s %s %d/%d ACs $%.2f/$%.2f",
		m.title, focusID, tier, ACsDone, ACsTotal, m.costSpent, m.costBudget)
}

// CompactLine renders the compact one-liner for tests.
func (m *Model) CompactLine() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.renderCompactLocked()
}

// renderMainLocked draws the 80×20+ main layout. Caller holds mu.
func (m *Model) renderMainLocked() string {
	var b strings.Builder
	b.WriteString(m.renderHeaderLocked())
	b.WriteString("\n")
	b.WriteString(m.renderSessionTreeLocked())
	b.WriteString("\n")
	b.WriteString(m.renderDescentTickerLocked())
	b.WriteString("\n")
	b.WriteString(m.renderEventsScrollLocked())
	b.WriteString("\n")
	b.WriteString(m.renderStatusBarLocked())
	if m.helpOpen {
		b.WriteString("\n")
		b.WriteString(m.renderHelpLocked())
	}
	return b.String()
}

func (m *Model) renderHeaderLocked() string {
	cost := fmt.Sprintf("$%.2f / $%.2f", m.costSpent, m.costBudget)
	left := fmt.Sprintf("stoke %s", m.title)
	right := cost
	if m.dropped > 0 {
		right = fmt.Sprintf("(dropped %d) %s", m.dropped, cost)
	}
	if m.maxWorkers > 0 {
		right = fmt.Sprintf("max-workers=%d  %s", m.maxWorkers, right)
	}
	line := left + "  " + right
	if m.monochrome {
		return line
	}
	return lipgloss.NewStyle().Bold(true).Render(line)
}

func (m *Model) renderSessionTreeLocked() string {
	if len(m.sessions) == 0 {
		return "(no sessions yet)"
	}
	// Focused running sessions first, then pending/blocked, then done.
	order := make([]int, len(m.sessions))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		ra := rankSession(m.sessions[order[a]].Status)
		rb := rankSession(m.sessions[order[b]].Status)
		return ra < rb
	})
	var b strings.Builder
	for _, i := range order {
		s := m.sessions[i]
		g := glyphFor(s.Status, m.monochrome)
		focused := i == m.focusSession
		var prefix string
		if focused {
			prefix = g + " "
		} else {
			prefix = "  " + g + " "
		}
		done, total := countACs(s)
		bar := progressBar(done, total, 8, m.monochrome)
		line := fmt.Sprintf("%s%s  [%s]  %d/%d ACs  attempt %d", prefix, s.ID, bar, done, total, s.Attempt)
		if s.Title != "" {
			line = fmt.Sprintf("%s%s %s  [%s]  %d/%d ACs  attempt %d",
				prefix, s.ID, s.Title, bar, done, total, s.Attempt)
		}
		b.WriteString(line)
		b.WriteString("\n")
		if focused {
			for _, t := range s.Tasks {
				for _, ac := range t.ACs {
					ag := glyphForAC(ac.Status, m.monochrome)
					b.WriteString(fmt.Sprintf("    %s %s %s %s\n", ag, ac.ID, ac.Title, ac.Verdict))
				}
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *Model) renderDescentTickerLocked() string {
	var b strings.Builder
	b.WriteString("descent ticker:\n")
	if len(m.descent) == 0 {
		b.WriteString("  (idle)")
		return b.String()
	}
	for _, t := range m.descent {
		b.WriteString(fmt.Sprintf("  %s→%s  %s  %s\n",
			t.FromTier, t.ToTier, t.Label, t.Detail))
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *Model) renderEventsScrollLocked() string {
	var b strings.Builder
	b.WriteString("last events:\n")
	if len(m.events) == 0 {
		b.WriteString("  (none)")
		return b.String()
	}
	// Show last 5 visible, adjusted by eventOffset.
	show := 5
	end := len(m.events) - m.eventOffset
	if end > len(m.events) {
		end = len(m.events)
	}
	start := end - show
	if start < 0 {
		start = 0
	}
	for _, e := range m.events[start:end] {
		ts := e.Ts.Format("15:04:05")
		label := e.Subtype
		if label == "" {
			label = e.Type
		}
		b.WriteString(fmt.Sprintf("  %s  %s\n", ts, label))
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *Model) renderStatusBarLocked() string {
	line := "[p]ause  [a]pprove  [s]tatus  [?]help  [q]uit"
	if m.hitlToast != "" {
		line = line + "  — " + m.hitlToast
	}
	return line
}

func (m *Model) renderHelpLocked() string {
	return strings.Join([]string{
		"  help:",
		"    p — pause focused session",
		"    r — resume focused session",
		"    a — approve pending HITL",
		"    s — status pane",
		"    tab / shift+tab — cycle focus",
		"    ↑/k ↓/j — scroll events",
		"    enter — detail view",
		"    esc — back",
		"    q / ctrl+c — quit",
	}, "\n")
}

func (m *Model) renderHITLModalLocked() string {
	choice := func(i int, label string) string {
		if m.hitlChoice == i {
			return "[" + label + "]"
		}
		return " " + label + " "
	}
	lines := []string{
		"APPROVAL REQUIRED",
		"Reason: " + m.hitlReq.Reason,
		"AC:     " + m.hitlReq.ACID,
		"Tier:   " + m.hitlReq.Tier + "  Category: " + m.hitlReq.Category,
		"Evidence: " + m.hitlReq.Evidence,
		fmt.Sprintf("%s  %s  %s",
			choice(0, "Approve"), choice(1, "Reject"), choice(2, "See full descent")),
	}
	return strings.Join(lines, "\n")
}

// ---------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------

func rankSession(s Status) int {
	switch s {
	case StatusRunning:
		return 0
	case StatusPending, StatusBlocked:
		return 1
	case StatusDone:
		return 2
	case StatusFailed:
		return 3
	}
	return 4
}

func countACs(s SessionNode) (done, total int) {
	for _, t := range s.Tasks {
		for _, ac := range t.ACs {
			total++
			if ac.Status == ACPass || ac.Status == ACSoftPass {
				done++
			}
		}
	}
	return
}
