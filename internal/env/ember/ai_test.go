package ember

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAIClientChat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/ai/chat" {
			t.Errorf("path=%s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("missing auth")
		}
		var req ChatRequest
		json.NewDecoder(r.Body).Decode(&req)
		if len(req.Messages) != 1 || req.Messages[0].Content != "hello" {
			t.Errorf("messages=%+v", req.Messages)
		}
		json.NewEncoder(w).Encode(ChatResponse{
			Choices: []ChatChoice{{
				Message:      ChatMessage{Role: "assistant", Content: "Hi there!"},
				FinishReason: "stop",
			}},
			Usage: ChatUsage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8, TotalCost: 0.001},
		})
	}))
	defer srv.Close()

	client := NewAIClient(srv.URL, "test-key")
	resp, err := client.Chat(context.Background(), ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("choices=%d", len(resp.Choices))
	}
	if resp.Choices[0].Message.Content != "Hi there!" {
		t.Errorf("content=%q", resp.Choices[0].Message.Content)
	}
	if resp.Usage.TotalTokens != 8 {
		t.Errorf("tokens=%d", resp.Usage.TotalTokens)
	}
}

func TestAIClientChatStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, `data: {"choices":[{"delta":{"content":"Hello"}}]}`)
		fmt.Fprintln(w, `data: {"choices":[{"delta":{"content":" world"}}]}`)
		fmt.Fprintln(w, `data: [DONE]`)
	}))
	defer srv.Close()

	client := NewAIClient(srv.URL, "test-key")
	var collected string
	_, err := client.ChatStream(context.Background(), ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	}, func(content string) {
		collected += content
	})
	if err != nil {
		t.Fatal(err)
	}
	if collected != "Hello world" {
		t.Errorf("collected=%q", collected)
	}
}

func TestAIClientUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/ai/usage" {
			t.Errorf("path=%s", r.URL.Path)
		}
		period := r.URL.Query().Get("period")
		if period != "month" {
			t.Errorf("period=%q", period)
		}
		json.NewEncoder(w).Encode(AIUsage{
			Period:       "month",
			InputTokens:  10000,
			OutputTokens: 5000,
			CostUSD:      0.50,
			TotalUSD:     0.60,
			RequestCount: 42,
		})
	}))
	defer srv.Close()

	client := NewAIClient(srv.URL, "test-key")
	usage, err := client.Usage(context.Background(), "month")
	if err != nil {
		t.Fatal(err)
	}
	if usage.RequestCount != 42 {
		t.Errorf("requests=%d", usage.RequestCount)
	}
	if usage.TotalUSD != 0.60 {
		t.Errorf("total=%f", usage.TotalUSD)
	}
}

func TestAIClientChatError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		json.NewEncoder(w).Encode(map[string]string{"error": "Monthly AI spend cap reached."})
	}))
	defer srv.Close()

	client := NewAIClient(srv.URL, "test-key")
	_, err := client.Chat(context.Background(), ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !contains(err.Error(), "402") {
		t.Errorf("error=%v", err)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && searchStr(s, sub)
}

func searchStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
