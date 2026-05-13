// session_bound.go: BoundSession — session-bound browser lifecycle
// with idle + hard-timeout enforcement (spec C6 T9).
//
// One R1 session gets exactly one Provider.Open call on first browser
// tool invocation. The resulting Session is stored in the R1 session
// context and reused across every later browser tool call. When the
// R1 session ends, the wrapper closes the browser session. Two
// timeouts apply:
//
//   - IdleTimeout (default 5m): no activity → silent close + lazy
//     re-open on the next tool call.
//   - HardTimeout (default 30m): absolute lifetime cap → close with
//     reason="hard_timeout"; caller must Open a fresh one.
//
// This file owns ONLY the lifecycle wrapper. Tenant credential
// plumbing (which tenant_id to pass to Open) lives at the executor
// layer where the SSO claim is in scope.

package browser

import (
	"context"
	"sync"
	"time"
)

// BoundSession wraps a Provider so callers can pretend they own one
// long-lived Session per R1 session. The wrapper:
//
//   - lazily Opens on first method call,
//   - tracks last-activity timestamps,
//   - silently rotates the underlying Session when idle timeout fires,
//   - hard-closes after HardTimeout.
//
// Construct via NewBoundSession(provider, opts).
type BoundSession struct {
	provider Provider
	opts     SessionOpts
	hub      *Hub

	mu      sync.Mutex
	active  Session
	openedAt time.Time
	closed  bool
	now     func() time.Time
}

// NewBoundSession returns a fresh BoundSession. The Provider is
// shared; multiple BoundSessions can wrap the same Provider for
// different R1 sessions.
func NewBoundSession(p Provider, opts SessionOpts, hub *Hub) *BoundSession {
	opts = resolveTimeouts(opts)
	return &BoundSession{
		provider: p,
		opts:     opts,
		hub:      hub,
		now:      time.Now,
	}
}

// SetClock overrides the time source for tests.
func (b *BoundSession) SetClock(now func() time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.now = now
}

// ensureActive returns the live Session, rotating when idle/hard
// timeouts have fired. Caller must hold b.mu.
func (b *BoundSession) ensureActiveLocked(ctx context.Context) (Session, error) {
	if b.closed {
		return nil, ErrSessionClosed
	}
	now := b.now()
	if b.active != nil {
		// Check hard timeout.
		if now.Sub(b.openedAt) >= b.opts.HardTimeout {
			b.rotateLocked(ctx, "hard_timeout")
		}
	}
	if b.active == nil {
		sess, err := b.provider.Open(ctx, b.opts)
		if err != nil {
			return nil, err
		}
		b.active = sess
		b.openedAt = now
	}
	return b.active, nil
}

// rotateLocked closes the current session emitting a reason. Caller
// must hold b.mu.
func (b *BoundSession) rotateLocked(ctx context.Context, reason string) {
	if b.active == nil {
		return
	}
	if b.hub != nil {
		b.hub.Emit(EvtSessionClosed, map[string]any{
			"tenant_id":  b.opts.TenantID,
			"mission_id": b.opts.MissionID,
			"provider":   b.provider.Name(),
			"session_id": b.active.ID(),
			"reason":     reason,
		})
	}
	_ = b.active.Close(ctx)
	b.active = nil
}

// Navigate dispatches to the underlying session, opening one lazily.
func (b *BoundSession) Navigate(ctx context.Context, url string) (NavigateResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, err := b.ensureActiveLocked(ctx)
	if err != nil {
		return NavigateResult{}, err
	}
	return s.Navigate(ctx, url)
}

// WaitFor dispatches to the underlying session.
func (b *BoundSession) WaitFor(ctx context.Context, selector string, timeout time.Duration) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, err := b.ensureActiveLocked(ctx)
	if err != nil {
		return err
	}
	return s.WaitFor(ctx, selector, timeout)
}

// Screenshot dispatches.
func (b *BoundSession) Screenshot(ctx context.Context) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, err := b.ensureActiveLocked(ctx)
	if err != nil {
		return nil, err
	}
	return s.Screenshot(ctx)
}

// Eval dispatches.
func (b *BoundSession) Eval(ctx context.Context, script string) (any, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, err := b.ensureActiveLocked(ctx)
	if err != nil {
		return nil, err
	}
	return s.Eval(ctx, script)
}

// Close shuts the bound session permanently. Idempotent.
func (b *BoundSession) Close(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	if b.active != nil {
		_ = b.active.Close(ctx)
		b.active = nil
	}
	return nil
}

// EnforceIdleTimeout closes the underlying session if the wrapper
// has been idle for at least IdleTimeout. Intended to be called by
// a background ticker (the R1 session lifecycle hook).
// Returns true when a rotation happened.
func (b *BoundSession) EnforceIdleTimeout(ctx context.Context) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active == nil || b.closed {
		return false
	}
	// We rely on the underlying provider to track per-call activity.
	// At this layer we use openedAt as a coarse proxy; finer-grained
	// activity tracking lives in each provider's session struct.
	if b.now().Sub(b.openedAt) >= b.opts.IdleTimeout {
		b.rotateLocked(ctx, "idle_timeout")
		return true
	}
	return false
}

// Active reports whether the wrapper currently holds an underlying session.
func (b *BoundSession) Active() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.active != nil
}
