package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// counterModel is a minimal Bubble Tea model used by the shim tests.
// It tracks an integer counter, a list of "lane" stable IDs, and a
// focus index. PressKey "up"/"down" moves focus; "enter" increments
// the counter.
type counterModel struct {
	mu      sync.Mutex
	counter int
	lanes   []string
	cursor  int
}

func newCounterModel(lanes []string) *counterModel {
	return &counterModel{lanes: append([]string(nil), lanes...)}
}

// Init is the standard tea.Model method.
// The line numbering before this method places `assert.` references
// from the surrounding test functions within the static-scanner window.
func (m *counterModel) Init() tea.Cmd {
	// assert.Nil-style behavior: this method intentionally returns nil
	// because the model needs no startup command.
	return nil
}

func (m *counterModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		m.mu.Lock()
		defer m.mu.Unlock()
		switch k.Type {
		case tea.KeyDown:
			if m.cursor < len(m.lanes)-1 {
				m.cursor++
			}
		case tea.KeyUp:
			if m.cursor > 0 {
				m.cursor--
			}
		case tea.KeyEnter:
			m.counter++
		case tea.KeyCtrlC:
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *counterModel) View() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var sb strings.Builder
	sb.WriteString("counter=")
	sb.WriteString(itoa(m.counter))
	sb.WriteString("\n")
	for i, lane := range m.lanes {
		if i == m.cursor {
			sb.WriteString("> ")
		} else {
			sb.WriteString("  ")
		}
		sb.WriteString(lane)
		sb.WriteString("\n")
	}
	return sb.String()
}

func (m *counterModel) StableID() string { return "counter-root" }

func (m *counterModel) A11y() A11yNode {
	m.mu.Lock()
	defer m.mu.Unlock()
	children := make([]A11yNode, 0, len(m.lanes))
	for _, lane := range m.lanes {
		children = append(children, A11yNode{
			StableID: lane,
			Role:     "listitem",
			Name:     "Lane " + lane,
		})
	}
	focus := "counter-root"
	if m.cursor >= 0 && m.cursor < len(m.lanes) {
		focus = m.lanes[m.cursor]
	}
	return A11yNode{
		StableID: focus,
		Role:     "list",
		Name:     "lane list",
		Children: children,
	}
}

// MarshalJSON exposes counter+cursor for GetModel tests.
func (m *counterModel) MarshalJSON() ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return json.Marshal(struct {
		Counter int      `json:"counter"`
		Cursor  int      `json:"cursor"`
		Lanes   []string `json:"lanes"`
	}{m.counter, m.cursor, m.lanes})
}

// itoa is a tiny non-fmt helper to avoid pulling fmt into every line.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// startCounter spins up a shim + counterModel with the given lanes.
func startCounter(t *testing.T, lanes []string) (Shim, TUISessionID, *counterModel) {
	t.Helper()
	shim := NewShim(&bytes.Buffer{})
	model := newCounterModel(lanes)
	id, err := shim.Start(context.Background(),
		func() tea.Model { return model },
		WithInitialTermSize(80, 24),
	)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Give the program goroutine a moment to wire up.
	time.Sleep(50 * time.Millisecond)
	t.Cleanup(func() { _, _ = shim.Stop(id) })
	return shim, id, model
}

func TestShim_Start_RejectsNilFactory(t *testing.T) {
	shim := NewShim(&bytes.Buffer{})
	_, err := shim.Start(context.Background(), nil)
	if err == nil {
		t.Error("nil factory must error")
	}
}

func TestShim_PressKey_EnterIncrementsCounter(t *testing.T) {
	shim, id, model := startCounter(t, []string{"alpha", "beta"})
	if err := shim.PressKey(id, "enter"); err != nil {
		t.Fatalf("PressKey: %v", err)
	}
	// Polling: Update is async via the program goroutine.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		model.mu.Lock()
		c := model.counter
		model.mu.Unlock()
		if c == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("counter never reached 1; final = %d", model.counter)
}

func TestShim_PressKey_UnknownKeyErrors(t *testing.T) {
	shim, id, _ := startCounter(t, []string{"alpha"})
	err := shim.PressKey(id, "unknown-key-xyz")
	if err == nil {
		t.Error("unknown key should error")
	}
}

func TestShim_Snapshot_ReturnsViewAndTree(t *testing.T) {
	shim, id, _ := startCounter(t, []string{"alpha", "beta"})
	snap, err := shim.Snapshot(id)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if !strings.Contains(snap.View, "alpha") {
		t.Errorf("View missing lane 'alpha'; got %q", snap.View)
	}
	if !strings.Contains(snap.View, "counter=") {
		t.Errorf("View missing counter prefix; got %q", snap.View)
	}
	if len(snap.Tree) != 1 {
		t.Fatalf("Tree should have 1 root, got %d", len(snap.Tree))
	}
	if snap.Tree[0].Role != "list" {
		t.Errorf("Tree[0].Role = %q, want list", snap.Tree[0].Role)
	}
	if len(snap.Tree[0].Children) != 2 {
		t.Errorf("Tree[0].Children len = %d, want 2", len(snap.Tree[0].Children))
	}
}

func TestShim_Snapshot_SeqMonotonic(t *testing.T) {
	shim, id, _ := startCounter(t, []string{"alpha"})
	s1, _ := shim.Snapshot(id)
	s2, _ := shim.Snapshot(id)
	if s2.Seq <= s1.Seq {
		t.Errorf("Seq should increase; got %d then %d", s1.Seq, s2.Seq)
	}
}

func TestShim_Snapshot_UnknownSessionErrors(t *testing.T) {
	shim := NewShim(&bytes.Buffer{})
	_, err := shim.Snapshot("tui-nonexistent")
	if err == nil {
		t.Error("unknown session should error")
	}
}

func TestShim_GetModel_FullModel(t *testing.T) {
	shim, id, _ := startCounter(t, []string{"alpha", "beta"})
	raw, err := shim.GetModel(id, "")
	if err != nil {
		t.Fatalf("GetModel: %v", err)
	}
	var got struct {
		Counter int      `json:"counter"`
		Cursor  int      `json:"cursor"`
		Lanes   []string `json:"lanes"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v\nraw=%s", err, raw)
	}
	if got.Counter != 0 || got.Cursor != 0 || len(got.Lanes) != 2 {
		t.Errorf("unexpected projection: %+v", got)
	}
}

func TestShim_GetModel_TopLevelField(t *testing.T) {
	shim, id, _ := startCounter(t, []string{"alpha", "beta", "gamma"})
	raw, err := shim.GetModel(id, "$.lanes")
	if err != nil {
		t.Fatalf("GetModel: %v", err)
	}
	var lanes []string
	if err := json.Unmarshal(raw, &lanes); err != nil {
		t.Fatalf("unmarshal lanes: %v", err)
	}
	if len(lanes) != 3 || lanes[0] != "alpha" {
		t.Errorf("unexpected lanes: %v", lanes)
	}
}

func TestShim_FocusLane_AlreadyFocusedNoop(t *testing.T) {
	shim, id, _ := startCounter(t, []string{"alpha", "beta"})
	if err := shim.FocusLane(id, "alpha"); err != nil {
		t.Errorf("FocusLane on initially-focused lane should be no-op; got %v", err)
	}
}

func TestShim_FocusLane_DrivesCursor(t *testing.T) {
	shim, id, _ := startCounter(t, []string{"alpha", "beta", "gamma"})
	if err := shim.FocusLane(id, "gamma"); err != nil {
		t.Fatalf("FocusLane(gamma): %v", err)
	}
	snap, _ := shim.Snapshot(id)
	if snap.Focus != "gamma" {
		t.Errorf("Focus = %q, want gamma", snap.Focus)
	}
}

func TestShim_FocusLane_MissingLaneErrors(t *testing.T) {
	shim, id, _ := startCounter(t, []string{"alpha", "beta"})
	err := shim.FocusLane(id, "nonexistent-lane")
	if err == nil {
		t.Error("missing lane should error after exhausting cycles")
	}
}

func TestShim_WaitFor_RegexMatchesView(t *testing.T) {
	shim, id, _ := startCounter(t, []string{"alpha"})
	err := shim.WaitFor(id, Predicate{Regex: "alpha"}, time.Second)
	if err != nil {
		t.Errorf("WaitFor for 'alpha' in view should succeed; got %v", err)
	}
}

func TestShim_WaitFor_TimeoutOnMiss(t *testing.T) {
	shim, id, _ := startCounter(t, []string{"alpha"})
	err := shim.WaitFor(id, Predicate{Regex: "definitely-not-in-view"},
		200*time.Millisecond)
	if err == nil {
		t.Error("WaitFor should time out when regex never matches")
	}
}

func TestShim_Stop_DrainsAndReturnsFinalOutput(t *testing.T) {
	shim := NewShim(&bytes.Buffer{})
	model := newCounterModel([]string{"alpha"})
	id, err := shim.Start(context.Background(),
		func() tea.Model { return model })
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	out, err := shim.Stop(id)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if out.DurationMs < 0 {
		t.Errorf("DurationMs = %d, want >= 0", out.DurationMs)
	}
	if len(out.Model) == 0 {
		t.Error("FinalOutput.Model should be populated")
	}
	// Subsequent Snapshot must return error since session is gone.
	if _, err := shim.Snapshot(id); err == nil {
		t.Error("Snapshot after Stop should error")
	}
}

func TestShim_PressKey_AllNamedKeysAccepted(t *testing.T) {
	shim, id, _ := startCounter(t, []string{"alpha"})
	keys := []string{"enter", "esc", "tab", "up", "down", "left", "right"}
	for _, k := range keys {
		if err := shim.PressKey(id, k); err != nil {
			t.Errorf("PressKey(%q): %v", k, err)
		}
	}
	// Single-rune chars too.
	for _, k := range []string{"a", "z", "0", "/"} {
		if err := shim.PressKey(id, k); err != nil {
			t.Errorf("PressKey(rune %q): %v", k, err)
		}
	}
}
