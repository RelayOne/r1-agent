package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ericmacdougall/stoke/internal/env/ember"
	"github.com/ericmacdougall/stoke/internal/stream"
)

func TestEmberProviderName(t *testing.T) {
	p := NewEmberProvider("http://localhost", "token", "")
	if p.Name() != "ember" {
		t.Errorf("Name()=%q, want ember", p.Name())
	}
}

func TestEmberProviderDefaultModel(t *testing.T) {
	p := NewEmberProvider("http://localhost", "token", "")
	if p.model != "anthropic/claude-sonnet-4" {
		t.Errorf("model=%q, want anthropic/claude-sonnet-4", p.model)
	}
}

func TestEmberProviderCustomModel(t *testing.T) {
	p := NewEmberProvider("http://localhost", "token", "openai/gpt-4o")
	if p.model != "openai/gpt-4o" {
		t.Errorf("model=%q, want openai/gpt-4o", p.model)
	}
}

func TestEmberProviderChat(t *testing.T) {
	var receivedReq ember.ChatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/ai/chat" {
			t.Errorf("path=%q, want /v1/ai/chat", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-token" {
			t.Errorf("auth=%q, want Bearer test-token", auth)
		}

		json.NewDecoder(r.Body).Decode(&receivedReq)

		json.NewEncoder(w).Encode(ember.ChatResponse{
			Choices: []ember.ChatChoice{{
				Message:      ember.ChatMessage{Role: "assistant", Content: "Hello from Ember!"},
				FinishReason: "stop",
			}},
			Usage: ember.ChatUsage{
				PromptTokens:     10,
				CompletionTokens: 5,
				TotalTokens:      15,
			},
		})
	}))
	defer srv.Close()

	p := NewEmberProvider(srv.URL, "test-token", "test-model")
	resp, err := p.Chat(ChatRequest{
		System: "You are helpful.",
		Messages: []ChatMessage{{
			Role:    "user",
			Content: toRaw("What is 2+2?"),
		}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	// Verify request conversion.
	if receivedReq.Model != "test-model" {
		t.Errorf("model=%q, want test-model", receivedReq.Model)
	}
	if len(receivedReq.Messages) != 2 {
		t.Fatalf("messages=%d, want 2 (system+user)", len(receivedReq.Messages))
	}
	if receivedReq.Messages[0].Role != "system" {
		t.Errorf("first message role=%q, want system", receivedReq.Messages[0].Role)
	}

	// Verify response conversion.
	if resp.Model != "test-model" {
		t.Errorf("resp.Model=%q, want test-model", resp.Model)
	}
	if len(resp.Content) != 1 || resp.Content[0].Text != "Hello from Ember!" {
		t.Errorf("content=%v, want Hello from Ember!", resp.Content)
	}
	if resp.StopReason != "stop" {
		t.Errorf("stop_reason=%q, want stop", resp.StopReason)
	}
	if resp.Usage.Input != 10 || resp.Usage.Output != 5 {
		t.Errorf("usage=%+v, want input=10 output=5", resp.Usage)
	}
}

func TestEmberProviderChatError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer srv.Close()

	p := NewEmberProvider(srv.URL, "token", "model")
	_, err := p.Chat(ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: toRaw("hi")}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "ember") {
		t.Errorf("error should mention ember: %v", err)
	}
}

func TestEmberProviderChatStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		chunks := []string{"Hello", " from", " streaming!"}
		for _, chunk := range chunks {
			delta := fmt.Sprintf(`{"choices":[{"delta":{"content":"%s"}}]}`, chunk)
			fmt.Fprintf(w, "data: %s\n\n", delta)
			flusher.Flush()
		}
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	p := NewEmberProvider(srv.URL, "token", "test-model")

	var events []stream.Event
	resp, err := p.ChatStream(ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: toRaw("hi")}},
	}, func(ev stream.Event) {
		events = append(events, ev)
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}

	// Should have received 3 streaming events.
	if len(events) != 3 {
		t.Errorf("events=%d, want 3", len(events))
	}
	for _, ev := range events {
		if ev.Type != "assistant" {
			t.Errorf("event type=%q, want assistant", ev.Type)
		}
	}

	// Final response should have accumulated content.
	if len(resp.Content) != 1 {
		t.Fatalf("content blocks=%d, want 1", len(resp.Content))
	}
	if resp.Content[0].Text != "Hello from streaming!" {
		t.Errorf("text=%q, want 'Hello from streaming!'", resp.Content[0].Text)
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("stop_reason=%q, want end_turn", resp.StopReason)
	}
}

func TestEmberProviderChatStreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte("rate limited"))
	}))
	defer srv.Close()

	p := NewEmberProvider(srv.URL, "token", "model")
	_, err := p.ChatStream(ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: toRaw("hi")}},
	}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestEmberProviderEmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(ember.ChatResponse{
			Choices: []ember.ChatChoice{},
		})
	}))
	defer srv.Close()

	p := NewEmberProvider(srv.URL, "token", "model")
	resp, err := p.Chat(ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: toRaw("hi")}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(resp.Content) != 0 {
		t.Errorf("content=%d, want 0 for empty choices", len(resp.Content))
	}
}

func TestExtractTextContentString(t *testing.T) {
	raw := toRaw("hello world")
	result := extractTextContent(raw)
	if result != "hello world" {
		t.Errorf("result=%q, want 'hello world'", result)
	}
}

func TestExtractTextContentBlocks(t *testing.T) {
	blocks := []map[string]string{
		{"type": "text", "text": "part 1"},
		{"type": "text", "text": "part 2"},
	}
	raw, _ := json.Marshal(blocks)
	result := extractTextContent(raw)
	if result != "part 1part 2" {
		t.Errorf("result=%q, want 'part 1part 2'", result)
	}
}

func TestConvertMessagesWithSystem(t *testing.T) {
	req := ChatRequest{
		System: "Be helpful",
		Messages: []ChatMessage{
			{Role: "user", Content: toRaw("hi")},
		},
	}
	msgs := convertMessages(req)
	if len(msgs) != 2 {
		t.Fatalf("len=%d, want 2", len(msgs))
	}
	if msgs[0].Role != "system" || msgs[0].Content != "Be helpful" {
		t.Errorf("msgs[0]=%+v", msgs[0])
	}
	if msgs[1].Role != "user" || msgs[1].Content != "hi" {
		t.Errorf("msgs[1]=%+v", msgs[1])
	}
}

func TestConvertMessagesWithoutSystem(t *testing.T) {
	req := ChatRequest{
		Messages: []ChatMessage{
			{Role: "user", Content: toRaw("hi")},
		},
	}
	msgs := convertMessages(req)
	if len(msgs) != 1 {
		t.Fatalf("len=%d, want 1", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("role=%q, want user", msgs[0].Role)
	}
}

func TestConvertResponseFull(t *testing.T) {
	resp := &ember.ChatResponse{
		Choices: []ember.ChatChoice{{
			Message:      ember.ChatMessage{Role: "assistant", Content: "answer"},
			FinishReason: "stop",
		}},
		Usage: ember.ChatUsage{PromptTokens: 100, CompletionTokens: 50},
	}

	result := convertResponse(resp, "test-model")
	if result.Model != "test-model" {
		t.Errorf("model=%q", result.Model)
	}
	if len(result.Content) != 1 || result.Content[0].Text != "answer" {
		t.Errorf("content=%+v", result.Content)
	}
	if result.StopReason != "stop" {
		t.Errorf("stop=%q", result.StopReason)
	}
	if result.Usage.Input != 100 || result.Usage.Output != 50 {
		t.Errorf("usage=%+v", result.Usage)
	}
}

func TestInterfaceCompliance(t *testing.T) {
	var _ Provider = (*EmberProvider)(nil)
}

func toRaw(s string) json.RawMessage {
	data, _ := json.Marshal(s)
	return data
}
