//go:build !stoke_rod

// rod_stub.go: no-tag RodClient type.
//
// Under the default build (no stoke_rod tag), RodClient is an
// opaque struct with Backend-satisfying methods that all error out
// via ChromeLaunchFailedError. This lets callers reference
// browser.RodClient / browser.NewRodClient without the real go-rod
// library being linked in — the single-binary distribution story.
//
// The stoke_rod tag version of this struct lives in rod.go and
// holds a *Pool + launcher state. Same method set, different body.

package browser

import (
	"context"
	"errors"
)

// RodClient is the go-rod-backed Backend. Under the default build
// tag, it is an empty struct that exists only so callers compile
// unchanged — all methods return ChromeLaunchFailedError with a
// "rebuild with -tags stoke_rod" cause.
type RodClient struct{}

// Fetch is a stub that returns ChromeLaunchFailedError.
func (r *RodClient) Fetch(ctx context.Context, url string) (FetchResult, error) {
	return FetchResult{}, &ChromeLaunchFailedError{
		Cause: errors.New("stoke built without stoke_rod tag; rod.Fetch unavailable"),
	}
}

// RunActions is a stub that returns ChromeLaunchFailedError.
func (r *RodClient) RunActions(ctx context.Context, actions []Action) ([]ActionResult, error) {
	return nil, &ChromeLaunchFailedError{
		Cause: errors.New("stoke built without stoke_rod tag; rod.RunActions unavailable"),
	}
}

// Close is a no-op for the stub RodClient.
func (r *RodClient) Close() error { return nil }

// Compile-time assertion: even the stub satisfies Backend.
var _ Backend = (*RodClient)(nil)
