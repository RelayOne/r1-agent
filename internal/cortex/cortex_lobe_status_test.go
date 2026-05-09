package cortex

import (
	"context"
	"strings"
	"testing"

	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/provider"
	"github.com/RelayOne/r1/internal/stream"
)

// fakeStatusLobe is a minimal Lobe with configurable id/desc/kind so
// the test can register two distinct Lobes and assert LobeStatus
// preserves order + reflects per-runner pause state.
type fakeStatusLobe struct {
	id   string
	desc string
	kind LobeKind
}

func (f *fakeStatusLobe) ID() string                              { return f.id }
func (f *fakeStatusLobe) Description() string                     { return f.desc }
func (f *fakeStatusLobe) Kind() LobeKind                          { return f.kind }
func (f *fakeStatusLobe) Run(_ context.Context, _ LobeInput) error { return nil }

// stubProvider satisfies provider.Provider with the smallest possible
// surface — Cortex.New requires a non-nil Provider. Chat / ChatStream
// are never invoked because the test does not call cortex.Start
// (which is where the prewarm pump fires).
type stubProvider struct{}

func (stubProvider) Name() string { return "stub" }
func (stubProvider) Chat(_ provider.ChatRequest) (*provider.ChatResponse, error) {
	return &provider.ChatResponse{}, nil
}
func (stubProvider) ChatStream(_ provider.ChatRequest, _ func(stream.Event)) (*provider.ChatResponse, error) {
	return &provider.ChatResponse{}, nil
}

func TestCortex_LobeStatus_PauseResumeRoundtrip(t *testing.T) {
	t.Parallel()

	a := &fakeStatusLobe{id: "a-lobe", desc: "first", kind: KindDeterministic}
	b := &fakeStatusLobe{id: "b-lobe", desc: "second", kind: KindLLM}

	c, err := New(Config{
		EventBus: hub.New(),
		Provider: stubProvider{},
		Lobes:    []Lobe{a, b},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// LobeStatus before Start: order preserved, pause states all false.
	infos := c.LobeStatus()
	if len(infos) != 2 {
		t.Fatalf("LobeStatus len = %d, want 2", len(infos))
	}
	if infos[0].ID != "a-lobe" || infos[1].ID != "b-lobe" {
		t.Errorf("LobeStatus order = [%s, %s], want [a-lobe, b-lobe]", infos[0].ID, infos[1].ID)
	}
	if infos[0].Paused || infos[1].Paused {
		t.Errorf("Paused = [%v, %v], want [false, false]", infos[0].Paused, infos[1].Paused)
	}
	if infos[0].Description != "first" || infos[1].Description != "second" {
		t.Errorf("Description not propagated: %+v", infos)
	}

	// Pause a-lobe, confirm only that one flips.
	if err := c.PauseLobe("a-lobe"); err != nil {
		t.Fatalf("PauseLobe(a-lobe): %v", err)
	}
	infos = c.LobeStatus()
	if !infos[0].Paused {
		t.Errorf("a-lobe Paused = false after PauseLobe, want true")
	}
	if infos[1].Paused {
		t.Errorf("b-lobe Paused = true (collateral), want false")
	}

	// Resume a-lobe, confirm flip back.
	if err := c.ResumeLobe("a-lobe"); err != nil {
		t.Fatalf("ResumeLobe(a-lobe): %v", err)
	}
	infos = c.LobeStatus()
	if infos[0].Paused {
		t.Errorf("a-lobe Paused = true after ResumeLobe, want false")
	}

	// Unknown lobe id → error.
	err = c.PauseLobe("does-not-exist")
	if err == nil || !strings.Contains(err.Error(), "no Lobe with id") {
		t.Errorf("PauseLobe(unknown) err = %v, want 'no Lobe with id'", err)
	}
	err = c.ResumeLobe("does-not-exist")
	if err == nil || !strings.Contains(err.Error(), "no Lobe with id") {
		t.Errorf("ResumeLobe(unknown) err = %v, want 'no Lobe with id'", err)
	}
}
