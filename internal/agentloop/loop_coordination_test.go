package agentloop

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/RelayOne/r1/internal/provider"
)

// fakeCoordinator records Acquire/Release calls and returns a scripted acquire
// outcome.
type fakeCoordinator struct {
	acquired    bool
	acquireErr  error
	acquireN    atomic.Int32
	releaseN    atomic.Int32
	chatStarted *atomic.Int32
}

func (f *fakeCoordinator) Acquire(context.Context) (bool, error) {
	f.acquireN.Add(1)
	return f.acquired, f.acquireErr
}
func (f *fakeCoordinator) Release(context.Context) error {
	f.releaseN.Add(1)
	return nil
}

// countingProvider counts Chat calls so we can prove a declined coordination
// gate dispatches no work.
type countingProvider struct {
	mockProvider
	calls atomic.Int32
}

func (c *countingProvider) Chat(req provider.ChatRequest) (*provider.ChatResponse, error) {
	c.calls.Add(1)
	return c.mockProvider.Chat(req)
}

func TestLoopCoordination_DeclinedDispatchesNothing(t *testing.T) {
	prov := &countingProvider{}
	coord := &fakeCoordinator{acquired: false}
	loop := New(prov, Config{MaxTurns: 5, Coordinator: coord}, nil, nil)

	res, err := loop.Run(context.Background(), "do the thing")
	if err != nil {
		t.Fatalf("declined run must not error, got %v", err)
	}
	if res.StopReason != "coordination_declined" {
		t.Fatalf("stop reason = %q, want coordination_declined", res.StopReason)
	}
	if prov.calls.Load() != 0 {
		t.Fatalf("declined gate must dispatch no work, got %d Chat calls", prov.calls.Load())
	}
	if coord.acquireN.Load() != 1 {
		t.Fatalf("Acquire must be called once, got %d", coord.acquireN.Load())
	}
	if coord.releaseN.Load() != 0 {
		t.Fatalf("declined gate must not Release (nothing acquired), got %d", coord.releaseN.Load())
	}
}

func TestLoopCoordination_AcquiredReleasesAtEnd(t *testing.T) {
	// MaxTurns=0 means the turn loop body never runs — the provider is never
	// called — so we isolate the acquire/release bracketing.
	prov := &countingProvider{}
	coord := &fakeCoordinator{acquired: true}
	loop := New(prov, Config{MaxTurns: 0, Coordinator: coord}, nil, nil)

	if _, err := loop.Run(context.Background(), "go"); err != nil {
		t.Fatalf("run error: %v", err)
	}
	if coord.acquireN.Load() != 1 {
		t.Fatalf("Acquire must be called once, got %d", coord.acquireN.Load())
	}
	if coord.releaseN.Load() != 1 {
		t.Fatalf("acquired gate must Release exactly once at run end, got %d", coord.releaseN.Load())
	}
}

func TestLoopCoordination_AcquireErrorAborts(t *testing.T) {
	prov := &countingProvider{}
	coord := &fakeCoordinator{acquireErr: errors.New("boom")}
	loop := New(prov, Config{MaxTurns: 5, Coordinator: coord}, nil, nil)

	res, err := loop.Run(context.Background(), "go")
	if err == nil {
		t.Fatal("acquire error must abort the run")
	}
	if res.StopReason != "coordination_error" {
		t.Fatalf("stop reason = %q, want coordination_error", res.StopReason)
	}
	if prov.calls.Load() != 0 {
		t.Fatal("acquire error must dispatch no work")
	}
}
