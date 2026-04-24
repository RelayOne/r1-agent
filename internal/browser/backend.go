// backend.go: Backend interface and no-tag RodClient stub.
//
// Backend is the executor-facing contract (spec 17 §3.3). Both the
// stdlib Client and the go-rod RodClient satisfy it; selection
// happens at construction time. This file is stdlib-only and builds
// in every configuration — the tag-gated real rod implementation
// lives in rod.go (//go:build stoke_rod).

package browser

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Backend is the executor-facing contract. Both the stdlib Client
// and the go-rod RodClient satisfy it.
type Backend interface {
	// Fetch is the stdlib-compatible path: GET url + extract text.
	// The rod backend implements Fetch by doing navigate + extract
	// against a real browser so JS-rendered pages work.
	Fetch(ctx context.Context, url string) (FetchResult, error)

	// RunActions executes the interactive action list in order.
	// The stdlib backend returns InteractiveUnsupportedError for any
	// action that requires a real browser (click / type / wait /
	// screenshot); the rod backend implements all of them.
	RunActions(ctx context.Context, actions []Action) ([]ActionResult, error)

	// Close releases resources (pool shutdown for rod; no-op for
	// http).
	Close() error
}

// RunActions is the stdlib-path implementation: navigate-only is
// handled via Fetch; every other kind is routed to
// InteractiveUnsupportedError so callers know to construct a RodClient.
//
// A lone navigate action does the fetch + returns a single result
// populated from FetchResult.FinalURL. Multiple navigates in one
// list are supported — each one refetches. An empty action list
// returns nil, nil (no-op).
func (c *Client) RunActions(ctx context.Context, actions []Action) ([]ActionResult, error) {
	if len(actions) == 0 {
		return nil, nil
	}
	out := make([]ActionResult, 0, len(actions))
	for _, a := range actions {
		if err := a.Validate(); err != nil {
			return out, err
		}
		if a.Kind != ActionNavigate {
			return out, &InteractiveUnsupportedError{Kind: a.Kind}
		}
		start := time.Now()
		fr, err := c.Fetch(ctx, a.URL)
		r := ActionResult{
			Kind:       ActionNavigate,
			DurationMs: time.Since(start).Milliseconds(),
		}
		if err != nil {
			r.OK = false
			r.Err = &NavigationFailedError{URL: a.URL, Cause: err}
		} else {
			r.OK = true
			r.URL = fr.FinalURL
			r.Text = fr.Text
		}
		out = append(out, r)
		if !r.OK {
			return out, r.Err
		}
	}
	return out, nil
}

// Close is a no-op for the stdlib client; added to satisfy Backend.
func (c *Client) Close() error { return nil }

// Compile-time assertion: *Client satisfies Backend.
var _ Backend = (*Client)(nil)

// RodConfig tunes the rod-backed RodClient. Zero values are
// sensible defaults (pool size 3, headless true, 30s timeout,
// CHROME_PATH env override). Declared here so callers can pass a
// config in either tag mode — the no-tag stub rejects construction
// but accepts the type.
type RodConfig struct {
	PoolSize     int           // default 3
	HeadlessMode bool          // default true
	Timeout      time.Duration // default 30s per action
	ChromePath   string        // override auto-download (honors CHROME_PATH env if empty)
	UserAgent    string        // optional override
	Logger       func(string, ...any)
}

// rodClientFactory is the concrete constructor implementation. It
// is set to the real rod factory when the stoke_rod build tag is
// present (rod.go) and to the stub factory otherwise (rod_stub.go).
//
// Kept as a package var rather than two const-dispatched functions
// so the tag-gated file can replace it in its init() without
// shadowing NewRodClient's signature.
var rodClientFactory func(RodConfig) (*RodClient, error) = stubRodFactory

// NewRodClient returns a Backend backed by the go-rod library.
// When Stoke is built without the stoke_rod tag, this returns
// (nil, ChromeLaunchFailedError) with a descriptive message — the
// caller is expected to either rebuild with the tag or fall back to
// the stdlib Client.
func NewRodClient(cfg RodConfig) (*RodClient, error) {
	if rodClientFactory == nil {
		return nil, &ChromeLaunchFailedError{
			Cause: errors.New("rodClientFactory not wired; rebuild with -tags stoke_rod"),
		}
	}
	return rodClientFactory(cfg)
}

// stubRodFactory is the default (no-tag) factory. Returns the clear
// rebuild-with-tag diagnostic. Replaced by init() in rod.go under
// the stoke_rod tag.
func stubRodFactory(cfg RodConfig) (*RodClient, error) {
	return nil, &ChromeLaunchFailedError{
		Cause: fmt.Errorf("interactive browser actions require the stoke_rod build tag; " +
			"rebuild with 'go build -tags stoke_rod ./cmd/stoke'"),
	}
}
