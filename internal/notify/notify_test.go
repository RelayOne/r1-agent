package notify

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWebhookNotifierSuccess(t *testing.T) {
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		received = buf[:n]
		w.WriteHeader(200)
	}))
	defer srv.Close()

	n := NewWebhookNotifier(srv.URL, nil, nil)
	err := n.Notify(NotifyEvent{Type: "test", BeaconID: "bc-123", SessionID: "sess-9", Message: "hello", Timestamp: time.Now()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(received) == 0 {
		t.Fatal("no body received")
	}

	var evt NotifyEvent
	json.Unmarshal(received, &evt)
	if evt.Type != "test" {
		t.Errorf("type = %q, want test", evt.Type)
	}
	if evt.BeaconID != "bc-123" || evt.SessionID != "sess-9" {
		t.Errorf("beacon/session not preserved: %+v", evt)
	}
}

func TestWebhookNotifierTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Second) // exceed 5s timeout
	}))
	defer srv.Close()

	n := NewWebhookNotifier(srv.URL, nil, nil)
	err := n.Notify(NotifyEvent{Type: "test", Message: "hello", Timestamp: time.Now()})
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestNopNotifier(t *testing.T) {
	n := NopNotifier{}
	if err := n.Notify(NotifyEvent{}); err != nil {
		t.Fatal(err)
	}
}

func TestDiscordFormat(t *testing.T) {
	data, err := DiscordFormat(NotifyEvent{Type: "task_complete", Message: "done", Timestamp: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	json.Unmarshal(data, &m)
	if _, ok := m["embeds"]; !ok {
		t.Error("missing embeds key")
	}
}

func TestSlackFormat(t *testing.T) {
	data, err := SlackFormat(NotifyEvent{Type: "task_failed", Message: "oops", Timestamp: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	json.Unmarshal(data, &m)
	if _, ok := m["blocks"]; !ok {
		t.Error("missing blocks key")
	}
}

// --- Retry behavior tests ---

func TestWebhookNotifierRetry(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(500) // fail first attempt
			return
		}
		w.WriteHeader(200) // succeed on retry
	}))
	defer srv.Close()

	n := NewWebhookNotifier(srv.URL, nil, nil)
	err := n.Notify(NotifyEvent{Type: "test_retry", Message: "should retry", Timestamp: time.Now()})
	if err != nil {
		t.Fatalf("expected retry to succeed, got: %v", err)
	}
	if attempts != 2 {
		t.Errorf("expected 2 attempts (initial + retry), got %d", attempts)
	}
}

// --- Custom headers test ---

func TestWebhookNotifierCustomHeaders(t *testing.T) {
	var receivedAuth string
	var receivedCustom string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		receivedCustom = r.Header.Get("X-Custom-Header")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	headers := map[string]string{
		"Authorization":   "Bearer test-token",
		"X-Custom-Header": "custom-value",
	}
	n := NewWebhookNotifier(srv.URL, headers, nil)
	err := n.Notify(NotifyEvent{Type: "test_headers", Message: "with headers", Timestamp: time.Now()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedAuth != "Bearer test-token" {
		t.Errorf("Authorization header = %q, want %q", receivedAuth, "Bearer test-token")
	}
	if receivedCustom != "custom-value" {
		t.Errorf("X-Custom-Header = %q, want %q", receivedCustom, "custom-value")
	}
}

// --- TelegramFormat test ---

func TestTelegramFormat(t *testing.T) {
	formatter := TelegramFormat("12345")
	data, err := formatter(NotifyEvent{Type: "task_complete", Message: "done", Timestamp: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	chatID, ok := m["chat_id"]
	if !ok {
		t.Fatal("missing chat_id in Telegram format")
	}
	if chatID != "12345" {
		t.Errorf("chat_id = %v, want %q", chatID, "12345")
	}
	if _, ok := m["parse_mode"]; !ok {
		t.Error("missing parse_mode in Telegram format")
	}
}

// --- GenericFormat test ---

func TestGenericFormat(t *testing.T) {
	evt := NotifyEvent{
		Type:        "build_complete",
		TaskID:      "TASK-42",
		BeaconID:    "bc-123",
		SessionID:   "sess-42",
		ArtifactRef: "sha256:artifact",
		Message:     "build passed",
		Timestamp:   time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC),
		Details:     map[string]string{"duration": "5m"},
	}
	data, err := GenericFormat(evt)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, field := range []string{"build_complete", "TASK-42", "bc-123", "sess-42", "sha256:artifact", "build passed", "duration", "5m"} {
		if !strings.Contains(content, field) {
			t.Errorf("GenericFormat output missing field %q", field)
		}
	}
}

// --- Invalid URL test ---

func TestWebhookNotifierInvalidURL(t *testing.T) {
	n := NewWebhookNotifier("http://[::1]:invalid-port/bad", nil, nil)
	err := n.Notify(NotifyEvent{Type: "test", Message: "bad url", Timestamp: time.Now()})
	if err == nil {
		t.Error("expected error for invalid URL, got nil")
	}
}
