package hub

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewBus(t *testing.T) {
	b := New()
	if b == nil {
		t.Fatal("New returned nil")
	}
	if b.SubscriberCount() != 0 {
		t.Fatalf("expected 0 subscribers, got %d", b.SubscriberCount())
	}
}

func TestRegisterUnregister(t *testing.T) {
	b := New()
	b.Register(Subscriber{
		ID:     "s1",
		Events: []EventType{EventTaskStarted},
		Mode:   ModeObserve,
	})
	if b.SubscriberCount() != 1 {
		t.Fatalf("expected 1 subscriber, got %d", b.SubscriberCount())
	}
	b.Unregister("s1")
	if b.SubscriberCount() != 0 {
		t.Fatalf("expected 0 after unregister, got %d", b.SubscriberCount())
	}
}

func TestWildcardSubscriber(t *testing.T) {
	b := New()
	var called atomic.Int32
	b.Register(Subscriber{
		ID:     "wildcard",
		Events: []EventType{"*"},
		Mode:   ModeGate,
		Handler: func(ctx context.Context, ev *Event) *HookResponse {
			called.Add(1)
			return &HookResponse{Decision: Allow}
		},
	})

	b.Emit(context.Background(), &Event{Type: EventTaskStarted})
	b.Emit(context.Background(), &Event{Type: EventToolPreUse})
	if called.Load() != 2 {
		t.Fatalf("wildcard should fire for all events, got %d calls", called.Load())
	}
}

func TestGateDeny(t *testing.T) {
	b := New()
	b.Register(Subscriber{
		ID:     "blocker",
		Events: []EventType{EventToolBashExec},
		Mode:   ModeGate,
		Handler: func(ctx context.Context, ev *Event) *HookResponse {
			return &HookResponse{Decision: Deny, Reason: "blocked by policy"}
		},
	})

	resp := b.Emit(context.Background(), &Event{Type: EventToolBashExec})
	if resp.Decision != Deny {
		t.Fatalf("expected Deny, got %s", resp.Decision)
	}
	if resp.Reason != "blocked by policy" {
		t.Fatalf("expected reason, got %q", resp.Reason)
	}
}

func TestGateConvenience(t *testing.T) {
	b := New()
	b.Register(Subscriber{
		ID:     "gate",
		Events: []EventType{EventToolBashExec},
		Mode:   ModeGate,
		Handler: func(ctx context.Context, ev *Event) *HookResponse {
			return &HookResponse{Decision: Deny, Reason: "no"}
		},
	})

	allowed, reason := b.Gate(context.Background(), &Event{Type: EventToolBashExec})
	if allowed {
		t.Fatal("expected denied")
	}
	if reason != "no" {
		t.Fatalf("expected reason 'no', got %q", reason)
	}
}

func TestTransformInjections(t *testing.T) {
	b := New()
	b.Register(Subscriber{
		ID:     "injector",
		Events: []EventType{EventPromptBuilding},
		Mode:   ModeTransform,
		Handler: func(ctx context.Context, ev *Event) *HookResponse {
			return &HookResponse{
				Decision: Allow,
				Injections: []Injection{
					{Position: "system", Content: "extra context", Label: "test"},
				},
			}
		},
	})

	injections := b.Transform(context.Background(), &Event{Type: EventPromptBuilding})
	if len(injections) != 1 {
		t.Fatalf("expected 1 injection, got %d", len(injections))
	}
	if injections[0].Content != "extra context" {
		t.Fatalf("unexpected injection content: %s", injections[0].Content)
	}
}

func TestObserveAsync(t *testing.T) {
	b := New()
	var called atomic.Int32
	b.Register(Subscriber{
		ID:     "observer",
		Events: []EventType{EventTaskCompleted},
		Mode:   ModeObserve,
		Handler: func(ctx context.Context, ev *Event) *HookResponse {
			called.Add(1)
			return &HookResponse{Decision: Abstain}
		},
	})

	b.Emit(context.Background(), &Event{Type: EventTaskCompleted})
	// Observer is async; give it time.
	time.Sleep(50 * time.Millisecond)
	if called.Load() != 1 {
		t.Fatalf("observer should have fired, got %d", called.Load())
	}
}

func TestPriorityOrdering(t *testing.T) {
	b := New()
	var order []string
	makeHandler := func(name string) HandlerFunc {
		return func(ctx context.Context, ev *Event) *HookResponse {
			order = append(order, name)
			return &HookResponse{Decision: Allow}
		}
	}

	b.Register(Subscriber{ID: "last", Events: []EventType{EventTaskStarted}, Mode: ModeGate, Priority: 2000, Handler: makeHandler("last")})
	b.Register(Subscriber{ID: "first", Events: []EventType{EventTaskStarted}, Mode: ModeGate, Priority: 100, Handler: makeHandler("first")})
	b.Register(Subscriber{ID: "mid", Events: []EventType{EventTaskStarted}, Mode: ModeGate, Priority: 500, Handler: makeHandler("mid")})

	b.Emit(context.Background(), &Event{Type: EventTaskStarted})
	if len(order) != 3 || order[0] != "first" || order[1] != "mid" || order[2] != "last" {
		t.Fatalf("expected [first mid last], got %v", order)
	}
}

func TestPrefixWildcard(t *testing.T) {
	b := New()
	var called atomic.Int32
	b.Register(Subscriber{
		ID:     "security-all",
		Events: []EventType{"security.*"},
		Mode:   ModeGate,
		Handler: func(ctx context.Context, ev *Event) *HookResponse {
			called.Add(1)
			return &HookResponse{Decision: Allow}
		},
	})

	b.Emit(context.Background(), &Event{Type: EventSecurityScanResult})
	b.Emit(context.Background(), &Event{Type: EventSecuritySecretDetected})
	// Should NOT match non-security events
	b.Emit(context.Background(), &Event{Type: EventTaskStarted})
	if called.Load() != 2 {
		t.Fatalf("prefix wildcard should match 2 security events, got %d", called.Load())
	}
}

func TestHandlerPanicRecovery(t *testing.T) {
	b := New()
	b.Register(Subscriber{
		ID:     "panicker",
		Events: []EventType{EventTaskStarted},
		Mode:   ModeGate,
		Handler: func(ctx context.Context, ev *Event) *HookResponse {
			panic("boom")
		},
	})

	// Should not panic the caller
	resp := b.Emit(context.Background(), &Event{Type: EventTaskStarted})
	if resp.Decision != Allow {
		t.Fatalf("expected Allow after panic recovery, got %s", resp.Decision)
	}
}

func TestNoSubscribersReturnsAllow(t *testing.T) {
	b := New()
	resp := b.Emit(context.Background(), &Event{Type: EventTaskStarted})
	if resp.Decision != Allow {
		t.Fatalf("expected Allow with no subscribers, got %s", resp.Decision)
	}
}

func TestEventIDAutoGenerated(t *testing.T) {
	b := New()
	ev := &Event{Type: EventTaskStarted}
	b.Emit(context.Background(), ev)
	if ev.ID == "" {
		t.Fatal("event ID should be auto-generated")
	}
	if ev.Timestamp.IsZero() {
		t.Fatal("event timestamp should be auto-set")
	}
}

func TestAuditLog(t *testing.T) {
	b := New()
	b.Register(Subscriber{
		ID:     "audited",
		Events: []EventType{EventTaskStarted},
		Mode:   ModeGate,
		Handler: func(ctx context.Context, ev *Event) *HookResponse {
			return &HookResponse{Decision: Allow}
		},
	})

	b.Emit(context.Background(), &Event{Type: EventTaskStarted})
	entries := b.AuditEntries(10)
	if len(entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(entries))
	}
	if entries[0].FinalResult != Allow {
		t.Fatalf("expected Allow in audit, got %s", entries[0].FinalResult)
	}
}

func TestCircuitBreaker(t *testing.T) {
	cb := &CircuitBreaker{MaxFailures: 2, ResetTimeout: 50 * time.Millisecond, HalfOpenMax: 1}
	cb.state = CircuitClosed

	if !cb.Allow() {
		t.Fatal("closed breaker should allow")
	}

	cb.RecordFailure()
	cb.RecordFailure()
	if cb.State() != CircuitOpen {
		t.Fatal("expected open after 2 failures")
	}
	if cb.Allow() {
		t.Fatal("open breaker should deny")
	}

	// Wait for reset timeout
	time.Sleep(60 * time.Millisecond)
	if !cb.Allow() {
		t.Fatal("should allow after reset timeout (half-open)")
	}
	if cb.State() != CircuitHalfOpen {
		t.Fatal("expected half-open")
	}

	cb.RecordSuccess()
	if cb.State() != CircuitClosed {
		t.Fatal("expected closed after success in half-open")
	}
}

func TestCircuitBreakerHalfOpenFailure(t *testing.T) {
	cb := &CircuitBreaker{MaxFailures: 1, ResetTimeout: 10 * time.Millisecond, HalfOpenMax: 1}
	cb.state = CircuitClosed

	cb.RecordFailure()
	time.Sleep(15 * time.Millisecond)
	cb.Allow() // transitions to half-open
	cb.RecordFailure()
	if cb.State() != CircuitOpen {
		t.Fatal("expected re-open after half-open failure")
	}
}

func TestAuditLogRingBuffer(t *testing.T) {
	a := NewAuditLog(3)
	for i := 0; i < 5; i++ {
		a.Record(AuditEntry{EventID: string(rune('a' + i))})
	}
	if a.Len() != 3 {
		t.Fatalf("expected capacity 3, got %d", a.Len())
	}
	recent := a.Recent(2)
	if len(recent) != 2 {
		t.Fatalf("expected 2 recent, got %d", len(recent))
	}
	// Most recent should be 'e' (index 4)
	if recent[0].EventID != string(rune('a'+4)) {
		t.Fatalf("expected most recent 'e', got %q", recent[0].EventID)
	}
}

func TestAuditLogEmpty(t *testing.T) {
	a := NewAuditLog(10)
	recent := a.Recent(5)
	if len(recent) != 0 {
		t.Fatalf("expected empty, got %d", len(recent))
	}
}

func TestMultipleTransformInjections(t *testing.T) {
	b := New()
	b.Register(Subscriber{
		ID: "t1", Events: []EventType{EventPromptBuilding}, Mode: ModeTransform, Priority: 100,
		Handler: func(ctx context.Context, ev *Event) *HookResponse {
			return &HookResponse{Injections: []Injection{{Content: "first"}}}
		},
	})
	b.Register(Subscriber{
		ID: "t2", Events: []EventType{EventPromptBuilding}, Mode: ModeTransform, Priority: 200,
		Handler: func(ctx context.Context, ev *Event) *HookResponse {
			return &HookResponse{Injections: []Injection{{Content: "second"}}}
		},
	})

	resp := b.Emit(context.Background(), &Event{Type: EventPromptBuilding})
	if len(resp.Injections) != 2 {
		t.Fatalf("expected 2 injections, got %d", len(resp.Injections))
	}
}

func TestTransformSuppress(t *testing.T) {
	b := New()
	var secondCalled bool
	b.Register(Subscriber{
		ID: "suppress", Events: []EventType{EventPromptBuilding}, Mode: ModeTransform, Priority: 100,
		Handler: func(ctx context.Context, ev *Event) *HookResponse {
			return &HookResponse{Suppress: true}
		},
	})
	b.Register(Subscriber{
		ID: "after", Events: []EventType{EventPromptBuilding}, Mode: ModeTransform, Priority: 200,
		Handler: func(ctx context.Context, ev *Event) *HookResponse {
			secondCalled = true
			return &HookResponse{Injections: []Injection{{Content: "nope"}}}
		},
	})

	resp := b.Emit(context.Background(), &Event{Type: EventPromptBuilding})
	if secondCalled {
		t.Fatal("second transform should not run after suppress")
	}
	if len(resp.Injections) != 0 {
		t.Fatalf("expected 0 injections after suppress, got %d", len(resp.Injections))
	}
}

func TestCircuitBreakerIntegration(t *testing.T) {
	b := New()
	var calls atomic.Int32
	b.Register(Subscriber{
		ID:     "flaky",
		Events: []EventType{EventTaskStarted},
		Mode:   ModeGate,
		Handler: func(ctx context.Context, ev *Event) *HookResponse {
			calls.Add(1)
			panic("always fails")
		},
	})

	// Trip the breaker (MaxFailures=3 by default)
	for i := 0; i < 4; i++ {
		b.Emit(context.Background(), &Event{Type: EventTaskStarted})
	}

	// After 3 failures, circuit should be open — 4th call should be skipped
	if calls.Load() != 3 {
		t.Fatalf("expected 3 calls before circuit opens, got %d", calls.Load())
	}
}

func TestEmitAsync(t *testing.T) {
	b := New()
	var called atomic.Int32
	b.Register(Subscriber{
		ID:     "async-gate",
		Events: []EventType{EventTaskStarted},
		Mode:   ModeGate,
		Handler: func(ctx context.Context, ev *Event) *HookResponse {
			called.Add(1)
			return &HookResponse{Decision: Allow}
		},
	})

	b.EmitAsync(&Event{Type: EventTaskStarted})
	time.Sleep(50 * time.Millisecond)
	if called.Load() != 1 {
		t.Fatalf("expected 1 async call, got %d", called.Load())
	}
}
