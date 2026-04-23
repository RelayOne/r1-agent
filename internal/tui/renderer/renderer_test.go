package renderer

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// sysEvt builds a system/* event map the way the streamjson emitter
// shapes it: _stoke.dev/ namespaced keys for our custom fields.
func sysEvt(subtype string, fields map[string]any) Event {
	m := map[string]any{"type": "system", "subtype": subtype}
	for k, v := range fields {
		m[k] = v
	}
	e, ok := ParseEvent(m)
	if !ok {
		panic("ParseEvent failed for " + subtype)
	}
	return e
}

// TestModelUpdate — synthetic events drive the state machine through
// the shapes documented in §Event → UI component mapping.
func TestModelUpdate(t *testing.T) {
	m := NewModel("sentinel-mvp")

	// session.start → SessionNode appended, focused.
	m.Apply(sysEvt("session.start", map[string]any{
		"_stoke.dev/session": "S3",
		"_stoke.dev/title":   "API routes",
	}))
	snap := m.Snapshot()
	if len(snap.Sessions) != 1 || snap.Sessions[0].ID != "S3" {
		t.Fatalf("session.start did not append node: %+v", snap.Sessions)
	}
	if snap.Sessions[0].Status != StatusRunning {
		t.Fatalf("session.start should mark Running, got %v", snap.Sessions[0].Status)
	}

	// task.dispatch → TaskNode under session, attempt bumped.
	m.Apply(sysEvt("task.dispatch", map[string]any{
		"_stoke.dev/session":  "S3",
		"_stoke.dev/task_id":  "T1",
		"_stoke.dev/attempt":  2.0,
		"_stoke.dev/title":    "Implement routes",
	}))
	snap = m.Snapshot()
	if len(snap.Sessions[0].Tasks) != 1 || snap.Sessions[0].Tasks[0].ID != "T1" {
		t.Fatalf("task.dispatch did not add task: %+v", snap.Sessions[0].Tasks)
	}
	if snap.Sessions[0].Tasks[0].Attempt != 2 {
		t.Fatalf("task.dispatch attempt=2 expected, got %d",
			snap.Sessions[0].Tasks[0].Attempt)
	}

	// ac.result pass → AC synthesized + status=pass.
	m.Apply(sysEvt("ac.result", map[string]any{
		"_stoke.dev/session":  "S3",
		"_stoke.dev/task_id":  "T1",
		"_stoke.dev/ac_id":    "AC1",
		"_stoke.dev/verdict":  "pass",
		"_stoke.dev/title":    "GET /tasks returns 200",
	}))
	snap = m.Snapshot()
	acs := snap.Sessions[0].Tasks[0].ACs
	if len(acs) != 1 || acs[0].Status != ACPass {
		t.Fatalf("ac.result did not mark pass: %+v", acs)
	}

	// descent.tier → descent ring gets a tick, AC tier updated.
	m.Apply(sysEvt("descent.tier", map[string]any{
		"_stoke.dev/session":   "S3",
		"_stoke.dev/task_id":   "T1",
		"_stoke.dev/ac_id":     "AC1",
		"_stoke.dev/tier":      "T4",
		"_stoke.dev/from_tier": "T3",
		"_stoke.dev/reason":    "repair",
		"_stoke.dev/attempt":   2.0,
	}))
	snap = m.Snapshot()
	if len(snap.Descent) != 1 {
		t.Fatalf("descent.tier should push to ring, got %d ticks", len(snap.Descent))
	}
	if snap.Descent[0].ToTier != "T4" {
		t.Fatalf("descent.tier tier mismatch: %+v", snap.Descent[0])
	}

	// task.complete → task marked done.
	m.Apply(sysEvt("task.complete", map[string]any{
		"_stoke.dev/session": "S3",
		"_stoke.dev/task_id": "T1",
		"_stoke.dev/verdict": "pass",
	}))
	snap = m.Snapshot()
	if snap.Sessions[0].Tasks[0].Status != StatusDone {
		t.Fatalf("task.complete should mark done, got %v",
			snap.Sessions[0].Tasks[0].Status)
	}

	// cost.update → costSpent updated.
	m.Apply(sysEvt("cost.update", map[string]any{
		"_stoke.dev/cost_usd": 8.42,
	}))
	snap = m.Snapshot()
	if snap.CostSpent != 8.42 {
		t.Fatalf("cost.update did not set costSpent, got %v", snap.CostSpent)
	}
}

// TestHITLModal — hitl_required opens modal; enter closes it.
func TestHITLModal(t *testing.T) {
	m := NewModel("sentinel")
	ev, ok := ParseEvent(map[string]any{
		"type":                  "hitl_required",
		"_stoke.dev/ask_id":     "ask-1",
		"_stoke.dev/reason":     "Soft-pass at T8",
		"_stoke.dev/ac_id":      "AC-03",
		"_stoke.dev/tier":       "T8",
		"_stoke.dev/category":   "acceptable_as_is",
		"_stoke.dev/evidence":   "3 analysts agreed env-gap only",
	})
	if !ok {
		t.Fatalf("parse hitl_required failed")
	}
	m.Apply(ev)
	snap := m.Snapshot()
	if !snap.HITLOpen {
		t.Fatalf("hitl_required should open modal")
	}

	// enter commits; modal closes.
	if _, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter}); false {
	}
	snap = m.Snapshot()
	if snap.HITLOpen {
		t.Fatalf("enter should close modal")
	}
}

// TestHITLQueue — two HITL events: second queues behind the first.
func TestHITLQueue(t *testing.T) {
	m := NewModel("sentinel")
	for i := 0; i < 2; i++ {
		ev, _ := ParseEvent(map[string]any{
			"type":              "hitl_required",
			"_stoke.dev/ask_id": "ask-x",
		})
		m.Apply(ev)
	}
	// Close the first; the second should auto-open.
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	snap := m.Snapshot()
	if !snap.HITLOpen {
		t.Fatalf("second hitl should auto-open after first closes")
	}
	// Close again — now queue is empty.
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	snap = m.Snapshot()
	if snap.HITLOpen {
		t.Fatalf("all hitl closed, modal should be closed")
	}
}

// TestHITLSessionctlNil — pressing `a` in main mode (no sessionctl)
// shows a toast rather than panicking.
func TestHITLSessionctlNil(t *testing.T) {
	m := NewModel("sentinel")
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	snap := m.Snapshot()
	if snap.HITLToast == "" {
		t.Fatalf("pressing `a` without sessionctl should emit a toast")
	}
	if !strings.Contains(snap.HITLToast, "unavailable") {
		t.Fatalf("toast should say unavailable, got %q", snap.HITLToast)
	}
}

// TestFallbackNoColor — NO_COLOR = monochrome glyphs.
func TestFallbackNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := NewModel("demo")
	m.Apply(sysEvt("session.start", map[string]any{
		"_stoke.dev/session": "S1",
	}))
	out := m.View()
	// Monochrome running glyph is ">" not "▶".
	if strings.Contains(out, "▶") {
		t.Fatalf("NO_COLOR set but Unicode running glyph present: %q", out)
	}
	if !strings.Contains(out, ">") {
		t.Fatalf("expected ascii '>' running glyph, got %q", out)
	}
}

// TestFallbackSmallTerminal — width<80 → compact one-liner.
func TestFallbackSmallTerminal(t *testing.T) {
	m := NewModel("demo")
	m.Apply(sysEvt("session.start", map[string]any{
		"_stoke.dev/session": "S1",
	}))
	m.SetSize(60, 10)
	out := m.View()
	if !strings.HasPrefix(out, "\r") {
		t.Fatalf("compact mode should start with carriage return, got %q", out)
	}
	if strings.Contains(out, "\n") {
		t.Fatalf("compact mode should be single-line, got %q", out)
	}
}

// TestEventScrollRing — 100 events collapse to the last 64.
func TestEventScrollRing(t *testing.T) {
	m := NewModel("demo")
	for i := 0; i < 100; i++ {
		m.Apply(sysEvt("worker.tool", map[string]any{
			"_stoke.dev/session": "S1",
		}))
	}
	snap := m.Snapshot()
	if len(snap.Events) != ringEventsCap {
		t.Fatalf("events ring should cap at %d, got %d",
			ringEventsCap, len(snap.Events))
	}
}

// TestDescentTickerRing — 20 ticks collapse to the last 8.
func TestDescentTickerRing(t *testing.T) {
	m := NewModel("demo")
	for i := 0; i < 20; i++ {
		m.Apply(sysEvt("descent.tier", map[string]any{
			"_stoke.dev/session": "S1",
			"_stoke.dev/task_id": "T1",
			"_stoke.dev/tier":    "T4",
		}))
	}
	snap := m.Snapshot()
	if len(snap.Descent) != ringDescentCap {
		t.Fatalf("descent ring should cap at %d, got %d",
			ringDescentCap, len(snap.Descent))
	}
}

// TestUnknownSubtype — unknown subtype lands in the scroll without
// mutating the tree or raising a panic.
func TestUnknownSubtype(t *testing.T) {
	m := NewModel("demo")
	m.Apply(sysEvt("stoke.experimental.foo", map[string]any{
		"_stoke.dev/session": "S1",
	}))
	snap := m.Snapshot()
	if len(snap.Events) != 1 {
		t.Fatalf("unknown subtype should append to scroll exactly once, got %d", len(snap.Events))
	}
	// Tree may still have the synthesized session (session key parsed);
	// we only guarantee that no panic occurred and the scroll captured it.
	if snap.Events[0].Subtype != "stoke.experimental.foo" {
		t.Fatalf("scroll entry subtype mismatch: %q",
			snap.Events[0].Subtype)
	}
}

// TestQuitKeepsSessionAlive — q sends tea.Quit but does not cancel a
// caller-owned context. We assert the quit command is returned and the
// model's quit reason is recorded.
func TestQuitKeepsSessionAlive(t *testing.T) {
	m := NewModel("demo")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Fatalf("expected tea.Quit cmd, got nil")
	}
	// Execute the cmd — it should return tea.QuitMsg.
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("quit cmd should produce tea.QuitMsg")
	}
	snap := m.Snapshot()
	// quitReason stored on model; verify via a dedicated accessor.
	m.mu.Lock()
	reason := m.quitReason
	done := m.done
	m.mu.Unlock()
	if reason != "user" || !done {
		t.Fatalf("expected quitReason=user, done=true; got %q / %v", reason, done)
	}
	_ = snap
}

// TestHelpOverlay — `?` toggles help; `esc` dismisses.
func TestHelpOverlay(t *testing.T) {
	m := NewModel("demo")
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m.mu.Lock()
	if !m.helpOpen {
		m.mu.Unlock()
		t.Fatalf("? should open help")
	}
	m.mu.Unlock()
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.helpOpen {
		t.Fatalf("esc should close help")
	}
}

// TestParseEventUnknownType — a payload with no type field is rejected.
func TestParseEventUnknownType(t *testing.T) {
	if _, ok := ParseEvent(map[string]any{"foo": "bar"}); ok {
		t.Fatalf("ParseEvent should reject payload without type")
	}
	if _, ok := ParseEvent(nil); ok {
		t.Fatalf("ParseEvent should reject nil map")
	}
}

// TestDescentClassify — classify event updates AC verdict string.
func TestDescentClassify(t *testing.T) {
	m := NewModel("demo")
	m.Apply(sysEvt("session.start", map[string]any{
		"_stoke.dev/session": "S1",
	}))
	m.Apply(sysEvt("task.dispatch", map[string]any{
		"_stoke.dev/session": "S1",
		"_stoke.dev/task_id": "T1",
	}))
	m.Apply(sysEvt("ac.result", map[string]any{
		"_stoke.dev/session":  "S1",
		"_stoke.dev/task_id":  "T1",
		"_stoke.dev/ac_id":    "AC1",
		"_stoke.dev/verdict":  "fail",
	}))
	m.Apply(sysEvt("descent.classify", map[string]any{
		"_stoke.dev/session":  "S1",
		"_stoke.dev/task_id":  "T1",
		"_stoke.dev/ac_id":    "AC1",
		"_stoke.dev/category": "code_bug",
		"_stoke.dev/reason":   "leaky bucket",
	}))
	snap := m.Snapshot()
	acs := snap.Sessions[0].Tasks[0].ACs
	if len(acs) == 0 || !strings.Contains(acs[0].Verdict, "code_bug") {
		t.Fatalf("descent.classify should set verdict with category, got %+v", acs)
	}
}

// TestCompactLineShape — compact output shows cost gauge + tier.
func TestCompactLineShape(t *testing.T) {
	m := NewModel("sentinel-mvp")
	m.SetBudget(15.00)
	m.Apply(sysEvt("session.start", map[string]any{
		"_stoke.dev/session": "S3",
	}))
	m.Apply(sysEvt("cost.update", map[string]any{
		"_stoke.dev/cost_usd": 8.42,
	}))
	m.SetSize(60, 18)
	line := m.CompactLine()
	for _, want := range []string{"sentinel-mvp", "S3", "$8.42", "$15.00"} {
		if !strings.Contains(line, want) {
			t.Fatalf("compact line missing %q: %q", want, line)
		}
	}
}

// TestMissionAbortedSetsDone — abort event flips done flag.
func TestMissionAbortedSetsDone(t *testing.T) {
	m := NewModel("demo")
	ev, _ := ParseEvent(map[string]any{"type": "mission.aborted"})
	m.Apply(ev)
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.done {
		t.Fatalf("mission.aborted should set done=true")
	}
}

// TestWindowSizeMsg — resize message flips tooSmall at the threshold.
func TestWindowSizeMsg(t *testing.T) {
	m := NewModel("demo")
	m.Update(tea.WindowSizeMsg{Width: 70, Height: 18})
	m.mu.Lock()
	if !m.tooSmall {
		m.mu.Unlock()
		t.Fatalf("70x18 should be tooSmall")
	}
	m.mu.Unlock()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tooSmall {
		t.Fatalf("120x30 should not be tooSmall")
	}
}
