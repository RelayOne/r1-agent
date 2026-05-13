package sessionhub

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// TestSessionhubKillFromBudgetEvent_Under100ms is the spec §T5 item 22
// acceptance criterion: from publication of a daemon.session.kill
// payload to sessionhub teardown, ≤100ms wall-clock.
func TestSessionhubKillFromBudgetEvent_Under100ms(t *testing.T) {
	_, cleanup := withSandbox(t)
	defer cleanup()
	hub, err := NewHub()
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}
	wd := t.TempDir()
	s, err := hub.Create(CreateOptions{Workdir: wd, Model: "test-model"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	payload, _ := json.Marshal(KillReason{
		SessionID: s.ID,
		Reason:    "test budget exceeded",
		Source:    "promptguard.budget_exceeded",
	})

	start := time.Now()
	if err := hub.ConsumeKillEvent(payload); err != nil {
		t.Fatalf("ConsumeKillEvent: %v", err)
	}
	elapsed := time.Since(start)

	if elapsed > 100*time.Millisecond {
		t.Errorf("kill latency = %v, want <=100ms", elapsed)
	}

	// Session must be gone.
	if _, err := hub.Get(s.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("after kill, Get returned %v; want ErrSessionNotFound", err)
	}

	if KillCount() < 1 {
		t.Errorf("KillCount = %d; want >=1", KillCount())
	}
}

// TestConsumeKillEvent_MalformedPayloadError covers the defensive
// path: a non-JSON payload surfaces an error rather than panicking.
func TestConsumeKillEvent_MalformedPayloadError(t *testing.T) {
	_, cleanup := withSandbox(t)
	defer cleanup()
	hub, err := NewHub()
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}
	if err := hub.ConsumeKillEvent([]byte("not json")); err == nil {
		t.Error("want error on malformed payload")
	}
}

// TestConsumeKillEvent_EmptySessionIDSentinel asserts that an empty
// session id surfaces the distinct sentinel error so callers can
// suppress the log line.
func TestConsumeKillEvent_EmptySessionIDSentinel(t *testing.T) {
	_, cleanup := withSandbox(t)
	defer cleanup()
	hub, err := NewHub()
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}
	payload, _ := json.Marshal(KillReason{Reason: "x"})
	err = hub.ConsumeKillEvent(payload)
	if !errors.Is(err, ErrEmptySessionID) {
		t.Errorf("err = %v; want ErrEmptySessionID", err)
	}
}
