package sessionctl

import (
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/bus"
)

func newTestBus(t *testing.T) *bus.Bus {
	t.Helper()
	b, err := bus.New(filepath.Join(t.TempDir(), "wal"))
	if err != nil {
		t.Fatalf("bus.New: %v", err)
	}
	t.Cleanup(func() { b.Close() })
	return b
}

// TestNewBusEmitter_PublishesEvent verifies that the helper
// produces events matching the kind, EmitterID, and payload
// shape required by audit consumers.
func TestNewBusEmitter_PublishesEvent(t *testing.T) {
	b := newTestBus(t)

	var (
		mu      sync.Mutex
		events  []bus.Event
		signal  = make(chan struct{}, 1)
		pattern = bus.Pattern{TypePrefix: "operator."}
	)
	b.Subscribe(pattern, func(evt bus.Event) {
		mu.Lock()
		events = append(events, evt)
		mu.Unlock()
		select {
		case signal <- struct{}{}:
		default:
		}
	})

	emit := NewBusEmitter(b, "sess-42")
	got := emit("operator.override", map[string]any{
		"ac_id":  "ac-9",
		"reason": "manual",
	})
	if got == "" {
		t.Fatal("emitter returned empty eventID; want bus event ID")
	}

	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal("event never delivered to subscriber")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 {
		t.Fatalf("events=%d want 1", len(events))
	}
	evt := events[0]
	if string(evt.Type) != "operator.override" {
		t.Errorf("Type=%q want operator.override", evt.Type)
	}
	if evt.EmitterID != "sessionctl/sess-42" {
		t.Errorf("EmitterID=%q want sessionctl/sess-42", evt.EmitterID)
	}
	if evt.ID != got {
		t.Errorf("emitter returned %q; bus event ID is %q", got, evt.ID)
	}
	var payload map[string]any
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload["ac_id"] != "ac-9" || payload["reason"] != "manual" {
		t.Errorf("payload=%v want {ac_id:ac-9, reason:manual}", payload)
	}
}

// TestNewBusEmitter_NilBusReturnsNoop verifies that callers can
// pass a nil bus without nil-checks elsewhere — the returned
// emitter is a safe no-op.
func TestNewBusEmitter_NilBusReturnsNoop(t *testing.T) {
	emit := NewBusEmitter(nil, "sess-42")
	if got := emit("operator.override", map[string]any{}); got != "" {
		t.Errorf("nil-bus emitter returned %q want empty", got)
	}
}

// TestNewBusEmitter_PublishFailureDropsEvent verifies that a
// closed bus (which makes Publish return an error) drops the
// event and returns an empty string — matching the best-effort
// contract of the handlers.go fallback emitter.
func TestNewBusEmitter_PublishFailureDropsEvent(t *testing.T) {
	b := newTestBus(t)
	b.Close()
	emit := NewBusEmitter(b, "sess-42")
	if got := emit("operator.override", map[string]any{"x": 1}); got != "" {
		t.Errorf("closed-bus emitter returned %q want empty", got)
	}
}
