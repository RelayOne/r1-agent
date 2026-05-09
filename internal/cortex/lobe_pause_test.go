package cortex

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// fakePauseLobe records each Run invocation. KindDeterministic so the
// runner does not bind against a semaphore.
type fakePauseLobe struct {
	calls atomic.Int64
}

func (f *fakePauseLobe) ID() string                              { return "fake-pause" }
func (f *fakePauseLobe) Description() string                     { return "test fake" }
func (f *fakePauseLobe) Kind() LobeKind                          { return KindDeterministic }
func (f *fakePauseLobe) Run(_ context.Context, _ LobeInput) error { f.calls.Add(1); return nil }

// TestLobeRunner_PauseSkipsRun confirms SetPaused(true) makes the
// runner skip the underlying lobe.Run call while still consuming
// ticks. The deferred signalDone fires regardless so no Round.Wait
// caller hangs.
func TestLobeRunner_PauseSkipsRun(t *testing.T) {
	t.Parallel()
	fake := &fakePauseLobe{}
	r := NewLobeRunner(fake, nil, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx)
	defer r.Stop(context.Background())

	// First tick: not paused, lobe.Run fires once.
	r.Tick() <- struct{}{}

	// Poll for first call to land. 2s deadline is plenty for a deterministic
	// no-op lobe that only does atomic.Add.
	deadline := time.Now().Add(2 * time.Second)
	for fake.calls.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := fake.calls.Load(); got != 1 {
		t.Fatalf("after first tick: calls = %d, want 1", got)
	}

	// Pause and tick: lobe.Run must NOT fire. Wait briefly to give the
	// runner a chance to consume the tick and short-circuit.
	r.SetPaused(true)
	if !r.IsPaused() {
		t.Errorf("IsPaused = false after SetPaused(true)")
	}
	r.Tick() <- struct{}{}
	time.Sleep(50 * time.Millisecond)
	if got := fake.calls.Load(); got != 1 {
		t.Errorf("after paused tick: calls = %d, want 1 (Run must not fire while paused)", got)
	}

	// Resume and tick: lobe.Run fires again.
	r.SetPaused(false)
	if r.IsPaused() {
		t.Errorf("IsPaused = true after SetPaused(false)")
	}
	r.Tick() <- struct{}{}
	deadline = time.Now().Add(2 * time.Second)
	for fake.calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := fake.calls.Load(); got != 2 {
		t.Errorf("after resume tick: calls = %d, want 2", got)
	}
}

// TestLobeRunner_LobeID confirms the LobeID() getter returns the
// underlying Lobe.ID().
func TestLobeRunner_LobeID(t *testing.T) {
	t.Parallel()
	r := NewLobeRunner(&fakePauseLobe{}, nil, nil, nil)
	if got := r.LobeID(); got != "fake-pause" {
		t.Errorf("LobeID() = %q, want fake-pause", got)
	}
}
