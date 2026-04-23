// transport_sse_test.go — unit tests for SSE MCP transport.
//
// These tests stand up an httptest.Server that speaks the same SSE wire
// format mcp-go's SSE client expects (documented at
// https://modelcontextprotocol.io/specification/2024-11-05/basic/transports#server-sent-events):
//
//   1. GET <base-url>   → "event: endpoint\ndata: <message-path>\n\n" then
//                          further "event: message\ndata: <json-rpc>\n\n"
//                          entries as responses stream in.
//   2. POST <message-path> with a JSON-RPC envelope. The server writes the
//                          corresponding response back onto the SSE stream.
//
// Covered:
//   * TestSSETransport_Initialize — full happy-path handshake.
//   * TestSSETransport_ReconnectHandlerFires — forcing the server to drop
//     the stream invokes the registered reconnect handler with a
//     monotonically-increasing attempt counter.

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// sseTestServer is a minimal MCP-over-SSE server good enough to exercise
// Initialize/ListTools/CallTool against. It is NOT an MCP server in the
// protocol-conformance sense — it canned-responds to the specific
// methods we drive in these tests.
//
// Concurrency model: the http.ResponseWriter associated with the SSE GET
// stream is NOT safe for concurrent writes. The test server therefore
// funnels every outbound SSE frame through a single writer goroutine
// (handleSSE) via s.send, so POST handlers never touch the writer
// directly.
type sseTestServer struct {
	ts *httptest.Server

	mu        sync.Mutex
	sendCh    chan []byte   // buffered: outbound SSE frames
	liveCh    chan struct{} // closed while an SSE handler is active
	dropOnce  sync.Once
	dropCh    chan struct{} // closed to force the handler to terminate early
}

func newSSETestServer(t *testing.T) *sseTestServer {
	t.Helper()
	s := &sseTestServer{dropCh: make(chan struct{})}

	mux := http.NewServeMux()
	mux.HandleFunc("/sse", s.handleSSE)
	mux.HandleFunc("/message", s.handleMessage)

	s.ts = httptest.NewServer(mux)
	t.Cleanup(s.ts.Close)
	return s
}

func (s *sseTestServer) url() string { return s.ts.URL + "/sse" }

func (s *sseTestServer) handleSSE(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	sendCh := make(chan []byte, 16)
	live := make(chan struct{})
	s.mu.Lock()
	s.sendCh = sendCh
	s.liveCh = live
	s.mu.Unlock()
	close(live) // signal liveness

	// Seed the endpoint event before announcing the stream.
	fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", "/message")
	flusher.Flush()

	// Writer pump. Exit cleanly on request cancellation or drop trigger.
	for {
		select {
		case <-r.Context().Done():
			s.teardown(sendCh)
			return
		case <-s.dropCh:
			s.teardown(sendCh)
			return
		case frame := <-sendCh:
			if _, err := w.Write(frame); err != nil {
				s.teardown(sendCh)
				return
			}
			flusher.Flush()
		}
	}
}

// teardown clears the live channel references so POST handlers can no
// longer enqueue frames. The send channel is NOT closed — in-flight
// senders may still be mid-enqueue; garbage collection handles the rest.
func (s *sseTestServer) teardown(sendCh chan []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sendCh == sendCh {
		s.sendCh = nil
		s.liveCh = nil
	}
}

// send enqueues an SSE frame, waiting briefly for the stream to come
// back up if a reconnect is in flight. Returns false if no stream is
// available within the deadline.
func (s *sseTestServer) send(frame []byte) bool {
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
				// Channel full or closed — try again.
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (s *sseTestServer) handleMessage(w http.ResponseWriter, r *http.Request) {
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

	// Acknowledge the POST immediately (per the SSE transport spec); the
	// real response streams back out the GET channel.
	w.WriteHeader(http.StatusAccepted)

	// Notifications ("initialized" et al) carry no id — nothing to reply.
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
			"serverInfo":      map[string]any{"name": "sse-test-server", "version": "0"},
		}
	case "tools/list":
		result = map[string]any{
			"tools": []map[string]any{
				{
					"name":        "echo",
					"description": "reflect the input",
					"inputSchema": map[string]any{"type": "object"},
				},
			},
		}
	case "tools/call":
		result = map[string]any{
			"content": []map[string]any{{"type": "text", "text": "ok"}},
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

// dropStream forces the in-flight SSE handler to return, which mcp-go's
// SSE transport observes as a connection-lost event. Safe to call once.
func (s *sseTestServer) dropStream() {
	s.dropOnce.Do(func() { close(s.dropCh) })
}

// waitForStream blocks until an SSE handler is active so tests that need
// to force a drop don't race the initial GET.
func (s *sseTestServer) waitForStream(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		live := s.sendCh != nil
		s.mu.Unlock()
		if live {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("SSE stream never came up")
}

// ----------------------------------------------------------------------
// Tests
// ----------------------------------------------------------------------

func TestNewSSETransport_Validation(t *testing.T) {
	cases := []struct {
		name    string
		cfg     ServerConfig
		wantErr bool
	}{
		{"empty transport", ServerConfig{URL: "https://x"}, true},
		{"bad transport", ServerConfig{Transport: "stdio", URL: "https://x"}, true},
		{"missing URL", ServerConfig{Transport: "sse"}, true},
		{"non-http URL", ServerConfig{Transport: "sse", URL: "ftp://x"}, true},
		{"sse ok", ServerConfig{Transport: "sse", URL: "https://example.com/sse"}, false},
		{"http fallthrough ok", ServerConfig{Transport: "http", URL: "http://localhost:8080/sse"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr, err := NewSSETransport(tc.cfg)
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
		})
	}
}

func TestSSETransport_Initialize(t *testing.T) {
	srv := newSSETestServer(t)

	tr, err := NewSSETransport(ServerConfig{
		Name:      "test",
		Transport: "sse",
		URL:       srv.url(),
	})
	if err != nil {
		t.Fatalf("NewSSETransport: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := tr.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	defer tr.Close()

	// Second Initialize must fail — the transport is already live.
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

	// Post-Close calls fail cleanly.
	if _, err := tr.ListTools(ctx); err == nil {
		t.Fatalf("expected error from ListTools after Close")
	}
}

func TestSSETransport_ReconnectHandlerFires(t *testing.T) {
	srv := newSSETestServer(t)

	tr, err := NewSSETransport(ServerConfig{
		Name:      "test",
		Transport: "sse",
		URL:       srv.url(),
	})
	if err != nil {
		t.Fatalf("NewSSETransport: %v", err)
	}

	var (
		calls   atomic.Int64
		lastAtt atomic.Int64
		gotErr  atomic.Bool
		done    = make(chan struct{}, 1)
	)
	tr.SetReconnectHandler(func(attempt int, err error) {
		calls.Add(1)
		lastAtt.Store(int64(attempt))
		if err != nil {
			gotErr.Store(true)
		}
		select {
		case done <- struct{}{}:
		default:
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := tr.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	defer tr.Close()

	srv.waitForStream(t)
	srv.dropStream()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("reconnect handler never fired (calls=%d)", calls.Load())
	}

	if got := calls.Load(); got < 1 {
		t.Fatalf("expected >=1 reconnect callback, got %d", got)
	}
	if got := lastAtt.Load(); got < 1 {
		t.Fatalf("expected attempt >=1, got %d", got)
	}
	if !gotErr.Load() {
		t.Fatalf("expected non-nil error on reconnect callback")
	}
}
