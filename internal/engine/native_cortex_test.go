package engine

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/agentloop"
	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/stream"
)

// fakeCortexProvider is a no-network provider used to satisfy cortex.New's
// non-nil Provider requirement (the Router / pre-warm pump). It is never
// expected to be called in these deterministic-lobe tests.
type fakeCortexProvider struct{}

func (p *fakeCortexProvider) Name() string { return "fake-cortex" }
func (p *fakeCortexProvider) Chat(req provider.ChatRequest) (*provider.ChatResponse, error) {
	return &provider.ChatResponse{ID: "msg", Model: req.Model, StopReason: "end_turn", Usage: stream.TokenUsage{Input: 1, Output: 1}}, nil
}
func (p *fakeCortexProvider) ChatStream(req provider.ChatRequest, _ func(stream.Event)) (*provider.ChatResponse, error) {
	return p.Chat(req)
}

// noteLobe publishes one Note (of a configurable severity/title) to the
// Workspace it captured at construction, on every Run tick.
type noteLobe struct {
	id    string
	sev   cortex.Severity
	title string
	ws    *cortex.Workspace
}

func (l *noteLobe) ID() string             { return l.id }
func (l *noteLobe) Description() string     { return "note test lobe" }
func (l *noteLobe) Kind() cortex.LobeKind   { return cortex.KindDeterministic }
func (l *noteLobe) Run(_ context.Context, _ cortex.LobeInput) error {
	return l.ws.Publish(cortex.Note{LobeID: l.id, Severity: l.sev, Title: l.title})
}

// wedgedLobe never finishes its round normally — it blocks until ctx is
// cancelled. Used to prove the RoundDeadline upper bound.
type wedgedLobe struct{ started chan struct{}; once sync.Once }

func (l *wedgedLobe) ID() string           { return "wedged" }
func (l *wedgedLobe) Description() string   { return "wedged test lobe" }
func (l *wedgedLobe) Kind() cortex.LobeKind { return cortex.KindDeterministic }
func (l *wedgedLobe) Run(ctx context.Context, _ cortex.LobeInput) error {
	l.once.Do(func() { close(l.started) })
	<-ctx.Done()
	return ctx.Err()
}

// TestNativeCortex_InjectedWorkspaceSurfacesNotes is the regression guard for
// the two-workspace bug: the deterministic lobes capture a Workspace at
// construction, and the Cortex that drains via MidturnNote MUST share that same
// Workspace (passed through cortex.Config.Workspace, as buildDeterministicCortex
// does). Without the injection, the Cortex would allocate its own Workspace and
// MidturnNote would return "" even though the lobe published a Note.
func TestNativeCortex_InjectedWorkspaceSurfacesNotes(t *testing.T) {
	bus := hub.New()
	ws := cortex.NewWorkspace(bus, nil)
	lobe := &noteLobe{id: "note-lobe", sev: cortex.SevWarning, title: "alpha-title", ws: ws}

	c, err := cortex.New(cortex.Config{
		EventBus:        bus,
		Provider:        &fakeCortexProvider{},
		Lobes:           []cortex.Lobe{lobe},
		Workspace:       ws, // <-- the fix: lobes + cortex share ONE workspace
		PreWarmInterval: time.Hour,
		RoundDeadline:   60 * time.Second,
	})
	if err != nil {
		t.Fatalf("cortex.New: %v", err)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	out := c.MidturnNote([]agentloop.Message{}, 0)
	if out == "" {
		t.Fatal("MidturnNote returned empty — the lobe's Note did not surface (two-workspace bug)")
	}
	if !strings.Contains(out, "note-lobe") || !strings.Contains(out, "alpha-title") {
		t.Fatalf("MidturnNote missing lobe id/title; got: %q", out)
	}
}

// TestNativeCortex_PreEndTurnGateBlocks proves an unresolved critical Note in
// the shared Workspace makes PreEndTurnGate refuse end_turn (non-empty), and a
// resolving Note flips it back to "".
func TestNativeCortex_PreEndTurnGateBlocks(t *testing.T) {
	bus := hub.New()
	ws := cortex.NewWorkspace(bus, nil)
	c, err := cortex.New(cortex.Config{
		EventBus:        bus,
		Provider:        &fakeCortexProvider{},
		Workspace:       ws,
		PreWarmInterval: time.Hour,
		RoundDeadline:   60 * time.Second,
	})
	if err != nil {
		t.Fatalf("cortex.New: %v", err)
	}
	if err := c.Workspace().Publish(cortex.Note{LobeID: "verify", Severity: cortex.SevCritical, Title: "build failed"}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if got := c.PreEndTurnGate([]agentloop.Message{}); got == "" {
		t.Fatal("PreEndTurnGate returned empty with an unresolved critical Note; expected a block")
	} else if !strings.Contains(strings.ToUpper(got), "CRITICAL") {
		t.Fatalf("PreEndTurnGate block missing CRITICAL marker; got: %q", got)
	}
	// Resolve it: the gate must clear.
	critID := c.Workspace().Snapshot()[0].ID
	if err := c.Workspace().Publish(cortex.Note{LobeID: "verify", Severity: cortex.SevInfo, Title: "resolved", Resolves: critID}); err != nil {
		t.Fatalf("publish resolve: %v", err)
	}
	if got := c.PreEndTurnGate([]agentloop.Message{}); got != "" {
		t.Fatalf("PreEndTurnGate still blocking after resolve; got: %q", got)
	}
}

// TestNativeCortex_BuildDeterministicConstructs proves buildDeterministicCortex
// (the native_runner activation helper) constructs a working Cortex from the 4
// real deterministic lobe constructors, starts/stops cleanly, and MidturnNote
// returns promptly (bounded by RoundDeadline) rather than hanging.
func TestNativeCortex_BuildDeterministicConstructs(t *testing.T) {
	live := buildDeterministicCortex("S1", hub.New(), &fakeCortexProvider{}, "sys", nil, nil, nil)
	if live == nil {
		t.Fatal("buildDeterministicCortex returned nil for a valid config")
	}
	if err := live.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = live.Stop(context.Background()) })

	done := make(chan struct{})
	go func() { _ = live.MidturnNote([]agentloop.Message{}, 0); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("MidturnNote did not return within 10s — bounded-deadline safety violated")
	}
}

// TestNativeCortex_BoundedMidturn proves the default-on safety property: even
// when a lobe wedges (never finishes its round), MidturnNote returns within the
// RoundDeadline bound and never deadlocks the hot loop.
func TestNativeCortex_BoundedMidturn(t *testing.T) {
	bus := hub.New()
	ws := cortex.NewWorkspace(bus, nil)
	w := &wedgedLobe{started: make(chan struct{})}
	c, err := cortex.New(cortex.Config{
		EventBus:        bus,
		Provider:        &fakeCortexProvider{},
		Lobes:           []cortex.Lobe{w},
		Workspace:       ws,
		PreWarmInterval: time.Hour,
		RoundDeadline:   200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("cortex.New: %v", err)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = c.Stop(context.Background()) })

	start := time.Now()
	done := make(chan struct{})
	go func() { _ = c.MidturnNote([]agentloop.Message{}, 0); close(done) }()
	select {
	case <-done:
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("MidturnNote took %v with a 200ms RoundDeadline — deadline not enforced", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("MidturnNote hung on a wedged lobe — RoundDeadline upper bound not enforced")
	}
}
