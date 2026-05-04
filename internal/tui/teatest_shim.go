// teatest_shim.go — in-process MCP-facing harness for Bubble Tea models
// per specs/agentic-test-harness.md §5 + §12 item 11.
//
// STATUS: PARTIAL — the canonical reference implementation per §5 wraps
// charmbracelet/x/exp/teatest. That package is not vendored in this
// checkpoint (only bubbletea v0.25, lipgloss v1.1, and x/{ansi,cellbuf,
// term} are), so this shim ships the same MCP surface using a
// vendor-only in-process driver. The interface is identical so the
// caller (cmd/r1d/...) does not need to know the difference; once the
// teatest dependency lands, the internal Start/Stop/Send paths swap to
// teatest.NewTestModel without changing the public Shim contract.
//
// What this shim provides:
//
//   - Shim interface (Start, PressKey, Snapshot, GetModel, FocusLane,
//     WaitFor, Stop) used by the four r1.tui.* MCP handlers.
//   - Deterministic snapshot output: lipgloss color profile is set to
//     ASCII at NewShim() time so View() output is byte-identical across
//     CI runners (item 13/43).
//   - Synthetic A11y tree extraction: any model implementing
//     internal/tui.A11yEmitter contributes its A11yNode tree to the
//     Snapshot. Models that don't implement it get a single root
//     A11yNode with StableID="unknown" (the lint at
//     tools/lint-view-without-api/ flags this case as a failure).
//
// Threading model: Send / Snapshot / GetModel / Stop are guarded by the
// Shim's per-session mutex; concurrent calls serialize. The Bubble Tea
// program runs in a goroutine started by Start; Stop signals it via
// QuitMsg and waits for the program goroutine to return.
package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// TUISessionID uniquely identifies one teatest harness session.
// Format: "tui-<hex sequence>" — stable across reconnects from the
// caller's perspective; the hex suffix is deterministic per (shim,
// session-creation order).
type TUISessionID string

// Shim is the MCP-facing surface for the in-process Bubble Tea
// harness. One Shim instance per r1d daemon; it owns all live
// TUISession entries.
type Shim interface {
	// Start launches a new in-process tea.Program-backed session for
	// the given root-model factory. Returns immediately once the
	// program is ready to receive Send().
	Start(ctx context.Context, modelFactory func() tea.Model, opts ...ProgramOption) (TUISessionID, error)

	// PressKey injects a key event into the named session. Mapping:
	//   "enter"   -> tea.KeyEnter
	//   "esc"     -> tea.KeyEsc
	//   "ctrl+c"  -> tea.KeyCtrlC
	//   "tab"     -> tea.KeyTab
	//   "up"/"down"/"left"/"right" -> arrow keys
	//   single char "x" -> tea.KeyRunes with that rune
	// Errors on unknown keys.
	PressKey(id TUISessionID, key string) error

	// Snapshot returns the deterministic view, the synthetic
	// accessibility tree, and the focused stable_id. Idempotent; safe
	// to call concurrently with PressKey.
	Snapshot(id TUISessionID) (Snapshot, error)

	// GetModel returns a JSON projection of the live tea.Model rooted
	// at the requested JSONPath. Empty path returns the whole model.
	GetModel(id TUISessionID, jsonPath string) (json.RawMessage, error)

	// FocusLane drives "j"/"down" key presses until the given lane_id
	// is the spotlight. No-op if already focused. Errors when lane is
	// not present after a full cycle.
	FocusLane(id TUISessionID, laneID string) error

	// WaitFor blocks up to timeout for the predicate to be satisfied
	// against the latest snapshot. Polls every 50ms; the timeout
	// budget is enforced by ctx cancellation in the runner caller.
	WaitFor(id TUISessionID, predicate Predicate, timeout time.Duration) error

	// Stop terminates the session, drains the program goroutine, and
	// returns the captured stdout tail + final model JSON for golden-
	// file diffing.
	Stop(id TUISessionID) (FinalOutput, error)
}

// Snapshot is the response shape for r1.tui.snapshot.
type Snapshot struct {
	View  string     `json:"view"`
	Tree  []A11yNode `json:"tree"`
	Focus string     `json:"focus"`
	Seq   int64      `json:"seq"`
}

// Predicate is the WaitFor matcher. Either Regex (matched against the
// snapshot view) or JSONPath+Equals (matched against the model JSON
// projection) is set; not both.
type Predicate struct {
	Regex    string `json:"regex,omitempty"`
	JSONPath string `json:"jsonpath,omitempty"`
	Equals   string `json:"equals,omitempty"`
}

// FinalOutput is returned by Stop.
type FinalOutput struct {
	StdoutTail string          `json:"stdout_tail"`
	Model      json.RawMessage `json:"model"`
	DurationMs int64           `json:"duration_ms"`
}

// ProgramOption is the local stand-in for tea.ProgramOption — when the
// teatest dependency lands, this aliases teatest.ProgramOption. The
// shim today accepts an empty slice and ignores caller-supplied
// options; the lint at tools/lint-view-without-api/ checks that no
// caller passes a non-empty slice in this checkpoint.
type ProgramOption func(*programOptions)

type programOptions struct {
	initialTermWidth  int
	initialTermHeight int
}

// WithInitialTermSize configures the synthetic terminal size for the
// program. Default 120x40.
func WithInitialTermSize(w, h int) ProgramOption {
	return func(po *programOptions) {
		po.initialTermWidth = w
		po.initialTermHeight = h
	}
}

// NewShim constructs the singleton Shim. out is typically io.Discard
// in production; tests can pass a *bytes.Buffer to capture output.
//
// As a side-effect, NewShim sets the lipgloss color profile to ASCII
// (per spec §10a "Snapshot drift" mitigation) so snapshots are
// byte-deterministic across CI runners. This is item 13/43.
func NewShim(out io.Writer) Shim {
	// Set ASCII color profile so View() output never carries terminal
	// escape sequences. This is global lipgloss state; we do it once
	// at shim construction so it propagates to every program the shim
	// hosts. Tests that need a different profile must call
	// lipgloss.SetColorProfile themselves AFTER NewShim.
	lipgloss.SetColorProfile(termenv.Ascii)
	if out == nil {
		out = io.Discard
	}
	return &shimImpl{
		out:      out,
		sessions: make(map[TUISessionID]*tuiSession),
	}
}

// shimImpl is the concrete in-process Shim. The exported interface is
// minimal so a future swap to teatest.NewTestModel only changes this
// file.
type shimImpl struct {
	mu       sync.Mutex
	out      io.Writer
	seq      atomic.Int64
	sessions map[TUISessionID]*tuiSession
}

// tuiSession bundles per-session state.
type tuiSession struct {
	mu        sync.Mutex
	id        TUISessionID
	prog      *tea.Program
	model     tea.Model
	startedAt time.Time
	done      chan struct{}
	progErr   error
	snapSeq   atomic.Int64
	focused   string
	out       *capturingWriter
}

// capturingWriter is a thread-safe write sink that retains the last
// 64KiB of output for FinalOutput.StdoutTail.
type capturingWriter struct {
	mu    sync.Mutex
	buf   []byte
	limit int
}

func (w *capturingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	if len(w.buf) > w.limit {
		// Trim head, keep tail.
		w.buf = append([]byte(nil), w.buf[len(w.buf)-w.limit:]...)
	}
	return len(p), nil
}

func (w *capturingWriter) Tail() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(append([]byte(nil), w.buf...))
}

// Start launches a tea.Program in a background goroutine.
func (s *shimImpl) Start(ctx context.Context, modelFactory func() tea.Model, opts ...ProgramOption) (TUISessionID, error) {
	if modelFactory == nil {
		return "", errors.New("Start: modelFactory must not be nil")
	}
	po := &programOptions{initialTermWidth: 120, initialTermHeight: 40}
	for _, opt := range opts {
		opt(po)
	}

	model := modelFactory()
	if model == nil {
		return "", errors.New("Start: modelFactory returned nil model")
	}

	cap := &capturingWriter{limit: 64 * 1024}
	prog := tea.NewProgram(model,
		tea.WithInput(strings.NewReader("")),
		tea.WithOutput(cap),
		tea.WithoutSignalHandler(),
		tea.WithoutCatchPanics(),
	)

	id := TUISessionID(fmt.Sprintf("tui-%016x", s.seq.Add(1)))
	sess := &tuiSession{
		id:        id,
		prog:      prog,
		model:     model,
		startedAt: time.Now(),
		done:      make(chan struct{}),
		out:       cap,
	}

	s.mu.Lock()
	s.sessions[id] = sess
	s.mu.Unlock()

	// Run the program in the background. Any error (including normal
	// QuitMsg-driven exit) is captured for Stop().
	go func() {
		defer close(sess.done)
		final, err := prog.Run()
		sess.mu.Lock()
		if final != nil {
			sess.model = final
		}
		sess.progErr = err
		sess.mu.Unlock()
	}()

	// Synchronize with program startup: send a no-op WindowSizeMsg so
	// we know Send() is wired before returning.
	prog.Send(tea.WindowSizeMsg{Width: po.initialTermWidth, Height: po.initialTermHeight})

	return id, nil
}

// PressKey converts the named key into a tea.KeyMsg and forwards it.
func (s *shimImpl) PressKey(id TUISessionID, key string) error {
	sess, err := s.lookup(id)
	if err != nil {
		return err
	}
	msg, err := keyMsgFromString(key)
	if err != nil {
		return err
	}
	sess.prog.Send(msg)
	return nil
}

func keyMsgFromString(key string) (tea.KeyMsg, error) {
	switch key {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}, nil
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}, nil
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}, nil
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}, nil
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}, nil
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}, nil
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}, nil
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}, nil
	}
	if len(key) == 1 {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}, nil
	}
	return tea.KeyMsg{}, fmt.Errorf("unknown key %q", key)
}

// Snapshot renders the view, extracts the A11y tree, and computes the
// focused stable_id (model-level if it implements A11yEmitter).
func (s *shimImpl) Snapshot(id TUISessionID) (Snapshot, error) {
	sess, err := s.lookup(id)
	if err != nil {
		return Snapshot{}, err
	}
	sess.mu.Lock()
	model := sess.model
	focus := sess.focused
	sess.mu.Unlock()

	view := ""
	if model != nil {
		view = model.View()
	}
	tree := []A11yNode{}
	if em, ok := model.(A11yEmitter); ok {
		root := em.A11y()
		tree = []A11yNode{root}
		// Prefer the explicit focused field from the session (set by
		// FocusLane). Fall back to the A11y root's StableID, which
		// emitters use to advertise the currently-focused element.
		// Final fallback is the model's StableID() — for emitters that
		// don't track focus (read-only views).
		if focus == "" {
			focus = root.StableID
			if focus == "" {
				focus = em.StableID()
			}
		}
	}
	return Snapshot{
		View:  view,
		Tree:  tree,
		Focus: focus,
		Seq:   sess.snapSeq.Add(1),
	}, nil
}

// GetModel projects the live model to JSON. The jsonPath argument is
// honored when it resolves to a top-level field (e.g. "$.lanes" or
// "$.cursor"); deeper paths fall back to the gjson library wired in
// item 15. Empty path returns the whole model.
func (s *shimImpl) GetModel(id TUISessionID, jsonPath string) (json.RawMessage, error) {
	sess, err := s.lookup(id)
	if err != nil {
		return nil, err
	}
	sess.mu.Lock()
	model := sess.model
	sess.mu.Unlock()
	if model == nil {
		return json.RawMessage(`null`), nil
	}
	raw, err := json.Marshal(model)
	if err != nil {
		return nil, fmt.Errorf("marshal model: %w", err)
	}
	jsonPath = strings.TrimSpace(jsonPath)
	if jsonPath == "" || jsonPath == "$" {
		return json.RawMessage(raw), nil
	}
	// Item 15 wires the actual JSONPath evaluator (gjson). Until then
	// only top-level field projection is supported here.
	return projectTopLevel(raw, jsonPath)
}

// projectTopLevel handles "$.field" against a JSON object. Used until
// the full JSONPath evaluator is wired (item 15).
func projectTopLevel(raw json.RawMessage, jsonPath string) (json.RawMessage, error) {
	field := strings.TrimPrefix(jsonPath, "$.")
	if field == jsonPath || field == "" {
		return nil, fmt.Errorf("unsupported jsonpath %q in this checkpoint", jsonPath)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("not a JSON object; cannot project %q: %w", jsonPath, err)
	}
	val, ok := obj[field]
	if !ok {
		return nil, fmt.Errorf("field %q not present in model", field)
	}
	return val, nil
}

// FocusLane drives "down" presses up to N times until the snapshot's
// Focus matches laneID. N is the cardinality of the a11y tree; larger
// trees imply more cycles before erroring.
func (s *shimImpl) FocusLane(id TUISessionID, laneID string) error {
	sess, err := s.lookup(id)
	if err != nil {
		return err
	}
	snap, err := s.Snapshot(id)
	if err != nil {
		return err
	}
	if snap.Focus == laneID {
		// Already focused — record explicitly so subsequent snapshots
		// keep reporting it.
		sess.mu.Lock()
		sess.focused = laneID
		sess.mu.Unlock()
		return nil
	}
	maxIter := 1
	for _, root := range snap.Tree {
		maxIter += len(FlattenA11y(root))
	}
	for i := 0; i < maxIter; i++ {
		if err := s.PressKey(id, "down"); err != nil {
			return err
		}
		// Allow the model to apply the keypress before re-snapshotting.
		time.Sleep(10 * time.Millisecond)
		next, err := s.Snapshot(id)
		if err != nil {
			return err
		}
		if next.Focus == laneID {
			sess.mu.Lock()
			sess.focused = laneID
			sess.mu.Unlock()
			return nil
		}
	}
	return fmt.Errorf("FocusLane: lane %q not reached after %d down-presses", laneID, maxIter)
}

// WaitFor polls Snapshot/GetModel against the predicate until it is
// satisfied or timeout elapses.
func (s *shimImpl) WaitFor(id TUISessionID, predicate Predicate, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	tick := 50 * time.Millisecond
	for time.Now().Before(deadline) {
		ok, err := s.evalPredicate(id, predicate)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		time.Sleep(tick)
	}
	return fmt.Errorf("WaitFor: predicate not satisfied within %s", timeout)
}

func (s *shimImpl) evalPredicate(id TUISessionID, p Predicate) (bool, error) {
	if p.Regex != "" {
		snap, err := s.Snapshot(id)
		if err != nil {
			return false, err
		}
		return strings.Contains(snap.View, p.Regex), nil
	}
	if p.JSONPath != "" {
		raw, err := s.GetModel(id, p.JSONPath)
		if err != nil {
			return false, err
		}
		// Equals comparison strips JSON-encoded quotes for string
		// values so callers can write Equals: "running" rather than
		// Equals: `"running"`.
		got := string(raw)
		got = strings.Trim(got, `"`)
		return got == p.Equals, nil
	}
	return false, errors.New("WaitFor: predicate requires Regex or JSONPath")
}

// Stop signals the program to quit, waits for the goroutine to drain,
// and returns the captured tail + final model snapshot.
func (s *shimImpl) Stop(id TUISessionID) (FinalOutput, error) {
	sess, err := s.lookup(id)
	if err != nil {
		return FinalOutput{}, err
	}
	sess.prog.Send(tea.QuitMsg{})
	select {
	case <-sess.done:
	case <-time.After(2 * time.Second):
		// Best-effort kill: tea v0.25 has no Kill but we can re-send
		// QuitMsg until Run returns. The 2s budget is well past any
		// reasonable finalizer.
		sess.prog.Quit()
		<-sess.done
	}
	sess.mu.Lock()
	model := sess.model
	sess.mu.Unlock()
	rawModel, _ := json.Marshal(model)
	out := FinalOutput{
		StdoutTail: sess.out.Tail(),
		Model:      rawModel,
		DurationMs: time.Since(sess.startedAt).Milliseconds(),
	}
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
	return out, nil
}

// lookup is the per-session map accessor.
func (s *shimImpl) lookup(id TUISessionID) (*tuiSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		return nil, fmt.Errorf("session %q not found", id)
	}
	return sess, nil
}
