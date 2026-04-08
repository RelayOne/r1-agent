package remote

import (
	"testing"
)

func TestNew_NoEnvKey(t *testing.T) {
	t.Setenv("EMBER_API_KEY", "")
	r := New()
	if r != nil {
		t.Errorf("New() = %+v, want nil when EMBER_API_KEY is empty", r)
	}
}

func TestNew_WithEnvKey(t *testing.T) {
	t.Setenv("EMBER_API_KEY", "test-key")
	t.Setenv("EMBER_API_URL", "https://custom.api.dev")
	r := New()
	if r == nil {
		t.Fatal("New() = nil, want non-nil when EMBER_API_KEY is set")
	}
	if r.apiKey != "test-key" {
		t.Errorf("apiKey = %q, want %q", r.apiKey, "test-key")
	}
	if r.endpoint != "https://custom.api.dev" {
		t.Errorf("endpoint = %q, want %q", r.endpoint, "https://custom.api.dev")
	}
}

func TestSessionReporter_NilSafe(t *testing.T) {
	// All methods on a nil receiver should not panic and return zero values.
	var r *SessionReporter

	if id := r.SessionID(); id != "" {
		t.Errorf("nil.SessionID() = %q, want empty", id)
	}
	if url := r.WebURL(); url != "" {
		t.Errorf("nil.WebURL() = %q, want empty", url)
	}

	// RegisterSession on nil should return empty string, no error
	url, err := r.RegisterSession("plan-1")
	if err != nil {
		t.Errorf("nil.RegisterSession() error = %v, want nil", err)
	}
	if url != "" {
		t.Errorf("nil.RegisterSession() = %q, want empty", url)
	}

	// Update on nil should return nil
	if err := r.Update(SessionUpdate{}); err != nil {
		t.Errorf("nil.Update() error = %v, want nil", err)
	}

	// Complete on nil should return nil
	if err := r.Complete(true, "done"); err != nil {
		t.Errorf("nil.Complete() error = %v, want nil", err)
	}
}

func TestWebURL_Format(t *testing.T) {
	r := &SessionReporter{
		endpoint:  "https://api.ember.dev",
		sessionID: "sess-abc-123",
	}
	got := r.WebURL()
	want := "https://api.ember.dev/s/sess-abc-123"
	if got != want {
		t.Errorf("WebURL() = %q, want %q", got, want)
	}
}

func TestWebURL_EmptySession(t *testing.T) {
	r := &SessionReporter{
		endpoint:  "https://api.ember.dev",
		sessionID: "",
	}
	if got := r.WebURL(); got != "" {
		t.Errorf("WebURL() with empty session = %q, want empty", got)
	}
}

func TestSessionID(t *testing.T) {
	r := &SessionReporter{sessionID: "my-session"}
	if got := r.SessionID(); got != "my-session" {
		t.Errorf("SessionID() = %q, want %q", got, "my-session")
	}
}
