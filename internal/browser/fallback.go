// fallback.go: FallbackProvider — primary + optional secondary.
//
// Spec C6 T8: when the primary's Open returns a transient error
// (ErrProviderUnreachable, WS dial timeouts), the wrapper retries
// against the secondary and emits browser.fallback_used. Permanent
// errors (ErrAuthFailed, ErrNoToken, ErrBudgetExhausted,
// NetworkDeniedError) DO NOT fall back — that would mask real
// configuration bugs.
//
// T8a: prod must explicitly opt into local-fallback via
// browser.fallback_allow_in_prod (enforced by config loader, not
// here — this file owns only the runtime behavior).

package browser

import (
	"context"
	"errors"
)

// FallbackProvider wraps a primary Provider with an optional
// secondary fallback. Construct via NewFallback; Name() returns the
// primary's name so cost / event attribution stays correct.
type FallbackProvider struct {
	primary   Provider
	secondary Provider // may be nil → no fallback
	hub       *Hub
}

// NewFallback constructs a FallbackProvider. secondary may be nil
// (the no-fallback shape); hub may be nil (no event emission).
func NewFallback(primary, secondary Provider, hub *Hub) *FallbackProvider {
	return &FallbackProvider{primary: primary, secondary: secondary, hub: hub}
}

// Name forwards to the primary.
func (f *FallbackProvider) Name() string { return f.primary.Name() }

// Open tries the primary first; on a transient error it tries the
// secondary and emits browser.fallback_used.
func (f *FallbackProvider) Open(ctx context.Context, opts SessionOpts) (Session, error) {
	sess, err := f.primary.Open(ctx, opts)
	if err == nil {
		return sess, nil
	}
	if !isTransientForFallback(err) || f.secondary == nil {
		return nil, err
	}
	if f.hub != nil {
		f.hub.Emit(EvtFallbackUsed, map[string]any{
			"tenant_id":      opts.TenantID,
			"mission_id":     opts.MissionID,
			"primary":        f.primary.Name(),
			"secondary":      f.secondary.Name(),
			"primary_error":  err.Error(),
		})
	}
	return f.secondary.Open(ctx, opts)
}

// Close closes both providers; returns the first error.
func (f *FallbackProvider) Close(ctx context.Context) error {
	var firstErr error
	if err := f.primary.Close(ctx); err != nil {
		firstErr = err
	}
	if f.secondary != nil {
		if err := f.secondary.Close(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// isTransientForFallback returns true when err is a class the
// fallback wrapper should route around. Auth + token + budget + the
// network-denied error are permanent — falling back would mask a
// real configuration issue.
func isTransientForFallback(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrProviderUnreachable) {
		return true
	}
	// Auth + token + budget + denial are permanent.
	if errors.Is(err, ErrAuthFailed) ||
		errors.Is(err, ErrNoToken) ||
		errors.Is(err, ErrNoTenantToken) ||
		errors.Is(err, ErrBudgetExhausted) ||
		errors.Is(err, ErrProviderClosed) ||
		IsNetworkDenied(err) {
		return false
	}
	// Default: treat as transient. Conservative — the wrapper is the
	// last-line redundancy; an unrecognized error that the primary
	// surfaced once is likely worth one retry against the secondary.
	return true
}

// Compile-time assertion.
var _ Provider = (*FallbackProvider)(nil)
