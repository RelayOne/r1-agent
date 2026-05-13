// session_bound_test.go: BoundSession lifecycle tests (spec C6 T9).
// Timer-driven assertions use an injected clock so the test runs
// in milliseconds rather than minutes.

package browser

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestBoundSession_LazyOpen — first method call opens the session.
func TestBoundSession_LazyOpen(t *testing.T) {
	t.Parallel()
	p := &stubProvider{name: "browserless"}
	b := NewBoundSession(p, SessionOpts{TenantID: "t"}, NewHub())
	if b.Active() {
		t.Errorf("BoundSession should not be active before first call")
	}
	if _, err := b.Navigate(context.Background(), "http://x"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if p.openCalls.Load() != 1 {
		t.Errorf("provider Open calls = %d, want 1", p.openCalls.Load())
	}
}

// TestBoundSession_ReuseUntilHardTimeout — repeated method calls
// share one underlying Open until HardTimeout fires.
func TestBoundSession_ReuseUntilHardTimeout(t *testing.T) {
	t.Parallel()
	p := &stubProvider{name: "browserless"}
	hub := NewHub()
	var rotated atomic.Int64
	hub.Subscribe(func(name string, attrs EventAttrs) {
		if name == EvtSessionClosed && attrs["reason"] == "hard_timeout" {
			rotated.Add(1)
		}
	})
	b := NewBoundSession(p, SessionOpts{
		TenantID:    "t",
		IdleTimeout: 1 * time.Hour,
		HardTimeout: 100 * time.Millisecond,
	}, hub)

	clock := time.Now()
	b.SetClock(func() time.Time { return clock })

	// First Navigate opens.
	if _, err := b.Navigate(context.Background(), "http://x"); err != nil {
		t.Fatalf("Navigate1: %v", err)
	}
	// Second call within hard timeout reuses.
	if _, err := b.Navigate(context.Background(), "http://x"); err != nil {
		t.Fatalf("Navigate2: %v", err)
	}
	if p.openCalls.Load() != 1 {
		t.Errorf("after two calls within hard timeout, opens = %d, want 1", p.openCalls.Load())
	}
	// Advance clock past hard timeout.
	clock = clock.Add(200 * time.Millisecond)
	if _, err := b.Navigate(context.Background(), "http://x"); err != nil {
		t.Fatalf("Navigate3: %v", err)
	}
	if p.openCalls.Load() != 2 {
		t.Errorf("after hard timeout, opens = %d, want 2", p.openCalls.Load())
	}
	if rotated.Load() != 1 {
		t.Errorf("hard_timeout event count = %d, want 1", rotated.Load())
	}
}

// TestBoundSession_IdleTimeout — EnforceIdleTimeout rotates after the
// configured idle window.
func TestBoundSession_IdleTimeout(t *testing.T) {
	t.Parallel()
	p := &stubProvider{name: "browserless"}
	hub := NewHub()
	var rotated atomic.Int64
	hub.Subscribe(func(name string, attrs EventAttrs) {
		if name == EvtSessionClosed && attrs["reason"] == "idle_timeout" {
			rotated.Add(1)
		}
	})
	b := NewBoundSession(p, SessionOpts{
		TenantID:    "t",
		IdleTimeout: 50 * time.Millisecond,
		HardTimeout: 1 * time.Hour,
	}, hub)
	clock := time.Now()
	b.SetClock(func() time.Time { return clock })

	if _, err := b.Navigate(context.Background(), "http://x"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if got := b.EnforceIdleTimeout(context.Background()); got {
		t.Errorf("idle timeout fired prematurely")
	}
	clock = clock.Add(100 * time.Millisecond)
	if got := b.EnforceIdleTimeout(context.Background()); !got {
		t.Errorf("idle timeout did not fire after window elapsed")
	}
	if rotated.Load() != 1 {
		t.Errorf("idle_timeout event count = %d, want 1", rotated.Load())
	}
}

// TestBoundSession_CloseIsIdempotent — double Close is fine.
func TestBoundSession_CloseIsIdempotent(t *testing.T) {
	t.Parallel()
	p := &stubProvider{name: "browserless"}
	b := NewBoundSession(p, SessionOpts{TenantID: "t"}, NewHub())
	if _, err := b.Navigate(context.Background(), "http://x"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}
	if err := b.Close(context.Background()); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := b.Close(context.Background()); err != nil {
		t.Errorf("second Close: %v", err)
	}
}
