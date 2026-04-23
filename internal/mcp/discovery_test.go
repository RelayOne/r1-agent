package mcp

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestDiscoverHappy covers the happy path: server at
// /.well-known/mcp.json returns a well-formed payload whose
// transport matches the operator's ServerConfig.Transport.
func TestDiscoverHappy(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/mcp.json" {
			t.Errorf("unexpected probe path: %q", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"version":     "2025-11-25",
			"transport":   "streamable_http",
			"tools":       ["list_issues","create_issue"],
			"description": "GitHub MCP"
		}`))
	}))
	defer srv.Close()

	cfg := ServerConfig{
		Name:      "github",
		Transport: "streamable_http",
		URL:       srv.URL,
	}
	wk, err := Discover(context.Background(), cfg, srv.Client())
	if err != nil {
		t.Fatalf("Discover: unexpected error: %v", err)
	}
	if wk == nil {
		t.Fatal("Discover: wanted WellKnown, got nil")
	}
	if wk.Version != "2025-11-25" {
		t.Errorf("version: got %q want 2025-11-25", wk.Version)
	}
	if wk.Transport != "streamable_http" {
		t.Errorf("transport: got %q want streamable_http", wk.Transport)
	}
	if len(wk.Tools) != 2 || wk.Tools[0] != "list_issues" || wk.Tools[1] != "create_issue" {
		t.Errorf("tools: got %#v", wk.Tools)
	}
	if wk.Description != "GitHub MCP" {
		t.Errorf("description: got %q", wk.Description)
	}
}

// TestDiscoverHappyHyphenTransport exercises the normalization path:
// the server advertises "streamable-http" while the operator config
// uses "streamable_http". Both should match.
func TestDiscoverHappyHyphenTransport(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"transport":"streamable-http","tools":[]}`))
	}))
	defer srv.Close()

	cfg := ServerConfig{Name: "a", Transport: "streamable_http", URL: srv.URL}
	if _, err := Discover(context.Background(), cfg, srv.Client()); err != nil {
		t.Fatalf("hyphen-vs-underscore normalization failed: %v", err)
	}
}

// TestDiscover404 asserts a 404 well-known endpoint is non-fatal:
// (nil, nil) returned, no error surfaced.
func TestDiscover404(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	cfg := ServerConfig{Name: "none", Transport: "streamable_http", URL: srv.URL}
	wk, err := Discover(context.Background(), cfg, srv.Client())
	if err != nil {
		t.Fatalf("404 should be non-fatal, got err: %v", err)
	}
	if wk != nil {
		t.Fatalf("404 should yield nil WellKnown, got %+v", wk)
	}
}

// TestDiscoverTimeout uses a server that stalls forever, combined
// with a context deadline shorter than the 500ms floor, to assert
// timeouts are non-fatal.
func TestDiscoverTimeout(t *testing.T) {
	t.Parallel()

	stall := make(chan struct{})
	t.Cleanup(func() { close(stall) })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-stall:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	cfg := ServerConfig{Name: "slow", Transport: "streamable_http", URL: srv.URL}
	wk, err := Discover(ctx, cfg, srv.Client())
	if err != nil {
		t.Fatalf("timeout should be non-fatal, got err: %v", err)
	}
	if wk != nil {
		t.Fatalf("timeout should yield nil WellKnown, got %+v", wk)
	}
}

// TestDiscoverConnectionRefused asserts that a dead listener yields
// (nil, nil) — the canonical "server is gone" case.
func TestDiscoverConnectionRefused(t *testing.T) {
	t.Parallel()

	// Grab a port by listening, then close so the port is free.
	// The next dial to that address will refuse.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	cfg := ServerConfig{
		Name:      "dead",
		Transport: "streamable_http",
		URL:       "http://" + addr,
	}
	wk, err := Discover(context.Background(), cfg, &http.Client{Timeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatalf("connection refused should be non-fatal, got err: %v", err)
	}
	if wk != nil {
		t.Fatalf("connection refused should yield nil WellKnown, got %+v", wk)
	}
}

// TestDiscoverTransportMismatch asserts the cross-check fires: config
// says stdio-over-http was expected, server says sse.
func TestDiscoverTransportMismatch(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"transport":"sse","tools":["ping"]}`))
	}))
	defer srv.Close()

	cfg := ServerConfig{Name: "mismatch", Transport: "streamable_http", URL: srv.URL}
	wk, err := Discover(context.Background(), cfg, srv.Client())
	if err == nil {
		t.Fatalf("expected transport-mismatch error, got wk=%+v", wk)
	}
	if wk != nil {
		t.Fatalf("mismatch should not return a WellKnown, got %+v", wk)
	}
	if !strings.Contains(err.Error(), "transport mismatch") {
		t.Errorf("error missing 'transport mismatch' phrase: %v", err)
	}
	if !strings.Contains(err.Error(), "streamable_http") || !strings.Contains(err.Error(), "sse") {
		t.Errorf("error should mention both transports, got: %v", err)
	}
}

// TestDiscoverBadJSON asserts malformed payloads are a hard error —
// a 200 with non-JSON body is a broken / MITM'd server, not a
// "server doesn't publish a manifest" case.
func TestDiscoverBadJSON(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version": "2025-11-25", "transport": BROKEN`))
	}))
	defer srv.Close()

	cfg := ServerConfig{Name: "bad", Transport: "streamable_http", URL: srv.URL}
	wk, err := Discover(context.Background(), cfg, srv.Client())
	if err == nil {
		t.Fatalf("expected malformed-json error, got wk=%+v", wk)
	}
	if wk != nil {
		t.Fatalf("bad json should not return a WellKnown, got %+v", wk)
	}
	if !strings.Contains(err.Error(), "malformed mcp.json") {
		t.Errorf("error missing 'malformed mcp.json' phrase: %v", err)
	}
}

// TestDiscoverEmptyURL asserts stdio-style configs (no URL) skip
// cleanly rather than erroring.
func TestDiscoverEmptyURL(t *testing.T) {
	t.Parallel()

	cfg := ServerConfig{Name: "stdio", Transport: "stdio", URL: ""}
	wk, err := Discover(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("empty URL should be non-fatal, got err: %v", err)
	}
	if wk != nil {
		t.Fatalf("empty URL should yield nil WellKnown, got %+v", wk)
	}
}

// TestDiscoverServerAdvertisesNoTransport asserts the cross-check
// passes when the server omits the transport field (no opinion =
// no disagreement).
func TestDiscoverServerAdvertisesNoTransport(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"version":"2025-11-25","tools":["a"]}`))
	}))
	defer srv.Close()

	cfg := ServerConfig{Name: "x", Transport: "streamable_http", URL: srv.URL}
	wk, err := Discover(context.Background(), cfg, srv.Client())
	if err != nil {
		t.Fatalf("no-opinion transport should pass, got err: %v", err)
	}
	if wk == nil || wk.Version != "2025-11-25" {
		t.Fatalf("expected parsed WellKnown, got %+v", wk)
	}
}

// TestDiscoverBadURL ensures a malformed ServerConfig.URL is a hard
// error (this is an operator typo, not a network failure).
func TestDiscoverBadURL(t *testing.T) {
	t.Parallel()

	cfg := ServerConfig{Name: "typo", Transport: "streamable_http", URL: "not a url"}
	if _, err := Discover(context.Background(), cfg, nil); err == nil {
		t.Fatalf("expected error for malformed url")
	}
}

// TestDiscoverRespectsContextCancel asserts the request honors a
// caller-cancelled context (pre-flight cancel yields non-fatal).
func TestDiscoverRespectsContextCancel(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"transport":"streamable_http"}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cfg := ServerConfig{Name: "cancelled", Transport: "streamable_http", URL: srv.URL}
	wk, err := Discover(ctx, cfg, srv.Client())
	if err != nil {
		t.Fatalf("cancelled ctx should be non-fatal, got err: %v", err)
	}
	if wk != nil {
		t.Fatalf("cancelled ctx should yield nil WellKnown, got %+v", wk)
	}
}
