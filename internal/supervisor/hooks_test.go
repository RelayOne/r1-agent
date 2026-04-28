package supervisor_test

// hooks_test.go — covers RegisterHookRules with both:
//
//   - a mock rule that DOES implement HookRule (must register and fire
//     with all four HookAction kinds: inject, pause, resume, spawn).
//   - a mock rule that does NOT implement HookRule (must not register
//     and must not fire on the publish path beyond Subscribe).
//
// Closes R1-V1 audit Domain 9 P0 #1 / PR #24 HIGH-1: prior to this
// test, no rule in the codebase implemented HookRule, so RegisterHookRules
// always returned 0 and the privileged-hook code path was unexercised.

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/supervisor"
)

// hookMockRule implements both supervisor.Rule and supervisor.HookRule.
type hookMockRule struct {
	mockRule
	hookPriority bus.HookPriority
	hookActionFn func(ctx context.Context, evt bus.Event) (*bus.HookAction, error)
}

func (r *hookMockRule) HookPriority() bus.HookPriority { return r.hookPriority }
func (r *hookMockRule) HookAction(ctx context.Context, evt bus.Event) (*bus.HookAction, error) {
	return r.hookActionFn(ctx, evt)
}

func TestRegisterHookRulesRegistersOnlyHookRules(t *testing.T) {
	b, l := setupTestInfra(t)

	plain := &mockRule{
		name:     "plain.observer",
		pattern:  bus.Pattern{TypePrefix: "worker.action."},
		priority: 10,
		evalFn:   func(_ context.Context, _ bus.Event, _ *ledger.Ledger) (bool, error) { return true, nil },
		actionFn: func(_ context.Context, _ bus.Event, _ *bus.Bus) error { return nil },
	}

	var hookCalls atomic.Int32
	hooky := &hookMockRule{
		mockRule: mockRule{
			name:     "gate.example",
			pattern:  bus.Pattern{TypePrefix: "worker.action."},
			priority: 50,
			evalFn:   func(_ context.Context, _ bus.Event, _ *ledger.Ledger) (bool, error) { return true, nil },
			actionFn: func(_ context.Context, _ bus.Event, _ *bus.Bus) error { return nil },
		},
		hookPriority: 100,
		hookActionFn: func(_ context.Context, _ bus.Event) (*bus.HookAction, error) {
			hookCalls.Add(1)
			return &bus.HookAction{
				PauseWorker:  "w-1",
				ResumeWorker: "w-2",
				SpawnWorker:  &bus.SpawnRequest{Role: "CTO"},
				InjectEvents: []bus.Event{{Type: "test.injected"}},
			}, nil
		},
	}

	s := supervisor.New(supervisor.Config{
		ID:   "hook-sup",
		Type: supervisor.TypeMission,
	}, b, l)
	s.RegisterRules(plain, hooky)

	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Stop()

	// RegisterHookRules is invoked from Start; verify only the HookRule
	// got wired by counting via a direct re-call.
	count, err := s.RegisterHookRules(context.Background())
	if err != nil {
		t.Fatalf("RegisterHookRules: %v", err)
	}
	if count != 1 {
		t.Fatalf("RegisterHookRules count = %d, want 1 (only gate.example implements HookRule)", count)
	}

	// Subscribe to all events so we can observe the materialized side-effects.
	var mu sync.Mutex
	var seen []bus.Event
	b.Subscribe(bus.Pattern{}, func(e bus.Event) {
		mu.Lock()
		seen = append(seen, e)
		mu.Unlock()
	})

	// Publish an event that matches the hook's pattern. Note: Start has
	// already registered the HookRule once; re-registering above wired
	// the same handler a second time. We accept the duplicate firings
	// in this test by counting types rather than absolute event count.
	payload, _ := json.Marshal(map[string]any{"worker_id": "w-1"})
	if err := b.Publish(bus.Event{
		Type:    "worker.action.proposed",
		Payload: payload,
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// Wait for hook to fire at least once.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hookCalls.Load() >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if hookCalls.Load() < 1 {
		t.Fatalf("HookAction was never invoked (count=%d) — RegisterHookRules failed to wire", hookCalls.Load())
	}

	// Drain the deferred materialized events.
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	hasType := func(want bus.EventType) bool {
		for _, e := range seen {
			if e.Type == want {
				return true
			}
		}
		return false
	}
	for _, want := range []bus.EventType{
		"worker.action.proposed",
		"test.injected",
		bus.EvtWorkerPaused,
		bus.EvtWorkerResumed,
		bus.EvtSupervisorSpawnRequested,
	} {
		if !hasType(want) {
			t.Errorf("expected event type %q in subscribed stream; saw %d events", want, len(seen))
		}
	}
}
