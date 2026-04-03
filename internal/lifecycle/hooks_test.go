package lifecycle

import (
	"context"
	"testing"
)

func TestRegistryRegisterAndDispatch(t *testing.T) {
	reg := NewRegistry()
	called := false

	reg.Register(Hook{
		Name:   "test-hook",
		Tier:   TierSession,
		Events: []Event{EventSessionStart},
		Fn: func(ctx context.Context, hctx *HookContext) HookResult {
			called = true
			return HookResult{Decision: DecisionAllow}
		},
	})

	hctx := &HookContext{Event: EventSessionStart}
	result := reg.Dispatch(context.Background(), hctx)

	if !called {
		t.Error("hook should have been called")
	}
	if result.Decision != DecisionAllow {
		t.Errorf("expected allow, got %s", result.Decision)
	}
}

func TestRegistryDenyShortCircuits(t *testing.T) {
	reg := NewRegistry()
	secondCalled := false

	reg.Register(Hook{
		Name:     "denier",
		Tier:     TierToolGuard,
		Events:   []Event{EventPreToolUse},
		Priority: 10,
		Fn: func(ctx context.Context, hctx *HookContext) HookResult {
			return HookResult{Decision: DecisionDeny, Reason: "blocked"}
		},
	})
	reg.Register(Hook{
		Name:     "after-deny",
		Tier:     TierToolGuard,
		Events:   []Event{EventPreToolUse},
		Priority: 20,
		Fn: func(ctx context.Context, hctx *HookContext) HookResult {
			secondCalled = true
			return HookResult{Decision: DecisionAllow}
		},
	})

	hctx := &HookContext{Event: EventPreToolUse}
	result := reg.Dispatch(context.Background(), hctx)

	if result.Decision != DecisionDeny {
		t.Error("expected deny")
	}
	if secondCalled {
		t.Error("second hook should not run after deny")
	}
	if !hctx.Cancelled {
		t.Error("context should be marked cancelled")
	}
}

func TestRegistryPriorityOrder(t *testing.T) {
	reg := NewRegistry()
	var order []string

	for _, p := range []struct {
		name string
		prio int
	}{{"c", 30}, {"a", 10}, {"b", 20}} {
		name := p.name
		reg.Register(Hook{
			Name:     name,
			Tier:     TierSession,
			Events:   []Event{EventSessionInit},
			Priority: p.prio,
			Fn: func(ctx context.Context, hctx *HookContext) HookResult {
				order = append(order, name)
				return HookResult{Decision: DecisionContinue}
			},
		})
	}

	reg.Dispatch(context.Background(), &HookContext{Event: EventSessionInit})

	if len(order) != 3 || order[0] != "a" || order[1] != "b" || order[2] != "c" {
		t.Errorf("expected [a b c], got %v", order)
	}
}

func TestRegistryModify(t *testing.T) {
	reg := NewRegistry()

	reg.Register(Hook{
		Name:   "modifier",
		Tier:   TierTransform,
		Events: []Event{EventPromptTransform},
		Fn: func(ctx context.Context, hctx *HookContext) HookResult {
			return HookResult{Decision: DecisionModify, Output: hctx.Output + " INJECTED"}
		},
	})

	hctx := &HookContext{Event: EventPromptTransform, Output: "original"}
	reg.Dispatch(context.Background(), hctx)

	if hctx.Output != "original INJECTED" {
		t.Errorf("expected modified output, got %q", hctx.Output)
	}
}

func TestFileProtectionHook(t *testing.T) {
	hook := FileProtectionHook([]string{"go.mod", "go.sum"})
	reg := NewRegistry()
	reg.Register(hook)

	// Protected file
	hctx := &HookContext{Event: EventPreToolUse, FilePath: "go.mod"}
	result := reg.Dispatch(context.Background(), hctx)
	if result.Decision != DecisionDeny {
		t.Error("should deny write to protected file")
	}

	// Non-protected file
	hctx2 := &HookContext{Event: EventPreToolUse, FilePath: "main.go"}
	result2 := reg.Dispatch(context.Background(), hctx2)
	if result2.Decision == DecisionDeny {
		t.Error("should allow write to non-protected file")
	}
}

func TestScopeEnforcementHook(t *testing.T) {
	hook := ScopeEnforcementHook([]string{"src/auth.go", "src/auth_test.go"})
	reg := NewRegistry()
	reg.Register(hook)

	// In scope
	hctx := &HookContext{Event: EventPreToolUse, FilePath: "src/auth.go"}
	result := reg.Dispatch(context.Background(), hctx)
	if result.Decision == DecisionDeny {
		t.Error("should allow in-scope file")
	}

	// Out of scope
	hctx2 := &HookContext{Event: EventPreToolUse, FilePath: "src/main.go"}
	result2 := reg.Dispatch(context.Background(), hctx2)
	if result2.Decision != DecisionDeny {
		t.Error("should deny out-of-scope file")
	}
}

func TestContextInjectionHook(t *testing.T) {
	hook := ContextInjectionHook("test-inject", func() string { return "REMINDER: do not skip tests" })
	reg := NewRegistry()
	reg.Register(hook)

	hctx := &HookContext{Event: EventContextInject, Output: "base prompt"}
	reg.Dispatch(context.Background(), hctx)

	if hctx.Output != "base prompt\nREMINDER: do not skip tests" {
		t.Errorf("unexpected output: %q", hctx.Output)
	}
}

func TestRegistryStats(t *testing.T) {
	reg := NewRegistry()
	reg.Register(Hook{
		Name:   "stats-test",
		Tier:   TierSession,
		Events: []Event{EventSessionStart},
		Fn: func(ctx context.Context, hctx *HookContext) HookResult {
			return HookResult{Decision: DecisionAllow}
		},
	})

	reg.Dispatch(context.Background(), &HookContext{Event: EventSessionStart})
	reg.Dispatch(context.Background(), &HookContext{Event: EventSessionStart})

	stats := reg.Stats("stats-test")
	if stats.Invocations != 2 {
		t.Errorf("expected 2 invocations, got %d", stats.Invocations)
	}
}

func TestRegistryCount(t *testing.T) {
	reg := NewRegistry()
	reg.Register(Hook{Name: "a", Events: []Event{EventSessionStart, EventSessionComplete}, Fn: func(ctx context.Context, hctx *HookContext) HookResult { return HookResult{} }})
	reg.Register(Hook{Name: "b", Events: []Event{EventSessionStart}, Fn: func(ctx context.Context, hctx *HookContext) HookResult { return HookResult{} }})

	if reg.Count() != 2 {
		t.Errorf("expected 2 unique hooks, got %d", reg.Count())
	}
}

func TestHooksFor(t *testing.T) {
	reg := NewRegistry()
	reg.Register(Hook{Name: "h1", Events: []Event{EventSessionStart}, Priority: 10, Fn: func(ctx context.Context, hctx *HookContext) HookResult { return HookResult{} }})
	reg.Register(Hook{Name: "h2", Events: []Event{EventSessionStart}, Priority: 20, Fn: func(ctx context.Context, hctx *HookContext) HookResult { return HookResult{} }})

	names := reg.HooksFor(EventSessionStart)
	if len(names) != 2 || names[0] != "h1" || names[1] != "h2" {
		t.Errorf("expected [h1 h2], got %v", names)
	}
}

func TestNoHooksReturnsAllow(t *testing.T) {
	reg := NewRegistry()
	result := reg.Dispatch(context.Background(), &HookContext{Event: EventSessionStart})
	if result.Decision != DecisionAllow {
		t.Error("no hooks should return allow")
	}
}
