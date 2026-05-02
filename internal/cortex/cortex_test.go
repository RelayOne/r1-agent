package cortex

import (
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/hub"
)

// TestNewMissingEventBus asserts that New() rejects a Config with no
// EventBus set; the validator must surface "EventBus" in the error so
// boot logs make the misconfiguration obvious.
func TestNewMissingEventBus(t *testing.T) {
	_, err := New(Config{})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "EventBus") {
		t.Fatalf("expected EventBus in error, got %q", err.Error())
	}
}

// TestNewMissingProvider asserts that New() rejects a Config with an
// EventBus but no Provider; same surface-the-cause contract as the
// EventBus check.
func TestNewMissingProvider(t *testing.T) {
	_, err := New(Config{EventBus: hub.New()})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "Provider") {
		t.Fatalf("expected Provider in error, got %q", err.Error())
	}
}

// TestNewPanicsTooManyLobes asserts the spec-mandated panic when a
// caller asks for more than 8 LLM lobes. The hard cap matches
// LobeSemaphore's own panic, but cortex.New surfaces it before
// LobeSemaphore is constructed so the trace points at the cortex
// layer.
func TestNewPanicsTooManyLobes(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic on MaxLLMLobes=9, got none")
		}
	}()
	_, _ = New(Config{
		EventBus:    hub.New(),
		Provider:    &fakeRouterProvider{},
		MaxLLMLobes: 9,
	})
}

// TestNewDefaults asserts that New() applies the documented defaults
// when the caller leaves the optional fields zero-valued. We read
// back the values via c.cfg (in-package access) since New does not
// expose them through public accessors.
func TestNewDefaults(t *testing.T) {
	c, err := New(Config{
		EventBus: hub.New(),
		Provider: &fakeRouterProvider{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.cfg.MaxLLMLobes != 5 {
		t.Fatalf("MaxLLMLobes=%d, want 5", c.cfg.MaxLLMLobes)
	}
	if c.cfg.RoundDeadline != 2*time.Second {
		t.Fatalf("RoundDeadline=%v, want 2s", c.cfg.RoundDeadline)
	}
	if c.cfg.PreWarmInterval != 4*time.Minute {
		t.Fatalf("PreWarmInterval=%v, want 4m", c.cfg.PreWarmInterval)
	}
	if c.cfg.PreWarmModel != "claude-haiku-4-5" {
		t.Fatalf("PreWarmModel=%q, want claude-haiku-4-5", c.cfg.PreWarmModel)
	}
	if c.workspace == nil {
		t.Fatalf("workspace nil")
	}
	if c.round == nil {
		t.Fatalf("round nil")
	}
	if c.router == nil {
		t.Fatalf("router nil")
	}
	if c.sem == nil {
		t.Fatalf("sem nil")
	}
	if c.tracker == nil {
		t.Fatalf("tracker nil")
	}
}

// TestNewNegativeMaxLobesRejected asserts that a negative MaxLLMLobes
// is treated as a misconfiguration (returned as an error), distinct
// from the documented zero→default and >8→panic branches.
func TestNewNegativeMaxLobesRejected(t *testing.T) {
	_, err := New(Config{
		EventBus:    hub.New(),
		Provider:    &fakeRouterProvider{},
		MaxLLMLobes: -1,
	})
	if err == nil {
		t.Fatalf("expected error on MaxLLMLobes=-1, got nil")
	}
	if !strings.Contains(err.Error(), "MaxLLMLobes") {
		t.Fatalf("expected MaxLLMLobes in error, got %q", err.Error())
	}
}
