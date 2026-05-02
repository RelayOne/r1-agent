// Wire-up: see cmd/r1/main.go
//
// The integration that constructs a ProgressRenderer, subscribes it to the
// event bus alongside the existing streamjson.Emitter, and plumbs stderr in
// `stoke ship` lives in cmd/r1/main.go and is intentionally deferred to a
// follow-up commit (Track B task 13, S-1 pairs with the ship-command wiring
// task). This file only implements the renderer itself plus the Subscribe
// helper; it has no side effects until Subscribe is called.

package tui

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/RelayOne/r1/internal/hub"
)

// Stoke lifecycle event types. These mirror the subtypes the streamjson
// emitter publishes under `_stoke.dev/*`; when the bus carries the same
// events as hub.EventType values, ProgressRenderer consumes them here.
//
// The names intentionally match docs/stoke-protocol.md so a single map of
// names is authoritative across producer (streamjson) and consumer
// (ProgressRenderer).
const (
	// Canonical r1.* event types (D-032). These are the primary names
	// going forward; the legacy stoke.* aliases below remain for the
	// 60-day dual-emit window (until 2026-06-25).
	EventR1PlanReady      hub.EventType = "r1.plan.ready"
	EventR1SessionStart   hub.EventType = "r1.session.start"
	EventR1SessionEnd     hub.EventType = "r1.session.end"
	EventR1TaskStart      hub.EventType = "r1.task.start"
	EventR1TaskEnd        hub.EventType = "r1.task.end"
	EventR1ACResult       hub.EventType = "r1.ac.result"
	EventR1DescentStart   hub.EventType = "r1.descent.start"
	EventR1DescentTier    hub.EventType = "r1.descent.tier"
	EventR1DescentResolve hub.EventType = "r1.descent.resolve"
	EventR1Cost           hub.EventType = "r1.cost"

	// Legacy stoke.* aliases — dual-emitted alongside r1.* for the
	// 60-day window (D-032). Deprecated; remove after 2026-06-25.
	EventStokePlanReady      hub.EventType = "stoke.plan.ready"
	EventStokeSessionStart   hub.EventType = "stoke.session.start"
	EventStokeSessionEnd     hub.EventType = "stoke.session.end"
	EventStokeTaskStart      hub.EventType = "stoke.task.start"
	EventStokeTaskEnd        hub.EventType = "stoke.task.end"
	EventStokeACResult       hub.EventType = "stoke.ac.result"
	EventStokeDescentStart   hub.EventType = "stoke.descent.start"
	EventStokeDescentTier    hub.EventType = "stoke.descent.tier"
	EventStokeDescentResolve hub.EventType = "stoke.descent.resolve"
	EventStokeCost           hub.EventType = "stoke.cost"
)

// sessionStatus is a coarse state of a single session in the plan.
type sessionStatus int

const (
	sessionPending sessionStatus = iota
	sessionRunning
	sessionDone
	sessionFailed
	sessionBlocked
)

// acStatus tracks the state of one acceptance criterion under a session.
type acStatus int

const (
	acPending acStatus = iota
	acPass
	acFail
	acSoftPass
	acInDescent
)

// acState is per-AC display state.
type acState struct {
	id          string
	title       string
	status      acStatus
	tier        string // "T2".."T8" when in descent
	category    string // classify category, e.g. "env", "code_bug"
	repairN     int
	repairMax   int
	reason      string
}

// sessionState is per-session display state.
type sessionState struct {
	id            string
	title         string
	status        sessionStatus
	totalACs      int
	passedACs     int
	currentTask   string
	currentTaskID string
	cost          float64
	startedAt     time.Time
	endedAt       time.Time
	order         int // insertion order for deterministic rendering

	acs      map[string]*acState
	acOrder  []string
}

// ProgressRenderer consumes stoke.* lifecycle events and paints a
// multi-line terminal view to an io.Writer (typically stderr). It is a
// pure consumer -- never calls an LLM, never writes to stdout, and can be
// safely run concurrent with a streamjson.Emitter feeding NDJSON to a
// CloudSwarm subscriber.
//
// Design:
//   - one ProgressRenderer per SOW invocation
//   - thread-safe: the hub may dispatch events from multiple goroutines
//   - redraw-on-event: each event mutates state then triggers redraw
//   - ANSI cursor-up to overwrite the prior frame when isTTY;
//     fall back to plain-text one-line-per-event when not a TTY
//   - zero animation when isTTY=false (honors NO_COLOR and non-interactive
//     pipelines; the caller decides)
type ProgressRenderer struct {
	w     io.Writer
	isTTY bool

	mu                sync.Mutex
	title             string
	sessions          map[string]*sessionState
	sessionOrder      []string
	totalSessions     int
	completedSessions int
	spent             float64
	budget            float64

	// ANSI redraw bookkeeping: number of lines painted on the last
	// interactive frame. On next redraw we emit ESC[<N>A to move back up
	// and ESC[K to clear each line before writing fresh content.
	lastFrameLines int

	// optional clock override for tests (nil = time.Now).
	now func() time.Time
}

// New constructs a renderer writing to w. isTTY lets tests force the
// interactive or plain-text code path.
func New(w io.Writer, isTTY bool, budgetUSD float64) *ProgressRenderer {
	return &ProgressRenderer{
		w:            w,
		isTTY:        isTTY,
		budget:       budgetUSD,
		sessions:     make(map[string]*sessionState),
		sessionOrder: nil,
		now:          time.Now,
	}
}

// Subscribe wires the renderer to the event bus. Subscribes in Observe
// mode so it never blocks other consumers. Safe to call multiple times
// across different buses (each bus keeps its own subscriber table, and
// the hub deduplicates by ID, so the second call on the same bus is a
// no-op).
func (r *ProgressRenderer) Subscribe(bus *hub.Bus) {
	if bus == nil {
		return
	}
	bus.Register(hub.Subscriber{
		ID: "tui.progress_renderer",
		Events: []hub.EventType{
			EventStokePlanReady,
			EventStokeSessionStart,
			EventStokeSessionEnd,
			EventStokeTaskStart,
			EventStokeTaskEnd,
			EventStokeACResult,
			EventStokeDescentStart,
			EventStokeDescentTier,
			EventStokeDescentResolve,
			EventStokeCost,
		},
		Mode:     hub.ModeObserve,
		Priority: 9500, // after cost tracker (9000) so cost totals settle first
		Handler:  r.handle,
	})
}

// HandleEvent is the public entry point for direct event delivery (tests
// and non-bus integrations). It is equivalent to routing ev through the
// Subscribe handler.
func (r *ProgressRenderer) HandleEvent(ev *hub.Event) {
	if ev == nil {
		return
	}
	r.handle(context.Background(), ev)
}

// SessionCount reports how many sessions the renderer currently tracks.
// Intended for tests.
func (r *ProgressRenderer) SessionCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sessions)
}

// Spent reports the cost accumulated from stoke.cost events. Tests use it.
func (r *ProgressRenderer) Spent() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.spent
}

// Budget reports the last-known budget (from stoke.plan.ready or the
// constructor). Tests use it.
func (r *ProgressRenderer) Budget() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.budget
}

// --- event dispatch ---

func (r *ProgressRenderer) handle(_ context.Context, ev *hub.Event) *hub.HookResponse {
	if ev == nil {
		return &hub.HookResponse{Decision: hub.Allow}
	}

	switch ev.Type {
	case EventStokePlanReady:
		r.onPlanReady(ev)
	case EventStokeSessionStart:
		r.onSessionStart(ev)
	case EventStokeSessionEnd:
		r.onSessionEnd(ev)
	case EventStokeTaskStart:
		r.onTaskStart(ev)
	case EventStokeTaskEnd:
		r.onTaskEnd(ev)
	case EventStokeACResult:
		r.onACResult(ev)
	case EventStokeDescentStart:
		r.onDescentStart(ev)
	case EventStokeDescentTier:
		r.onDescentTier(ev)
	case EventStokeDescentResolve:
		r.onDescentResolve(ev)
	case EventStokeCost:
		r.onCost(ev)
	default:
		return &hub.HookResponse{Decision: hub.Allow}
	}

	r.redraw()
	return &hub.HookResponse{Decision: hub.Allow}
}

// The helpers below mutate r.mu-protected state. Each takes the lock.

func (r *ProgressRenderer) onPlanReady(ev *hub.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if v, ok := getFloat(ev.Custom, "budget_usd", "estimated_cost", "estimated_cost_usd"); ok {
		r.budget = v
	}
	if v, ok := getInt(ev.Custom, "total_sessions", "sessions"); ok {
		r.totalSessions = v
	}
	if v, ok := getString(ev.Custom, "title", "sow", "name"); ok {
		r.title = v
	}
	// Optional: preload sessions from a "plan" list if given.
	if plan, ok := ev.Custom["plan"].([]any); ok {
		for i, item := range plan {
			m, _ := item.(map[string]any)
			if m == nil {
				continue
			}
			id, _ := m["id"].(string)
			if id == "" {
				continue
			}
			if _, exists := r.sessions[id]; exists {
				continue
			}
			s := &sessionState{
				id:      id,
				status:  sessionPending,
				acs:     make(map[string]*acState),
				order:   i,
			}
			if t, ok := m["title"].(string); ok {
				s.title = t
			}
			if nacs, ok := toInt(m["acs"]); ok {
				s.totalACs = nacs
			}
			if blocked, ok := m["blocked"].(bool); ok && blocked {
				s.status = sessionBlocked
			}
			r.sessions[id] = s
			r.sessionOrder = append(r.sessionOrder, id)
		}
		if r.totalSessions == 0 {
			r.totalSessions = len(plan)
		}
	}
}

func (r *ProgressRenderer) onSessionStart(ev *hub.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := sessionID(ev)
	if id == "" {
		return
	}
	s := r.getOrCreateSession(id)
	s.status = sessionRunning
	s.startedAt = r.now()
	if t, ok := getString(ev.Custom, "title", "name"); ok && t != "" {
		s.title = t
	}
	if n, ok := getInt(ev.Custom, "total_acs", "acs"); ok {
		s.totalACs = n
	}
}

func (r *ProgressRenderer) onSessionEnd(ev *hub.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := sessionID(ev)
	if id == "" {
		return
	}
	s := r.getOrCreateSession(id)
	verdict, _ := getString(ev.Custom, "verdict", "status")
	if strings.EqualFold(verdict, "fail") || strings.EqualFold(verdict, "failed") {
		s.status = sessionFailed
	} else {
		s.status = sessionDone
	}
	s.endedAt = r.now()
	r.completedSessions++
}

func (r *ProgressRenderer) onTaskStart(ev *hub.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.getOrCreateSession(sessionID(ev))
	if s == nil {
		return
	}
	if tid, ok := getString(ev.Custom, "task_id", "id"); ok {
		s.currentTaskID = tid
	}
	if desc, ok := getString(ev.Custom, "description", "title", "task"); ok {
		s.currentTask = desc
	}
}

func (r *ProgressRenderer) onTaskEnd(ev *hub.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.getOrCreateSession(sessionID(ev))
	if s == nil {
		return
	}
	s.currentTask = ""
	s.currentTaskID = ""
}

func (r *ProgressRenderer) onACResult(ev *hub.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.getOrCreateSession(sessionID(ev))
	if s == nil {
		return
	}
	acID, _ := getString(ev.Custom, "ac_id", "id")
	if acID == "" {
		return
	}
	ac := r.getOrCreateAC(s, acID)
	if t, ok := getString(ev.Custom, "title", "description"); ok && t != "" {
		ac.title = t
	}
	verdict, _ := getString(ev.Custom, "verdict", "status", "result")
	previous := ac.status
	switch strings.ToLower(verdict) {
	case "pass", "passed", "ok", "success":
		ac.status = acPass
	case "fail", "failed", "error":
		ac.status = acFail
	case "softpass", "soft_pass", "soft-pass":
		ac.status = acSoftPass
	case "descent", "in_descent":
		ac.status = acInDescent
	default:
		// unknown verdict: leave as-is
	}
	if reason, ok := getString(ev.Custom, "reason", "message"); ok {
		ac.reason = reason
	}
	// Count a transition into pass/softpass (first time only) as a passed AC.
	if (ac.status == acPass || ac.status == acSoftPass) &&
		previous != acPass && previous != acSoftPass {
		s.passedACs++
	}
}

func (r *ProgressRenderer) onDescentStart(ev *hub.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.getOrCreateSession(sessionID(ev))
	if s == nil {
		return
	}
	acID, _ := getString(ev.Custom, "ac_id", "id")
	if acID == "" {
		return
	}
	ac := r.getOrCreateAC(s, acID)
	ac.status = acInDescent
	if tier, ok := getString(ev.Custom, "tier"); ok {
		ac.tier = tier
	} else {
		ac.tier = "T2"
	}
}

func (r *ProgressRenderer) onDescentTier(ev *hub.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.getOrCreateSession(sessionID(ev))
	if s == nil {
		return
	}
	acID, _ := getString(ev.Custom, "ac_id", "id")
	if acID == "" {
		return
	}
	ac := r.getOrCreateAC(s, acID)
	ac.status = acInDescent
	if tier, ok := getString(ev.Custom, "tier", "to_tier"); ok {
		ac.tier = tier
	}
	if cat, ok := getString(ev.Custom, "category", "classify"); ok {
		ac.category = cat
	}
	if n, ok := getInt(ev.Custom, "attempt", "repair_n"); ok {
		ac.repairN = n
	}
	if n, ok := getInt(ev.Custom, "max_attempts", "repair_max"); ok {
		ac.repairMax = n
	}
}

func (r *ProgressRenderer) onDescentResolve(ev *hub.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.getOrCreateSession(sessionID(ev))
	if s == nil {
		return
	}
	acID, _ := getString(ev.Custom, "ac_id", "id")
	if acID == "" {
		return
	}
	ac := r.getOrCreateAC(s, acID)
	verdict, _ := getString(ev.Custom, "verdict", "status", "result")
	previous := ac.status
	switch strings.ToLower(verdict) {
	case "pass", "passed":
		ac.status = acPass
	case "softpass", "soft_pass", "soft-pass":
		ac.status = acSoftPass
	case "fail", "failed":
		ac.status = acFail
	default:
		ac.status = acFail
	}
	ac.tier = ""
	if (ac.status == acPass || ac.status == acSoftPass) &&
		previous != acPass && previous != acSoftPass {
		s.passedACs++
	}
}

func (r *ProgressRenderer) onCost(ev *hub.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Prefer an explicit delta; fall back to Cost payload or total_spent.
	if v, ok := getFloat(ev.Custom, "delta_usd", "cost_usd", "amount"); ok {
		r.spent += v
	} else if ev.Cost != nil && ev.Cost.TotalSpent > 0 {
		r.spent = ev.Cost.TotalSpent
		if ev.Cost.BudgetLimit > 0 {
			r.budget = ev.Cost.BudgetLimit
		}
	} else if v, ok := getFloat(ev.Custom, "total_spent", "total_usd"); ok {
		r.spent = v
	}
	// A session-level cost attribution is optional.
	if sid := sessionID(ev); sid != "" {
		if v, ok := getFloat(ev.Custom, "session_cost_usd", "cost_usd"); ok {
			s := r.getOrCreateSession(sid)
			if s != nil {
				s.cost += v
			}
		}
	}
}

// getOrCreateSession returns the session by ID, creating a pending one if
// missing. Caller must already hold r.mu.
func (r *ProgressRenderer) getOrCreateSession(id string) *sessionState {
	if id == "" {
		return nil
	}
	if s, ok := r.sessions[id]; ok {
		return s
	}
	s := &sessionState{
		id:     id,
		status: sessionPending,
		acs:    make(map[string]*acState),
		order:  len(r.sessionOrder),
	}
	r.sessions[id] = s
	r.sessionOrder = append(r.sessionOrder, id)
	return s
}

// getOrCreateAC fetches or creates an AC on session s. Caller holds r.mu.
func (r *ProgressRenderer) getOrCreateAC(s *sessionState, id string) *acState {
	if ac, ok := s.acs[id]; ok {
		return ac
	}
	ac := &acState{id: id, status: acPending}
	s.acs[id] = ac
	s.acOrder = append(s.acOrder, id)
	return ac
}

// --- rendering ---

// redraw paints a full frame (TTY) or a single event line (non-TTY).
// Caller must NOT hold r.mu -- redraw takes it.
func (r *ProgressRenderer) redraw() {
	r.mu.Lock()
	frame := r.renderFrame()
	n := strings.Count(frame, "\n")
	if !strings.HasSuffix(frame, "\n") {
		n++
	}
	prior := r.lastFrameLines
	if r.isTTY {
		r.lastFrameLines = n
	}
	r.mu.Unlock()

	if !r.isTTY {
		// Plain-text fallback: one compact status line per event.
		_, _ = io.WriteString(r.w, frame)
		return
	}

	if prior > 0 {
		// Move cursor up `prior` lines and clear each line.
		fmt.Fprintf(r.w, "\x1b[%dA", prior)
		for i := 0; i < prior; i++ {
			_, _ = io.WriteString(r.w, "\x1b[2K")
			if i < prior-1 {
				_, _ = io.WriteString(r.w, "\x1b[1B")
			}
		}
		// Return to the top of the cleared region.
		if prior > 1 {
			fmt.Fprintf(r.w, "\x1b[%dA", prior-1)
		}
		_, _ = io.WriteString(r.w, "\r")
	}
	_, _ = io.WriteString(r.w, frame)
}

// renderFrame returns a full multi-line frame when isTTY, or a single
// one-line summary otherwise. Caller holds r.mu.
func (r *ProgressRenderer) renderFrame() string {
	if !r.isTTY {
		return r.renderPlainLine()
	}
	return r.renderTTYFrame()
}

func (r *ProgressRenderer) renderTTYFrame() string {
	var b strings.Builder

	title := r.title
	if title == "" {
		title = "stoke ship"
	}
	header := fmt.Sprintf("stoke ship -- %s (%d sessions, est. $%0.2f)", title, r.totalSessions, r.budget)
	b.WriteString(header)
	b.WriteByte('\n')
	b.WriteByte('\n')

	// Stable order: by insertion order (matches observed session.start
	// sequence, or plan.ready prefill).
	ids := make([]string, 0, len(r.sessionOrder))
	ids = append(ids, r.sessionOrder...)
	sort.SliceStable(ids, func(i, j int) bool {
		return r.sessions[ids[i]].order < r.sessions[ids[j]].order
	})

	for _, id := range ids {
		s := r.sessions[id]
		icon := statusIcon(s.status)
		title := s.title
		if title == "" {
			title = s.id
		}
		acStr := fmt.Sprintf("[%d/%d ACs]", s.passedACs, s.totalACs)
		if s.status == sessionBlocked {
			acStr = "[blocked]"
		}
		if s.status == sessionPending {
			acStr = "[pending]"
		}
		elapsed := ""
		if !s.startedAt.IsZero() {
			end := s.endedAt
			if end.IsZero() {
				end = r.now()
			}
			elapsed = fmtDuration(end.Sub(s.startedAt))
		}
		fmt.Fprintf(&b, "  %s %-24s %-14s $%0.2f  %s\n",
			icon, trimTitle(title, 24), acStr, s.cost, elapsed)

		// Render ACs only for actively running sessions.
		if s.status != sessionRunning {
			continue
		}
		for _, acID := range s.acOrder {
			ac := s.acs[acID]
			if ac == nil {
				continue
			}
			line := renderACLine(ac)
			if line != "" {
				fmt.Fprintf(&b, "     %s\n", line)
			}
		}
	}

	b.WriteByte('\n')
	fmt.Fprintf(&b, "  Spent: $%0.2f / $%0.2f   Sessions: %d/%d\n",
		r.spent, r.budget, r.completedSessions, r.totalSessions)

	return b.String()
}

// renderPlainLine returns one line summarising current state; used when
// the writer isn't a TTY (pipes, CI).
func (r *ProgressRenderer) renderPlainLine() string {
	return fmt.Sprintf(
		"stoke: sessions %d/%d  spent $%0.2f / $%0.2f\n",
		r.completedSessions, r.totalSessions, r.spent, r.budget,
	)
}

// renderACLine returns the "├─/└─ …" line for a single AC. Caller holds
// r.mu.
func renderACLine(ac *acState) string {
	var glyph string
	switch ac.status {
	case acPass:
		glyph = "[ok]"
	case acFail:
		glyph = "[x]"
	case acSoftPass:
		glyph = "[~]"
	case acInDescent:
		glyph = "[*]"
	case acPending:
		return ""
	default:
		return ""
	}
	title := ac.title
	if title == "" {
		title = ac.id
	}
	suffix := ""
	switch ac.status {
	case acInDescent:
		parts := []string{"descent"}
		if ac.tier != "" {
			parts = append(parts, ac.tier)
		}
		if ac.category != "" {
			parts = append(parts, ac.category)
		}
		suffix = fmt.Sprintf(" (%s)", strings.Join(parts, ": "))
		if ac.repairMax > 0 {
			suffix += fmt.Sprintf(" repair %d/%d", ac.repairN, ac.repairMax)
		}
	case acFail:
		if ac.reason != "" {
			suffix = fmt.Sprintf(" — %s", trimTitle(ac.reason, 40))
		}
	case acSoftPass:
		if ac.category != "" {
			suffix = fmt.Sprintf(" (soft-pass: %s)", ac.category)
		} else {
			suffix = " (soft-pass)"
		}
	case acPending, acPass:
		// No suffix for pending / passing states.
	}
	return fmt.Sprintf("%s %s %s%s", "|--", glyph, trimTitle(title, 30), suffix)
}

// --- helpers ---

func statusIcon(s sessionStatus) string {
	switch s {
	case sessionRunning:
		return "[>]"
	case sessionDone:
		return "[v]"
	case sessionFailed:
		return "[x]"
	case sessionBlocked:
		return "[-]"
	case sessionPending:
		return "[ ]"
	default:
		return "[ ]"
	}
}

func fmtDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d.Seconds())
	m := total / 60
	s := total % 60
	return fmt.Sprintf("%d:%02d", m, s)
}

func trimTitle(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

// sessionID pulls the session identifier from either Custom["session_id"]
// or Event.MissionID (the plumbing is flexible -- the streamjson emitter
// currently places it in `_stoke.dev/session` which the bus bridge copies
// to Custom["session_id"]).
func sessionID(ev *hub.Event) string {
	if v, ok := getString(ev.Custom, "session_id", "session"); ok {
		return v
	}
	if ev.MissionID != "" {
		return ev.MissionID
	}
	return ""
}

// getString fetches the first string-valued key from m.
func getString(m map[string]any, keys ...string) (string, bool) {
	if m == nil {
		return "", false
	}
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s, true
			}
		}
	}
	return "", false
}

// getFloat fetches the first numeric-valued key from m as a float64.
func getFloat(m map[string]any, keys ...string) (float64, bool) {
	if m == nil {
		return 0, false
	}
	for _, k := range keys {
		v, ok := m[k]
		if !ok {
			continue
		}
		if f, ok := toFloat(v); ok {
			return f, true
		}
	}
	return 0, false
}

// getInt fetches the first numeric-valued key from m as an int.
func getInt(m map[string]any, keys ...string) (int, bool) {
	if m == nil {
		return 0, false
	}
	for _, k := range keys {
		v, ok := m[k]
		if !ok {
			continue
		}
		if n, ok := toInt(v); ok {
			return n, true
		}
	}
	return 0, false
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int32:
		return float64(x), true
	case int64:
		return float64(x), true
	}
	return 0, false
}

func toInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int32:
		return int(x), true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	case float32:
		return int(x), true
	}
	return 0, false
}
