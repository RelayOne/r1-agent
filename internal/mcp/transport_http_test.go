// transport_http_test.go -- unit tests for the Streamable-HTTP
// transport with SSE fallthrough (MCP-4).
//
// These tests stand up httptest.Server instances that mimic the
// subset of the Streamable-HTTP / SSE wire protocol mcp-go speaks
// during initialize / tools/list / tools/call. They are not
// protocol-conformance tests; they exercise the glue code in
// transport_http.go.
//
// Covered:
//   * TestNewHTTPTransport_Validation -- NewHTTPTransport rejects
//     bad cfg inputs and accepts the documented transport spellings.
//   * TestHTTPTransport_StreamableHappyPath -- init + list + call
//     against a Streamable-HTTP-speaking httptest server, Active()
//     reports activeStreamable.
//   * TestHTTPTransport_FallthroughTo404 -- server returns 404 on
//     the initialize POST; transport demotes itself to SSE against
//     the same URL + cfg, and the SSE server then completes the
//     handshake. Active() reports activeSSE after init.
//   * TestHTTPTransport_SemaphoreCapBounds -- with MaxConcurrent=2,
//     at most two CallTool invocations run in parallel; a third
//     blocks until one of the first two returns.
//   * TestHTTPTransport_CloseReleasesSemaphore -- a caller blocked
//     on the semaphore wakes with an error after Close.

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ----------------------------------------------------------------------
// Helpers: Streamable-HTTP test server
// ----------------------------------------------------------------------

// streamableHTTPServer is a tiny httptest wrapper that speaks the
// Streamable-HTTP subset needed for initialize / tools/list /
// tools/call. Each POST returns application/json with the JSON-RPC
// response body inline.
type streamableHTTPServer struct {
	ts *httptest.Server

	mu           sync.Mutex
	sessionID    string
	callDelay    time.Duration // injected delay on tools/call responses
	callsInFlt   atomic.Int32  // live count of in-flight tools/call
	peakInFlight atomic.Int32
}

func newStreamableHTTPServer(t *testing.T) *streamableHTTPServer {
	t.Helper()
	s := &streamableHTTPServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", s.handle)
	s.ts = httptest.NewServer(mux)
	t.Cleanup(s.ts.Close)
	return s
}

func (s *streamableHTTPServer) url() string { return s.ts.URL + "/mcp" }

func (s *streamableHTTPServer) setCallDelay(d time.Duration) {
	s.mu.Lock()
	s.callDelay = d
	s.mu.Unlock()
}

func (s *streamableHTTPServer) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	method, _ := req["method"].(string)

	// Notifications (no id) -- just 204 them.
	if _, hasID := req["id"]; !hasID {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	switch method {
	case "initialize":
		s.mu.Lock()
		s.sessionID = fmt.Sprintf("sess-%d", time.Now().UnixNano())
		sid := s.sessionID
		s.mu.Unlock()
		w.Header().Set("Mcp-Session-Id", sid)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req["id"],
			"result": map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{},
				"serverInfo":      map[string]any{"name": "streamable-test", "version": "0"},
			},
		})
	case "tools/list":
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req["id"],
			"result": map[string]any{
				"tools": []map[string]any{
					{
						"name":        "echo",
						"description": "reflect the input",
						"inputSchema": map[string]any{"type": "object"},
					},
				},
			},
		})
	case "tools/call":
		// Track concurrency for the semaphore test.
		now := s.callsInFlt.Add(1)
		defer s.callsInFlt.Add(-1)
		for {
			old := s.peakInFlight.Load()
			if now <= old || s.peakInFlight.CompareAndSwap(old, now) {
				break
			}
		}

		s.mu.Lock()
		d := s.callDelay
		s.mu.Unlock()
		if d > 0 {
			time.Sleep(d)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req["id"],
			"result": map[string]any{
				"content": []map[string]any{{"type": "text", "text": "ok"}},
				"isError": false,
			},
		})
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req["id"],
			"result":  map[string]any{},
		})
	}
}

// ----------------------------------------------------------------------
// Helpers: hybrid 404-then-SSE server
// ----------------------------------------------------------------------

// fallthroughServer is an httptest.Server that answers an initial
// Streamable-HTTP POST to /mcp with 404 (triggering mcp-go's
// ErrLegacySSEServer signal), and then serves SSE on /mcp for the
// subsequent SSE-transport initialize. We reuse the sseTestServer
// wire semantics by hosting /mcp as the SSE "GET" endpoint and
// /mcp/message as the SSE "POST" endpoint.
type fallthroughServer struct {
	ts *httptest.Server

	mu       sync.Mutex
	sendCh   chan []byte
	postPath string
	rejected atomic.Int32 // count of 404s returned on POST /mcp
}

func newFallthroughServer(t *testing.T) *fallthroughServer {
	t.Helper()
	s := &fallthroughServer{postPath: "/mcp/message"}
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", s.handleRoot)
	mux.HandleFunc("/mcp/message", s.handleMessage)
	s.ts = httptest.NewServer(mux)
	t.Cleanup(s.ts.Close)
	return s
}

func (s *fallthroughServer) url() string { return s.ts.URL + "/mcp" }

// handleRoot dispatches: POST -> "legacy SSE" 404, GET -> SSE stream.
func (s *fallthroughServer) handleRoot(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		// Drain body so the client sees a clean close.
		_, _ = io.Copy(io.Discard, r.Body)
		s.rejected.Add(1)
		http.Error(w, "not found", http.StatusNotFound)
		return
	case http.MethodGet:
		s.serveSSE(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *fallthroughServer) serveSSE(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	sendCh := make(chan []byte, 16)
	s.mu.Lock()
	s.sendCh = sendCh
	s.mu.Unlock()

	// Announce the message-POST endpoint path.
	fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", s.postPath)
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			s.mu.Lock()
			if s.sendCh == sendCh {
				s.sendCh = nil
			}
			s.mu.Unlock()
			return
		case frame := <-sendCh:
			if _, err := w.Write(frame); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *fallthroughServer) send(frame []byte) bool {
	deadline := time.Now().Add(time.Second)
	for {
		s.mu.Lock()
		ch := s.sendCh
		s.mu.Unlock()
		if ch != nil {
			select {
			case ch <- frame:
				return true
			case <-time.After(50 * time.Millisecond):
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (s *fallthroughServer) handleMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusAccepted)

	if _, hasID := req["id"]; !hasID {
		return
	}

	method, _ := req["method"].(string)
	var result any
	switch method {
	case "initialize":
		result = map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"serverInfo":      map[string]any{"name": "fallthrough-sse", "version": "0"},
		}
	case "tools/list":
		result = map[string]any{
			"tools": []map[string]any{
				{"name": "echo", "description": "ok", "inputSchema": map[string]any{"type": "object"}},
			},
		}
	case "tools/call":
		result = map[string]any{
			"content": []map[string]any{{"type": "text", "text": "sse-ok"}},
			"isError": false,
		}
	default:
		result = map[string]any{}
	}
	resp := map[string]any{"jsonrpc": "2.0", "id": req["id"], "result": result}
	payload, _ := json.Marshal(resp)
	frame := []byte(fmt.Sprintf("event: message\ndata: %s\n\n", payload))
	_ = s.send(frame)
}

// ----------------------------------------------------------------------
// Tests
// ----------------------------------------------------------------------

func TestNewHTTPTransport_Validation(t *testing.T) {
	cases := []struct {
		name    string
		cfg     ServerConfig
		wantErr bool
	}{
		{"empty transport", ServerConfig{URL: "https://x"}, true},
		{"wrong transport", ServerConfig{Transport: "sse", URL: "https://x"}, true},
		{"missing URL", ServerConfig{Transport: "http"}, true},
		{"bad scheme", ServerConfig{Transport: "http", URL: "ftp://x"}, true},
		{"http spelling ok", ServerConfig{Transport: "http", URL: "http://localhost:8080/mcp"}, false},
		{"streamable-http spelling ok", ServerConfig{Transport: "streamable-http", URL: "https://api.example.com/mcp"}, false},
		{"streamable_http spelling ok", ServerConfig{Transport: "streamable_http", URL: "https://api.example.com/mcp"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr, err := NewHTTPTransport(tc.cfg)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got transport=%+v", tr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tr == nil {
				t.Fatalf("expected transport, got nil")
			}
			if tr.MaxConcurrent() != defaultMaxConcurrent {
				t.Fatalf("expected default MaxConcurrent=%d, got %d", defaultMaxConcurrent, tr.MaxConcurrent())
			}
			if tr.Active() != activeNone {
				t.Fatalf("expected Active()==activeNone pre-Initialize, got %v", tr.Active())
			}
		})
	}
}

func TestHTTPTransport_StreamableHappyPath(t *testing.T) {
	srv := newStreamableHTTPServer(t)

	tr, err := NewHTTPTransport(ServerConfig{
		Name:      "test",
		Transport: "http",
		URL:       srv.url(),
	})
	if err != nil {
		t.Fatalf("NewHTTPTransport: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := tr.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })

	if got := tr.Active(); got != activeStreamable {
		t.Fatalf("expected Active()==activeStreamable, got %v", got)
	}

	// Re-initialize must fail.
	if err := tr.Initialize(ctx); err == nil {
		t.Fatalf("expected double-Initialize error, got nil")
	}

	tools, err := tr.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 1 || tools[0].Definition.Name != "echo" {
		t.Fatalf("unexpected tools: %+v", tools)
	}
	if tools[0].ServerName != "test" {
		t.Fatalf("expected ServerName=\"test\", got %q", tools[0].ServerName)
	}

	res, err := tr.CallTool(ctx, "echo", json.RawMessage(`{"x":1}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected non-error, got %+v", res)
	}
	if len(res.Content) != 1 || res.Content[0].Type != "text" || res.Content[0].Text != "ok" {
		t.Fatalf("unexpected content: %+v", res.Content)
	}

	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Close is idempotent.
	if err := tr.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	if got := tr.Active(); got != activeNone {
		t.Fatalf("expected Active()==activeNone after Close, got %v", got)
	}

	// Post-Close RPCs fail cleanly rather than panic.
	if _, err := tr.ListTools(ctx); err == nil {
		t.Fatalf("expected ListTools error after Close")
	}
}

func TestHTTPTransport_FallthroughTo404(t *testing.T) {
	srv := newFallthroughServer(t)

	tr, err := NewHTTPTransport(ServerConfig{
		Name:      "fall",
		Transport: "streamable-http",
		URL:       srv.url(),
	})
	if err != nil {
		t.Fatalf("NewHTTPTransport: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := tr.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })

	if got := tr.Active(); got != activeSSE {
		t.Fatalf("expected Active()==activeSSE after fallthrough, got %v", got)
	}
	if rej := srv.rejected.Load(); rej < 1 {
		t.Fatalf("expected at least one 404 POST to trigger fallthrough, got %d", rej)
	}

	tools, err := tr.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools after fallthrough: %v", err)
	}
	if len(tools) != 1 || tools[0].Definition.Name != "echo" {
		t.Fatalf("unexpected tools after fallthrough: %+v", tools)
	}

	res, err := tr.CallTool(ctx, "echo", json.RawMessage(`{"x":1}`))
	if err != nil {
		t.Fatalf("CallTool after fallthrough: %v", err)
	}
	if len(res.Content) != 1 || res.Content[0].Text != "sse-ok" {
		t.Fatalf("unexpected CallTool result after fallthrough: %+v", res)
	}
}

func TestHTTPTransport_SemaphoreCapBounds(t *testing.T) {
	srv := newStreamableHTTPServer(t)
	// Each tools/call holds the server for 150ms so we can observe
	// how many land inside the window simultaneously.
	srv.setCallDelay(150 * time.Millisecond)

	const cap = 2
	tr, err := NewHTTPTransport(ServerConfig{
		Name:          "cap",
		Transport:     "http",
		URL:           srv.url(),
		MaxConcurrent: cap,
	})
	if err != nil {
		t.Fatalf("NewHTTPTransport: %v", err)
	}
	if tr.MaxConcurrent() != cap {
		t.Fatalf("expected MaxConcurrent==%d, got %d", cap, tr.MaxConcurrent())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := tr.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })

	const N = 5
	errs := make(chan error, N)
	start := time.Now()
	for i := 0; i < N; i++ {
		go func() {
			_, err := tr.CallTool(ctx, "echo", json.RawMessage(`{}`))
			errs <- err
		}()
	}
	for i := 0; i < N; i++ {
		if e := <-errs; e != nil {
			t.Fatalf("CallTool[%d] err: %v", i, e)
		}
	}
	elapsed := time.Since(start)

	// With cap=2 and 5 calls @150ms each, minimum wall-clock is
	// ceil(5/2)=3 batches * 150ms = 450ms. Allow ample slack for
	// race builds and CI.
	minExpected := 400 * time.Millisecond
	if elapsed < minExpected {
		t.Fatalf("expected elapsed >= %v (5 calls at cap=2, 150ms each), got %v",
			minExpected, elapsed)
	}

	if peak := srv.peakInFlight.Load(); peak > int32(cap) {
		t.Fatalf("semaphore breach: peak in-flight=%d > cap=%d", peak, cap)
	}
	if peak := srv.peakInFlight.Load(); peak < 1 {
		t.Fatalf("expected peak in-flight >=1, got %d", peak)
	}
}

func TestHTTPTransport_CloseReleasesSemaphore(t *testing.T) {
	srv := newStreamableHTTPServer(t)

	const cap = 1
	tr, err := NewHTTPTransport(ServerConfig{
		Name:          "close",
		Transport:     "http",
		URL:           srv.url(),
		MaxConcurrent: cap,
	})
	if err != nil {
		t.Fatalf("NewHTTPTransport: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := tr.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// Pre-fill the semaphore so the next acquire would block.
	tr.sem <- struct{}{}

	// Launch a blocked caller.
	type result struct {
		err     error
		elapsed time.Duration
	}
	done := make(chan result, 1)
	go func() {
		start := time.Now()
		_, err := tr.CallTool(ctx, "echo", json.RawMessage(`{}`))
		done <- result{err: err, elapsed: time.Since(start)}
	}()

	// Let the goroutine reach acquire().
	time.Sleep(50 * time.Millisecond)

	// Close should wake the caller.
	closeStart := time.Now()
	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case res := <-done:
		if res.err == nil {
			t.Fatalf("expected CallTool to error after Close, got nil")
		}
		if !strings.Contains(res.err.Error(), "closed") {
			t.Fatalf("expected error to mention 'closed', got %v", res.err)
		}
		// The wake should be fast -- well under the Close deadline.
		if time.Since(closeStart) > 2*time.Second {
			t.Fatalf("Close did not wake blocked caller in time (%v)", time.Since(closeStart))
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("blocked caller never woke after Close")
	}

	// Second Close is idempotent.
	if err := tr.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
