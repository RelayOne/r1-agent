package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"
)

func TestScriptGateAllow(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}
	b := New()
	b.Register(Subscriber{
		ID:     "script-allow",
		Events: []EventType{EventToolFileWrite},
		Mode:   ModeGate,
		Script: &ScriptConfig{
			Command:    "echo {}",
			Timeout:    5 * time.Second,
			InputJSON:  true,
			OutputJSON: false,
		},
	})

	resp := b.Emit(context.Background(), &Event{
		Type: EventToolFileWrite,
		File: &FileEvent{Path: "main.go"},
	})
	if resp.Decision == Deny {
		t.Fatal("expected allow from successful script")
	}
}

func TestScriptGateDenyOnFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}
	b := New()
	b.Register(Subscriber{
		ID:     "script-deny",
		Events: []EventType{EventToolFileWrite},
		Mode:   ModeGate,
		Script: &ScriptConfig{
			Command:    "false",
			Timeout:    5 * time.Second,
			InputJSON:  false,
			OutputJSON: false,
		},
	})

	resp := b.Emit(context.Background(), &Event{
		Type: EventToolFileWrite,
		File: &FileEvent{Path: "main.go"},
	})
	if resp.Decision != Deny {
		t.Fatalf("expected Deny from failed script, got %s", resp.Decision)
	}
}

func TestScriptJSONOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}
	b := New()
	b.Register(Subscriber{
		ID:     "script-json",
		Events: []EventType{EventToolFileWrite},
		Mode:   ModeGate,
		Script: &ScriptConfig{
			Command:    `echo '{"decision":"deny","reason":"not allowed"}'`,
			Timeout:    5 * time.Second,
			InputJSON:  false,
			OutputJSON: true,
		},
	})

	resp := b.Emit(context.Background(), &Event{
		Type: EventToolFileWrite,
		File: &FileEvent{Path: "main.go"},
	})
	if resp.Decision != Deny {
		t.Fatalf("expected Deny from JSON output, got %s", resp.Decision)
	}
	if resp.Reason != "not allowed" {
		t.Fatalf("expected reason 'not allowed', got %q", resp.Reason)
	}
}

func TestScriptTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}
	b := New()
	b.Register(Subscriber{
		ID:     "script-slow",
		Events: []EventType{EventToolFileWrite},
		Mode:   ModeGate,
		Script: &ScriptConfig{
			Command:    "sleep 5",
			Timeout:    200 * time.Millisecond,
			InputJSON:  false,
			OutputJSON: false,
		},
	})

	resp := b.Emit(context.Background(), &Event{
		Type: EventToolFileWrite,
		File: &FileEvent{Path: "main.go"},
	})
	// Timeout should not deny — should abstain
	if resp.Decision == Deny {
		t.Fatal("timeout should abstain, not deny")
	}
}

func TestScriptObserveNoBlockOnFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}
	b := New()
	b.Register(Subscriber{
		ID:     "script-observe-fail",
		Events: []EventType{EventTaskCompleted},
		Mode:   ModeObserve,
		Script: &ScriptConfig{
			Command:    "false",
			Timeout:    5 * time.Second,
			InputJSON:  false,
			OutputJSON: false,
		},
	})

	// Observe mode — emit shouldn't block or deny
	resp := b.Emit(context.Background(), &Event{Type: EventTaskCompleted})
	if resp.Decision == Deny {
		t.Fatal("observer script failure should not deny")
	}
}

func TestWebhookAllow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(scriptOutput{Decision: "allow"})
	}))
	defer server.Close()

	b := New()
	b.Register(Subscriber{
		ID:     "webhook-allow",
		Events: []EventType{EventTaskCompleted},
		Mode:   ModeGate,
		Webhook: &WebhookConfig{
			URL:     server.URL,
			Timeout: 5 * time.Second,
		},
	})

	resp := b.Emit(context.Background(), &Event{Type: EventTaskCompleted})
	if resp.Decision != Allow {
		t.Fatalf("expected Allow, got %s", resp.Decision)
	}
}

func TestWebhookDeny(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(scriptOutput{Decision: "deny", Reason: "policy violation"})
	}))
	defer server.Close()

	b := New()
	b.Register(Subscriber{
		ID:     "webhook-deny",
		Events: []EventType{EventToolBashExec},
		Mode:   ModeGate,
		Webhook: &WebhookConfig{
			URL:     server.URL,
			Timeout: 5 * time.Second,
		},
	})

	resp := b.Emit(context.Background(), &Event{Type: EventToolBashExec})
	if resp.Decision != Deny {
		t.Fatalf("expected Deny, got %s", resp.Decision)
	}
	if resp.Reason != "policy violation" {
		t.Fatalf("expected reason, got %q", resp.Reason)
	}
}

func TestWebhook4xxDeny(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, "forbidden")
	}))
	defer server.Close()

	b := New()
	b.Register(Subscriber{
		ID:     "webhook-403",
		Events: []EventType{EventToolBashExec},
		Mode:   ModeGate,
		Webhook: &WebhookConfig{
			URL:     server.URL,
			Timeout: 5 * time.Second,
		},
	})

	resp := b.Emit(context.Background(), &Event{Type: EventToolBashExec})
	if resp.Decision != Deny {
		t.Fatalf("expected Deny for 403, got %s", resp.Decision)
	}
}

func TestWebhookRetry(t *testing.T) {
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(scriptOutput{Decision: "allow"})
	}))
	defer server.Close()

	b := New()
	b.Register(Subscriber{
		ID:     "webhook-retry",
		Events: []EventType{EventTaskCompleted},
		Mode:   ModeGate,
		Webhook: &WebhookConfig{
			URL:     server.URL,
			Timeout: 10 * time.Second,
			Retries: 3,
		},
	})

	resp := b.Emit(context.Background(), &Event{Type: EventTaskCompleted})
	if resp.Decision != Allow {
		t.Fatalf("expected Allow after retries, got %s", resp.Decision)
	}
	if attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts)
	}
}

func TestWebhookCustomHeaders(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(scriptOutput{Decision: "allow"})
	}))
	defer server.Close()

	b := New()
	b.Register(Subscriber{
		ID:     "webhook-headers",
		Events: []EventType{EventTaskCompleted},
		Mode:   ModeGate,
		Webhook: &WebhookConfig{
			URL:     server.URL,
			Headers: map[string]string{"Authorization": "Bearer test-token"},
			Timeout: 5 * time.Second,
		},
	})

	b.Emit(context.Background(), &Event{Type: EventTaskCompleted})
	if gotAuth != "Bearer test-token" {
		t.Fatalf("expected auth header, got %q", gotAuth)
	}
}

func TestWebhookTransformInjections(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(scriptOutput{
			Injections: []Injection{{Position: "system", Content: "extra context", Label: "webhook"}},
		})
	}))
	defer server.Close()

	b := New()
	b.Register(Subscriber{
		ID:     "webhook-transform",
		Events: []EventType{EventPromptBuilding},
		Mode:   ModeTransform,
		Webhook: &WebhookConfig{
			URL:     server.URL,
			Timeout: 5 * time.Second,
		},
	})

	injections := b.Transform(context.Background(), &Event{Type: EventPromptBuilding})
	if len(injections) != 1 {
		t.Fatalf("expected 1 injection from webhook, got %d", len(injections))
	}
	if injections[0].Content != "extra context" {
		t.Fatalf("unexpected content: %s", injections[0].Content)
	}
}
