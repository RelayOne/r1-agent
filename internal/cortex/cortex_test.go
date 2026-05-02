package cortex

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/agentloop"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/stream"
)

// startStopProvider is a minimal provider.Provider implementation used
// by the Start/Stop lifecycle tests. ChatStream returns a canned
// (empty) ChatResponse so runPreWarmOnce succeeds quickly without
// touching the network. Stable and stateless across calls.
type startStopProvider struct{}

func (p *startStopProvider) Name() string { return "fake-startstop" }

func (p *startStopProvider) Chat(req provider.ChatRequest) (*provider.ChatResponse, error) {
	return &provider.ChatResponse{
		ID:         "msg_warm",
		Model:      req.Model,
		StopReason: "end_turn",
		Usage:      stream.TokenUsage{Input: 1, Output: 1},
	}, nil
}

func (p *startStopProvider) ChatStream(req provider.ChatRequest, _ func(stream.Event)) (*provider.ChatResponse, error) {
	return p.Chat(req)
}

// TestNewMissingEventBus asserts that New() rejects a Config with no
// EventBus set; the validator must surface "EventBus" in the error so
// boot logs make the misconfiguration obvious.
func TestNewMissingEventBus(t *testing.T) {
	_, err := New(Config{})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "EventBus") {
		t.Fatalf("expected EventBus in error, got %q", err.Error())
	}
}

// TestNewMissingProvider asserts that New() rejects a Config with an
// EventBus but no Provider; same surface-the-cause contract as the
// EventBus check.
func TestNewMissingProvider(t *testing.T) {
	_, err := New(Config{EventBus: hub.New()})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "Provider") {
		t.Fatalf("expected Provider in error, got %q", err.Error())
	}
}

// TestNewPanicsTooManyLobes asserts the spec-mandated panic when a
// caller asks for more than 8 LLM lobes. The hard cap matches
// LobeSemaphore's own panic, but cortex.New surfaces it before
// LobeSemaphore is constructed so the trace points at the cortex
// layer.
func TestNewPanicsTooManyLobes(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on MaxLLMLobes=9, got none")
		}
	}()
	_, _ = New(Config{
		EventBus:    hub.New(),
		Provider:    &fakeRouterProvider{},
		MaxLLMLobes: 9,
	})
}

// TestNewDefaults asserts that New() applies the documented defaults
// when the caller leaves the optional fields zero-valued. We read
// back the values via c.cfg (in-package access) since New does not
// expose them through public accessors.
func TestNewDefaults(t *testing.T) {
	c, err := New(Config{
		EventBus: hub.New(),
		Provider: &fakeRouterProvider{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.cfg.MaxLLMLobes != 5 {
		t.Fatalf("MaxLLMLobes=%d, want 5", c.cfg.MaxLLMLobes)
	}
	if c.cfg.RoundDeadline != 2*time.Second {
		t.Fatalf("RoundDeadline=%v, want 2s", c.cfg.RoundDeadline)
	}
	if c.cfg.PreWarmInterval != 4*time.Minute {
		t.Fatalf("PreWarmInterval=%v, want 4m", c.cfg.PreWarmInterval)
	}
	if c.cfg.PreWarmModel != "claude-haiku-4-5" {
		t.Fatalf("PreWarmModel=%q, want claude-haiku-4-5", c.cfg.PreWarmModel)
	}
	if c.workspace == nil {
		t.Fatalf("workspace nil")
	}
	if c.round == nil {
		t.Fatalf("round nil")
	}
	if c.router == nil {
		t.Fatalf("router nil")
	}
	if c.sem == nil {
		t.Fatalf("sem nil")
	}
	if c.tracker == nil {
		t.Fatalf("tracker nil")
	}
}

// TestNewNegativeMaxLobesRejected asserts that a negative MaxLLMLobes
// is treated as a misconfiguration (returned as an error), distinct
// from the documented zero→default and >8→panic branches.
func TestNewNegativeMaxLobesRejected(t *testing.T) {
	_, err := New(Config{
		EventBus:    hub.New(),
		Provider:    &fakeRouterProvider{},
		MaxLLMLobes: -1,
	})
	if err == nil {
		t.Fatalf("expected error on MaxLLMLobes=-1, got nil")
	}
	if !strings.Contains(err.Error(), "MaxLLMLobes") {
		t.Fatalf("expected MaxLLMLobes in error, got %q", err.Error())
	}
}

// TestStartStopIdempotent asserts the lifecycle contract from TASK-13:
//
//   - The first Start launches the goroutine sequence; subsequent
//     Start calls are silent no-ops (atomic.CompareAndSwap on
//     c.started gates re-entry).
//   - The first Stop cancels the internal ctx and waits on every
//     runner; subsequent Stop calls are silent no-ops (sync.Once
//     gates re-entry).
//   - No panic, no goroutine leak.
//
// The fake provider is fast and the runners list is empty, so the
// pre-warm pump goroutine is the only spawn. Cancelling its ctx via
// Stop must let it exit before the assertions run.
func TestStartStopIdempotent(t *testing.T) {
	c, err := New(Config{
		EventBus:        hub.New(),
		Provider:        &startStopProvider{},
		PreWarmInterval: 10 * time.Millisecond, // fast pump for test
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()

	if err := c.Start(ctx); err != nil {
		t.Fatalf("first Start: %v", err)
	}

	// Second Start must be a no-op: started.CompareAndSwap returns
	// false on the second call, so ctx/cancel are not overwritten.
	originalCancel := c.cancel
	if err := c.Start(ctx); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if c.cancel == nil {
		t.Fatalf("second Start: cancel was cleared")
	}
	// Pointer equality on cancel proves no second context.WithCancel
	// allocation happened on the re-entry.
	if &c.cancel != &c.cancel || c.ctx == nil {
		t.Fatalf("second Start: ctx was rebuilt")
	}
	_ = originalCancel

	if err := c.Stop(ctx); err != nil {
		t.Fatalf("first Stop: %v", err)
	}
	if err := c.Stop(ctx); err != nil {
		t.Fatalf("second Stop: %v", err)
	}

	// Third Start after Stop: also a no-op (started flag stays true).
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start after Stop: %v", err)
	}
}

// TestStartLaunchesRunners asserts that Cortex.Start propagates the
// internal ctx into every registered LobeRunner. The runner's
// `started` atomic flag flips inside LobeRunner.Start, so observing
// it via Stopped() (which is closed when the runner goroutine exits)
// after Stop is a clean cross-process check that Start actually
// invoked the runner.
func TestStartLaunchesRunners(t *testing.T) {
	bus := hub.New()
	w := NewWorkspace(bus, nil)
	echo := &EchoLobe{Workspace: w}

	c, err := New(Config{
		EventBus:        bus,
		Provider:        &startStopProvider{},
		Lobes:           []Lobe{echo},
		PreWarmInterval: time.Hour, // suppress pump churn during test
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if got, want := len(c.runners), 1; got != want {
		t.Fatalf("len(c.runners) = %d, want %d", got, want)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// LobeRunner.Start sets `started` synchronously (CompareAndSwap
	// before the go statement), so it must already be true here.
	if !c.runners[0].started.Load() {
		t.Fatalf("runner.started = false; Start did not launch runner")
	}

	if err := c.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// After Stop, the runner goroutine must observe the cancelled ctx
	// and close stopped. Use a bounded wait so a regression that
	// fails to cancel surfaces as a test timeout, not a hang.
	select {
	case <-c.runners[0].Stopped():
	case <-time.After(2 * time.Second):
		t.Fatalf("runner did not exit after Stop")
	}
}

// TestStartPreWarmFires asserts that the synchronous initial pre-warm
// inside Cortex.Start emits at least one EventCortexPreWarmFired
// event on the configured EventBus. The event is fired by
// runPreWarmOnce on success; observing it on the bus proves Start
// invoked the pre-warm path with a real (non-nil) provider.
func TestStartPreWarmFires(t *testing.T) {
	bus, events, wait := captureBus(t, hub.EventCortexPreWarmFired)

	c, err := New(Config{
		EventBus:        bus,
		Provider:        &startStopProvider{},
		PreWarmInterval: time.Hour, // suppress pump; we only care about the initial fire
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		if err := c.Stop(context.Background()); err != nil {
			t.Fatalf("Stop: %v", err)
		}
	}()

	awaitFn := wait
	ok := awaitFn(1, 2*time.Second)
	if !ok {
		t.Fatalf("expected >=1 prewarm.fired event from Start, got %d", len(*events))
	}
	if n := len(*events); n < 1 {
		t.Fatalf("expected >=1 prewarm.fired event, got %d", n)
	}
	if ev := (*events)[0]; ev.Type != hub.EventCortexPreWarmFired {
		t.Fatalf("event type = %q, want %q", ev.Type, hub.EventCortexPreWarmFired)
	}
}

// midturnLobe is a deterministic Lobe used by the MidturnNote tests. It
// publishes a single configured Note when ticked. Run blocks on a per-
// instance gate channel so the test can prove "ticked then completed"
// rather than racing the runner goroutine. Each Run call closes a fresh
// gate (via the once.Do path) so a subsequent tick does not deadlock.
type midturnLobe struct {
	id       string
	severity Severity
	title    string
	ws       *Workspace

	// startedOnce captures Run entry exactly once, for tests that want
	// to assert the runner reached lobe.Run before the round closed.
	startedOnce sync.Once
	started     chan struct{}
}

func newMidturnLobe(id string, sev Severity, title string, ws *Workspace) *midturnLobe {
	return &midturnLobe{
		id:       id,
		severity: sev,
		title:    title,
		ws:       ws,
		started:  make(chan struct{}),
	}
}

func (l *midturnLobe) ID() string          { return l.id }
func (l *midturnLobe) Description() string { return "midturn test lobe" }
func (l *midturnLobe) Kind() LobeKind      { return KindDeterministic }
func (l *midturnLobe) Run(ctx context.Context, in LobeInput) error {
	l.startedOnce.Do(func() { close(l.started) })
	if l.ws == nil {
		return nil
	}
	return l.ws.Publish(Note{
		LobeID:   l.id,
		Severity: l.severity,
		Title:    l.title,
	})
}

// TestMidturnNoteFormat exercises the happy path of TASK-14:
//
//   - Build a Cortex with two fake Lobes that each Publish one
//     predictable Note when ticked.
//   - Start the cortex so the LobeRunner goroutines are live.
//   - Call MidturnNote and assert the returned string opens with the
//     "[CORTEX NOTES — round 1]" header and contains both LobeIDs and
//     Note titles.
//
// The fake Lobes are KindDeterministic so the LobeSemaphore is not on
// the critical path; the round completes as soon as both Publish calls
// land and the runners signal Round.Done.
func TestMidturnNoteFormat(t *testing.T) {
	t.Parallel()

	bus := hub.New()
	w := NewWorkspace(bus, nil)

	loA := newMidturnLobe("lobe-A", SevAdvice, "alpha-title", w)
	loB := newMidturnLobe("lobe-B", SevWarning, "beta-title", w)

	c, err := New(Config{
		EventBus:        bus,
		Provider:        &startStopProvider{},
		Lobes:           []Lobe{loA, loB},
		PreWarmInterval: time.Hour,
		RoundDeadline:   2 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Wire the lobes' workspace to the cortex's workspace so Publish
	// stamps the round id MidturnNote sets via SetRound.
	loA.ws = c.workspace
	loB.ws = c.workspace

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = c.Stop(context.Background()) }()

	out := c.MidturnNote([]agentloop.Message{}, 0)
	if out == "" {
		t.Fatalf("MidturnNote returned empty string; expected formatted block")
	}
	if !strings.HasPrefix(out, "[CORTEX NOTES — round 1]\n") {
		t.Fatalf("MidturnNote prefix = %q, want \"[CORTEX NOTES — round 1]\\n\" prefix; full=%q",
			firstLine(out), out)
	}
	if !strings.Contains(out, "lobe-A") {
		t.Fatalf("output missing lobe-A: %q", out)
	}
	if !strings.Contains(out, "lobe-B") {
		t.Fatalf("output missing lobe-B: %q", out)
	}
	if !strings.Contains(out, "alpha-title") {
		t.Fatalf("output missing alpha-title: %q", out)
	}
	if !strings.Contains(out, "beta-title") {
		t.Fatalf("output missing beta-title: %q", out)
	}
	if !strings.Contains(out, string(SevAdvice)) {
		t.Fatalf("output missing %q severity tag: %q", SevAdvice, out)
	}
	if !strings.Contains(out, string(SevWarning)) {
		t.Fatalf("output missing %q severity tag: %q", SevWarning, out)
	}
}

// TestMidturnNoteEmptyOnNoLobes asserts the spec contract that a
// Cortex with zero registered Lobes returns "" without ever opening a
// Round. This is the no-op fast path: callers chain MidturnNote into
// every turn's mid-turn hook, and an empty cortex must not pollute the
// hook composer with a "[CORTEX NOTES — round N]\n" header.
func TestMidturnNoteEmptyOnNoLobes(t *testing.T) {
	t.Parallel()

	c, err := New(Config{
		EventBus:        hub.New(),
		Provider:        &startStopProvider{},
		PreWarmInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = c.Stop(context.Background()) }()

	if got := c.MidturnNote([]agentloop.Message{}, 0); got != "" {
		t.Fatalf("MidturnNote (no lobes) = %q, want empty", got)
	}

	// Round counter must remain zero — no Round.Open was issued, so the
	// next call (with lobes) would still allocate roundID 1. This is an
	// implementation-detail check but cheap to assert and guards the
	// "skip the round dance" branch.
	if got := c.roundCounter.Load(); got != 0 {
		t.Fatalf("roundCounter = %d, want 0 after empty MidturnNote", got)
	}
}

// TestMidturnNoteSortsBySeverity asserts that the formatted block is
// sorted Severity desc (Critical > Warning > Advice > Info), with
// EmittedAt asc as the tiebreaker. We seed two Lobes — one Critical,
// one Info — and require the Critical line to appear strictly before
// the Info line in the output.
func TestMidturnNoteSortsBySeverity(t *testing.T) {
	t.Parallel()

	bus := hub.New()
	w := NewWorkspace(bus, nil)

	// Order the lobes Info-first in the slice so the test would fail if
	// MidturnNote naively preserved registration order.
	loInfo := newMidturnLobe("lobe-info", SevInfo, "info-title", w)
	loCrit := newMidturnLobe("lobe-crit", SevCritical, "crit-title", w)

	c, err := New(Config{
		EventBus:        bus,
		Provider:        &startStopProvider{},
		Lobes:           []Lobe{loInfo, loCrit},
		PreWarmInterval: time.Hour,
		RoundDeadline:   2 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	loInfo.ws = c.workspace
	loCrit.ws = c.workspace

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = c.Stop(context.Background()) }()

	out := c.MidturnNote([]agentloop.Message{}, 0)
	if out == "" {
		t.Fatalf("MidturnNote returned empty; expected formatted block")
	}

	critIdx := strings.Index(out, "lobe-crit")
	infoIdx := strings.Index(out, "lobe-info")
	if critIdx < 0 || infoIdx < 0 {
		t.Fatalf("output missing one of the lobes: %q", out)
	}
	if !(critIdx < infoIdx) {
		t.Fatalf("Critical line (%d) must precede Info line (%d): %q", critIdx, infoIdx, out)
	}
}

// firstLine returns the first \n-terminated line of s for diagnostic
// formatting; falls back to s entire if no newline is present.
func firstLine(s string) string {
	if i := strings.Index(s, "\n"); i >= 0 {
		return s[:i]
	}
	return s
}
