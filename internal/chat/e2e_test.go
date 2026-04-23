package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestE2E_RealAnthropicProvider_Streaming stands up a mock HTTP server
// that speaks the Anthropic Messages streaming protocol, points the
// real provider.AnthropicProvider at it, and drives a chat session
// end to end. This exercises the actual wire code that Send uses in
// production and catches regressions the mockProvider unit tests
// cannot — JSON shape bugs, header bugs, SSE framing bugs.
func TestE2E_RealAnthropicProvider_Streaming(t *testing.T) {
	// Track what the server received so we can assert the Session
	// sent a well-formed request.
	var receivedMessages atomic.Value // []map[string]any
	var receivedSystem atomic.Value   // string
	var receivedTools atomic.Value    // []any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || !strings.Contains(r.URL.Path, "/v1/messages") {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "bad route", 404)
			return
		}

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("bad JSON body: %v", err)
			http.Error(w, err.Error(), 400)
			return
		}
		if msgs, ok := body["messages"].([]any); ok {
			var flat []map[string]any
			for _, m := range msgs {
				if mm, ok := m.(map[string]any); ok {
					flat = append(flat, mm)
				}
			}
			receivedMessages.Store(flat)
		}
		if sys, ok := body["system"].(string); ok {
			receivedSystem.Store(sys)
		}
		if tools, ok := body["tools"].([]any); ok {
			receivedTools.Store(tools)
		}

		// Stream a two-chunk reply followed by a message_stop.
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("ResponseWriter is not a Flusher")
		}

		writeEvent := func(ev string, data any) {
			raw, _ := json.Marshal(data)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev, string(raw))
			flusher.Flush()
		}

		writeEvent("message_start", map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id":    "msg_1",
				"type":  "message",
				"role":  "assistant",
				"model": "claude-sonnet-4-6",
				"usage": map[string]any{"input_tokens": 10, "output_tokens": 0},
			},
		})
		writeEvent("content_block_start", map[string]any{
			"type":  "content_block_start",
			"index": 0,
			"content_block": map[string]any{
				"type": "text",
				"text": "",
			},
		})
		writeEvent("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]any{"type": "text_delta", "text": "Hello "},
		})
		writeEvent("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]any{"type": "text_delta", "text": "from mock"},
		})
		writeEvent("content_block_stop", map[string]any{
			"type":  "content_block_stop",
			"index": 0,
		})
		writeEvent("message_delta", map[string]any{
			"type":  "message_delta",
			"delta": map[string]any{"stop_reason": "end_turn"},
			"usage": map[string]any{"output_tokens": 12},
		})
		writeEvent("message_stop", map[string]any{"type": "message_stop"})
	}))
	defer server.Close()

	// Build a provider pointing at the mock server.
	p, err := NewProviderFromOptions(ProviderOptions{
		BaseURL: server.URL,
		APIKey:  "test-key",
	})
	if err != nil {
		t.Fatalf("NewProviderFromOptions: %v", err)
	}

	s, err := NewSession(p, Config{
		Model:        "claude-sonnet-4-6",
		SystemPrompt: "You are Stoke.",
		Tools:        DispatcherTools(),
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	var got strings.Builder
	onDispatch := func(ctx context.Context, name string, input json.RawMessage) (string, error) {
		return "unused in this test", nil
	}
	result, err := s.Send(context.Background(), "hi", func(d string) { got.WriteString(d) }, onDispatch)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	if got.String() != "Hello from mock" {
		t.Errorf("streamed text = %q, want 'Hello from mock'", got.String())
	}
	if result.Text != "Hello from mock" {
		t.Errorf("result.Text = %q", result.Text)
	}

	// Server-side assertions
	msgs, _ := receivedMessages.Load().([]map[string]any)
	if len(msgs) != 1 {
		t.Errorf("server received %d messages, want 1", len(msgs))
	}
	sys, _ := receivedSystem.Load().(string)
	// Note: AnthropicProvider sends SystemRaw (content blocks) when
	// set, else the plain System string. chat uses plain System, so
	// the server sees it as a string.
	if !strings.Contains(sys, "Stoke") {
		t.Errorf("server didn't see system prompt: %q", sys)
	}
	tools, _ := receivedTools.Load().([]any)
	if len(tools) == 0 {
		t.Errorf("server didn't see tool advertisements (onDispatch was wired)")
	}
}

// TestE2E_ProviderError_PropagatesThroughSession confirms that a
// non-retriable error from the provider surfaces as a Send error
// without corrupting the session history. Uses 401 (auth error) so
// the provider fails fast without burning 95s on retry backoff
// (Chat/ChatStream retry 5xx and 429 with 5/10/20/30/30s sleeps;
// 4xx other than 429 are non-retriable per
// isRetriableProviderError).
func TestE2E_ProviderError_PropagatesThroughSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"type":"authentication_error","message":"bad key"}}`, 401)
	}))
	defer server.Close()

	p, _ := NewProviderFromOptions(ProviderOptions{BaseURL: server.URL, APIKey: "k"})
	s, _ := NewSession(p, Config{Model: "m"})

	_, err := s.Send(context.Background(), "hi", nil, nil)
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
	if !strings.Contains(err.Error(), "401") && !strings.Contains(err.Error(), "authentication") {
		t.Errorf("error does not mention 401/authentication: %v", err)
	}
	if s.TurnCount() != 0 {
		t.Errorf("failed turn should not pollute history, got %d messages", s.TurnCount())
	}
}

// TestE2E_ServerConnectionRefused simulates a totally-down upstream.
// The provider should return an error that Send wraps with chat-level
// context.
//
// "connection refused" is classified as retriable by
// isRetriableProviderError (to survive litellm restarts), so the
// provider's ChatStream will retry with 5/10/20/30/30s backoff.
// Session.Send watches ctx.Done() so we bail out early via a
// short-deadline context; any non-nil error (ctx-cancelled or
// connection-refused) counts as "error propagated through Send".
func TestE2E_ServerConnectionRefused(t *testing.T) {
	// Use a closed server: NewServer then Close gives us a URL that
	// will fail to connect.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close()

	p, _ := NewProviderFromOptions(ProviderOptions{BaseURL: server.URL, APIKey: "k"})
	s, _ := NewSession(p, Config{Model: "m"})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := s.Send(ctx, "hi", nil, nil)
	if err == nil {
		t.Fatal("expected error for closed server")
	}
	if s.TurnCount() != 0 {
		t.Errorf("failed turn should not pollute history, got %d", s.TurnCount())
	}
}
