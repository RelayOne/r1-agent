package supervisor_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/ericmacdougall/stoke/internal/bus"
	"github.com/ericmacdougall/stoke/internal/ledger"
	"github.com/ericmacdougall/stoke/internal/supervisor"
)

// mockRule implements supervisor.Rule for testing.
type mockRule struct {
	name      string
	pattern   bus.Pattern
	priority  int
	evalFn    func(ctx context.Context, evt bus.Event, l *ledger.Ledger) (bool, error)
	actionFn  func(ctx context.Context, evt bus.Event, b *bus.Bus) error
	rationale string
}

func (r *mockRule) Name() string                                                            { return r.name }
func (r *mockRule) Pattern() bus.Pattern                                                    { return r.pattern }
func (r *mockRule) Priority() int                                                           { return r.priority }
func (r *mockRule) Evaluate(ctx context.Context, evt bus.Event, l *ledger.Ledger) (bool, error) { return r.evalFn(ctx, evt, l) }
func (r *mockRule) Action(ctx context.Context, evt bus.Event, b *bus.Bus) error             { return r.actionFn(ctx, evt, b) }
func (r *mockRule) Rationale() string                                                       { return r.rationale }

func setupTestInfra(t *testing.T) (*bus.Bus, *ledger.Ledger) {
	t.Helper()
	busDir := t.TempDir()
	b, err := bus.New(busDir)
	if err != nil {
		t.Fatalf("bus.New: %v", err)
	}
	t.Cleanup(func() { b.Close() })

	ledgerDir := t.TempDir()
	l, err := ledger.New(ledgerDir)
	if err != nil {
		t.Fatalf("ledger.New: %v", err)
	}
	t.Cleanup(func() { l.Close() })

	return b, l
}

func TestSupervisorStartStop(t *testing.T) {
	b, l := setupTestInfra(t)

	s := supervisor.New(supervisor.Config{
		ID:                 "test-sup",
		Type:               supervisor.TypeMission,
	}, b, l)

	ctx := context.Background()
	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Double-start should fail.
	if err := s.Start(ctx); err == nil {
		t.Fatal("expected error on double Start")
	}

	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Double-stop should fail.
	if err := s.Stop(); err == nil {
		t.Fatal("expected error on double Stop")
	}
}

func TestRulePriorityOrder(t *testing.T) {
	b, l := setupTestInfra(t)

	var mu sync.Mutex
	var order []string

	makeRule := func(name string, priority int) supervisor.Rule {
		return &mockRule{
			name:     name,
			pattern:  bus.Pattern{TypePrefix: "test."},
			priority: priority,
			evalFn:   func(_ context.Context, _ bus.Event, _ *ledger.Ledger) (bool, error) { return true, nil },
			actionFn: func(_ context.Context, _ bus.Event, _ *bus.Bus) error {
				mu.Lock()
				order = append(order, name)
				mu.Unlock()
				return nil
			},
			rationale: "test",
		}
	}

	s := supervisor.New(supervisor.Config{
		ID:                 "test-sup",
		Type:               supervisor.TypeMission,
	}, b, l)

	s.RegisterRules(
		makeRule("low", 10),
		makeRule("high", 100),
		makeRule("mid", 50),
	)

	rules := s.Rules()
	if len(rules) != 3 {
		t.Fatalf("expected 3 rules, got %d", len(rules))
	}
	if rules[0].Name() != "high" || rules[1].Name() != "mid" || rules[2].Name() != "low" {
		t.Errorf("rules not sorted by priority: %s, %s, %s", rules[0].Name(), rules[1].Name(), rules[2].Name())
	}
}

func TestRulePatternFiltering(t *testing.T) {
	b, l := setupTestInfra(t)

	var fired []string
	var mu sync.Mutex

	s := supervisor.New(supervisor.Config{
		ID:                 "test-sup",
		Type:               supervisor.TypeMission,
	}, b, l)

	s.RegisterRules(
		&mockRule{
			name:     "worker-rule",
			pattern:  bus.Pattern{TypePrefix: "worker."},
			priority: 100,
			evalFn:   func(_ context.Context, _ bus.Event, _ *ledger.Ledger) (bool, error) { return true, nil },
			actionFn: func(_ context.Context, _ bus.Event, _ *bus.Bus) error {
				mu.Lock()
				fired = append(fired, "worker-rule")
				mu.Unlock()
				return nil
			},
		},
		&mockRule{
			name:     "ledger-rule",
			pattern:  bus.Pattern{TypePrefix: "ledger."},
			priority: 90,
			evalFn:   func(_ context.Context, _ bus.Event, _ *ledger.Ledger) (bool, error) { return true, nil },
			actionFn: func(_ context.Context, _ bus.Event, _ *bus.Bus) error {
				mu.Lock()
				fired = append(fired, "ledger-rule")
				mu.Unlock()
				return nil
			},
		},
	)

	ctx := context.Background()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}

	// Publish a worker event — only worker-rule should fire.
	b.Publish(bus.Event{Type: bus.EvtWorkerDeclarationDone})
	time.Sleep(50 * time.Millisecond)

	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()

	if len(fired) != 1 || fired[0] != "worker-rule" {
		t.Errorf("expected [worker-rule], got %v", fired)
	}
}

func TestWizardDisablesRule(t *testing.T) {
	b, l := setupTestInfra(t)

	disabled := false
	disabledPtr := &disabled

	s := supervisor.New(supervisor.Config{
		ID:   "test-sup",
		Type: supervisor.TypeMission,
		RuleOverrides: map[string]supervisor.RuleConfig{
			"disabled-rule": {Enabled: disabledPtr},
		},
	}, b, l)

	s.RegisterRules(
		&mockRule{name: "disabled-rule", pattern: bus.Pattern{TypePrefix: "test."}, priority: 100,
			evalFn: func(_ context.Context, _ bus.Event, _ *ledger.Ledger) (bool, error) { return true, nil },
			actionFn: func(_ context.Context, _ bus.Event, _ *bus.Bus) error { return nil }},
		&mockRule{name: "enabled-rule", pattern: bus.Pattern{TypePrefix: "test."}, priority: 50,
			evalFn: func(_ context.Context, _ bus.Event, _ *ledger.Ledger) (bool, error) { return true, nil },
			actionFn: func(_ context.Context, _ bus.Event, _ *bus.Bus) error { return nil }},
	)

	rules := s.Rules()
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule (disabled-rule removed), got %d", len(rules))
	}
	if rules[0].Name() != "enabled-rule" {
		t.Errorf("expected enabled-rule, got %s", rules[0].Name())
	}
}

func TestCheckpointWritesToLedger(t *testing.T) {
	b, l := setupTestInfra(t)

	s := supervisor.New(supervisor.Config{
		ID:    "test-sup",
		Type:  supervisor.TypeMission,
		Scope: bus.Scope{MissionID: "m-1"},
	}, b, l)

	ctx := context.Background()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}

	// Wait for at least one checkpoint.
	time.Sleep(150 * time.Millisecond)

	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}

	// Query ledger for checkpoint nodes.
	nodes, err := l.Query(ctx, ledger.QueryFilter{Type: "supervisor.checkpoint"})
	if err != nil {
		t.Fatal(err)
	}
	// At least one checkpoint from periodic + one from stop.
	if len(nodes) < 1 {
		t.Errorf("expected at least 1 checkpoint node, got %d", len(nodes))
	}
}

func TestScopeFiltering(t *testing.T) {
	b, l := setupTestInfra(t)

	var fired int
	var mu sync.Mutex

	s := supervisor.New(supervisor.Config{
		ID:   "test-sup",
		Type: supervisor.TypeBranch,
		Scope: bus.Scope{
			MissionID: "m-1",
			BranchID:  "b-1",
		},
	}, b, l)

	s.RegisterRules(&mockRule{
		name:     "branch-rule",
		pattern:  bus.Pattern{TypePrefix: "worker."},
		priority: 100,
		evalFn:   func(_ context.Context, _ bus.Event, _ *ledger.Ledger) (bool, error) { return true, nil },
		actionFn: func(_ context.Context, _ bus.Event, _ *bus.Bus) error {
			mu.Lock()
			fired++
			mu.Unlock()
			return nil
		},
	})

	ctx := context.Background()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}

	// Event matching scope.
	b.Publish(bus.Event{
		Type:  bus.EvtWorkerDeclarationDone,
		Scope: bus.Scope{MissionID: "m-1", BranchID: "b-1"},
	})
	// Event NOT matching scope — different branch.
	b.Publish(bus.Event{
		Type:  bus.EvtWorkerDeclarationDone,
		Scope: bus.Scope{MissionID: "m-1", BranchID: "b-2"},
	})
	time.Sleep(50 * time.Millisecond)

	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if fired != 1 {
		t.Errorf("expected 1 firing (scope-matched), got %d", fired)
	}
}

func TestRuleFiredEventPublished(t *testing.T) {
	b, l := setupTestInfra(t)

	s := supervisor.New(supervisor.Config{
		ID:                 "test-sup",
		Type:               supervisor.TypeMission,
	}, b, l)

	s.RegisterRules(&mockRule{
		name:     "firing-rule",
		pattern:  bus.Pattern{TypePrefix: "worker."},
		priority: 100,
		evalFn:   func(_ context.Context, _ bus.Event, _ *ledger.Ledger) (bool, error) { return true, nil },
		actionFn: func(_ context.Context, _ bus.Event, _ *bus.Bus) error { return nil },
		rationale: "test rationale",
	})

	var ruleFiredEvents []bus.Event
	var mu sync.Mutex
	b.Subscribe(bus.Pattern{TypePrefix: "supervisor.rule.fired"}, func(evt bus.Event) {
		mu.Lock()
		ruleFiredEvents = append(ruleFiredEvents, evt)
		mu.Unlock()
	})

	ctx := context.Background()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}

	b.Publish(bus.Event{Type: bus.EvtWorkerDeclarationDone})
	time.Sleep(50 * time.Millisecond)

	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(ruleFiredEvents) != 1 {
		t.Fatalf("expected 1 rule-fired event, got %d", len(ruleFiredEvents))
	}

	var payload struct {
		RuleName string `json:"rule_name"`
	}
	if err := json.Unmarshal(ruleFiredEvents[0].Payload, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.RuleName != "firing-rule" {
		t.Errorf("expected rule_name=firing-rule, got %s", payload.RuleName)
	}
}

func TestEvaluateFailureDoesNotCrash(t *testing.T) {
	b, l := setupTestInfra(t)

	var actionCalled bool
	var mu sync.Mutex

	s := supervisor.New(supervisor.Config{
		ID:                 "test-sup",
		Type:               supervisor.TypeMission,
	}, b, l)

	s.RegisterRules(
		// Failing rule.
		&mockRule{
			name:     "failing-rule",
			pattern:  bus.Pattern{TypePrefix: "worker."},
			priority: 100,
			evalFn:   func(_ context.Context, _ bus.Event, _ *ledger.Ledger) (bool, error) { return false, context.DeadlineExceeded },
			actionFn: func(_ context.Context, _ bus.Event, _ *bus.Bus) error { return nil },
		},
		// Subsequent rule that should still fire.
		&mockRule{
			name:     "good-rule",
			pattern:  bus.Pattern{TypePrefix: "worker."},
			priority: 50,
			evalFn:   func(_ context.Context, _ bus.Event, _ *ledger.Ledger) (bool, error) { return true, nil },
			actionFn: func(_ context.Context, _ bus.Event, _ *bus.Bus) error {
				mu.Lock()
				actionCalled = true
				mu.Unlock()
				return nil
			},
		},
	)

	ctx := context.Background()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}

	b.Publish(bus.Event{Type: bus.EvtWorkerDeclarationDone})
	time.Sleep(50 * time.Millisecond)

	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !actionCalled {
		t.Error("expected good-rule to fire despite failing-rule's evaluate error")
	}
}
