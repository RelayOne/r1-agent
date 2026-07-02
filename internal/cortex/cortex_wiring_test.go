// cortex_wiring_test.go — regression tests for audit A010: MidturnNote
// must thread the conversation History and the Cortex-level Provider
// into every round-style Lobe's LobeInput, and daemon-style Lobes
// (whose Run blocks until ctx cancellation) must be excluded from the
// Round barrier so they cannot stall every midturn check at the full
// RoundDeadline.
package cortex

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/agentloop"
	"github.com/RelayOne/r1/internal/hub"
)

// captureInputLobe records every LobeInput it receives so tests can
// assert what the runner's buildInput actually populated.
type captureInputLobe struct {
	mu     sync.Mutex
	inputs []LobeInput
}

func (l *captureInputLobe) ID() string          { return "capture-input" }
func (l *captureInputLobe) Description() string { return "records LobeInput (test stub)" }
func (l *captureInputLobe) Kind() LobeKind      { return KindDeterministic }
func (l *captureInputLobe) Run(_ context.Context, in LobeInput) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.inputs = append(l.inputs, in)
	return nil
}

func (l *captureInputLobe) captured() []LobeInput {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]LobeInput, len(l.inputs))
	copy(out, l.inputs)
	return out
}

// daemonTestLobe mimics walkeeper/rulecheck: Run blocks until ctx is
// cancelled and declares RunStyleDaemon via the optional RunStyler
// interface.
type daemonTestLobe struct {
	startedOnce sync.Once
	started     chan struct{}
}

func newDaemonTestLobe() *daemonTestLobe {
	return &daemonTestLobe{started: make(chan struct{})}
}

func (l *daemonTestLobe) ID() string          { return "daemon-test" }
func (l *daemonTestLobe) Description() string { return "blocks until ctx done (test stub)" }
func (l *daemonTestLobe) Kind() LobeKind      { return KindDeterministic }
func (l *daemonTestLobe) RunStyle() RunStyle  { return RunStyleDaemon }
func (l *daemonTestLobe) Run(ctx context.Context, _ LobeInput) error {
	l.startedOnce.Do(func() { close(l.started) })
	<-ctx.Done()
	return nil
}

// Compile-time check: the daemon stub satisfies the optional interface
// the production daemon Lobes (walkeeper, rulecheck) implement.
var _ RunStyler = (*daemonTestLobe)(nil)

// TestMidturnNotePropagatesHistoryAndProvider is the core A010
// regression: before the fix, buildInput left History and Provider nil
// on every round, so History-driven Lobes (memoryrecall, antitrunc)
// were unconditional no-ops in production.
func TestMidturnNotePropagatesHistoryAndProvider(t *testing.T) {
	t.Parallel()

	bus := hub.New()
	lobe := &captureInputLobe{}
	prov := &startStopProvider{}

	c, err := New(Config{
		EventBus:        bus,
		Provider:        prov,
		Lobes:           []Lobe{lobe},
		PreWarmInterval: time.Hour,
		RoundDeadline:   60 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = c.Stop(context.Background()) }()

	messages := []agentloop.Message{
		{
			Role: "user",
			Content: []agentloop.ContentBlock{
				{Type: "text", Text: "please deploy the staging build"},
			},
		},
		{
			Role: "assistant",
			Content: []agentloop.ContentBlock{
				{Type: "text", Text: "on it"},
			},
		},
	}

	_ = c.MidturnNote(messages, 3)

	got := lobe.captured()
	if len(got) != 1 {
		t.Fatalf("captured %d LobeInputs, want 1", len(got))
	}
	in := got[0]

	if in.Round != 1 {
		t.Errorf("LobeInput.Round = %d, want 1", in.Round)
	}
	if len(in.History) != 2 {
		t.Fatalf("LobeInput.History len = %d, want 2 (history not propagated — A010 regression)", len(in.History))
	}
	if in.History[0].Content[0].Text != "please deploy the staging build" {
		t.Errorf("History[0] text = %q, want the user message", in.History[0].Content[0].Text)
	}
	if in.Provider == nil {
		t.Fatalf("LobeInput.Provider is nil, want the Cortex-level provider (A010 regression)")
	}
	if p, ok := in.Provider.(*startStopProvider); !ok || p != prov {
		t.Errorf("LobeInput.Provider = %#v, want the exact cfg.Provider instance", in.Provider)
	}
	if in.Workspace == nil {
		t.Errorf("LobeInput.Workspace is nil, want read-only adapter")
	}
	if in.Bus == nil {
		t.Errorf("LobeInput.Bus is nil, want the event bus")
	}

	// Deep-copy contract: mutating the caller's slice after MidturnNote
	// must not leak into the Lobe's stashed History.
	messages[0].Content[0].Text = "MUTATED"
	if in.History[0].Content[0].Text != "please deploy the staging build" {
		t.Errorf("History aliases the caller's slice; want a deep copy")
	}
}

// TestMidturnNoteExcludesDaemonLobesFromBarrier asserts the second half
// of A010: a daemon-style Lobe (Run blocks until ctx done, like
// walkeeper/rulecheck) must not count as a Round participant. Before
// the fix, Round.Open expected it, Done never fired, and every
// MidturnNote burned the full RoundDeadline.
func TestMidturnNoteExcludesDaemonLobesFromBarrier(t *testing.T) {
	t.Parallel()

	bus := hub.New()
	w := NewWorkspace(bus, nil)

	daemon := newDaemonTestLobe()
	noter := newMidturnLobe("fast-noter", SevWarning, "round-note", w)

	c, err := New(Config{
		EventBus:        bus,
		Provider:        &startStopProvider{},
		Lobes:           []Lobe{daemon, noter},
		Workspace:       w,
		PreWarmInterval: time.Hour,
		RoundDeadline:   10 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = c.Stop(context.Background()) }()

	// Daemon Lobes start their long-lived Run at Cortex.Start, without
	// waiting for a round tick.
	select {
	case <-daemon.started:
	case <-time.After(5 * time.Second):
		t.Fatalf("daemon lobe Run did not start at Cortex.Start")
	}

	start := time.Now()
	out := c.MidturnNote([]agentloop.Message{}, 0)
	elapsed := time.Since(start)

	// Well under the 10s RoundDeadline: the barrier must only wait on
	// the round-style Lobe. Generous 5s bound absorbs CI jitter while
	// still failing decisively if the daemon re-enters the barrier.
	if elapsed >= 5*time.Second {
		t.Fatalf("MidturnNote took %v; daemon lobe is stalling the Round barrier (A010 regression)", elapsed)
	}
	if !strings.Contains(out, "round-note") {
		t.Errorf("MidturnNote output missing the round lobe's note: %q", out)
	}

	// Both Lobes still surface in LobeStatus, in registration order.
	infos := c.LobeStatus()
	if len(infos) != 2 || infos[0].ID != "daemon-test" || infos[1].ID != "fast-noter" {
		t.Errorf("LobeStatus = %+v, want [daemon-test fast-noter]", infos)
	}
}
