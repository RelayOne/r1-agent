package workflow

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/config"
	"github.com/RelayOne/r1/internal/costtrack"
	"github.com/RelayOne/r1/internal/engine"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/model"
	"github.com/RelayOne/r1/internal/taskstate"
	"github.com/RelayOne/r1/internal/verify"
)

// TestWorkflowBudgetHaltE2E is the R1-3 end-to-end assertion for the
// site claim "per-build budget ceilings halt runs before overrun".
//
// Unlike the costtrack-level unit test (which models the halt shape),
// this test drives the REAL Engine.Run path: a pre-populated Tracker
// enters the attempt loop already over its ceiling, and we assert the
// three contract points from the live engine:
//
//  1. The returned error matches the engine's halt shape —
//     `budget exceeded ($X.XX spent), aborting`
//     (see internal/workflow/workflow.go budget gate).
//  2. A hub.EventCostBudgetExceeded event was emitted with a
//     populated CostEvent payload (TotalSpent, BudgetLimit,
//     PercentUsed, Threshold == "exceeded").
//  3. The Tracker's OverBudget() reports true and BudgetRemaining()
//     is negative after the halt.
//
// If a refactor changes the halt wording, the event type, or the
// CostEvent payload shape, this test catches the divergence.
func TestWorkflowBudgetHaltE2E(t *testing.T) {
	repo := initTestRepo(t)

	// Pre-populate the tracker so we enter the attempt loop already
	// over budget. The workflow checks OverBudget() BEFORE spending
	// a new attempt, so the gate fires on attempt 1.
	const budget = 0.01
	tracker := costtrack.NewTracker(budget, nil)
	// Claude Opus: $15/M input + $75/M output. 100k in + 50k out is
	// well over $0.01.
	tracker.Record("claude-opus-4", "prior-session-spend", 100_000, 50_000, 0, 0)
	if !tracker.OverBudget() {
		t.Fatalf("test arrangement error: tracker should be over budget before Run() (spent=%v, budget=%v)", tracker.Total(), budget)
	}

	// Subscribe an observer to capture the halt event. The engine
	// emits this event via EmitAsync (goroutine-dispatched), so we
	// also expose a signal channel the test can block on.
	bus := hub.New()
	var (
		capturedMu sync.Mutex
		captured   []*hub.Event
	)
	eventCh := make(chan *hub.Event, 4)
	bus.Register(hub.Subscriber{
		ID:     "r1-3-budget-halt-observer",
		Events: []hub.EventType{hub.EventCostBudgetExceeded},
		Mode:   hub.ModeObserve,
		Handler: func(_ context.Context, ev *hub.Event) *hub.HookResponse {
			capturedMu.Lock()
			// Copy-out so later mutations don't alias into our capture.
			cp := *ev
			captured = append(captured, &cp)
			capturedMu.Unlock()
			select {
			case eventCh <- &cp:
			default:
			}
			return nil
		},
	})

	mock := newMockRunner()
	policy := config.DefaultPolicy()
	// Disable verify gates — we want the budget gate alone to stop us.
	policy.Verification.Build = false
	policy.Verification.Tests = false
	policy.Verification.Lint = false
	policy.Verification.ScopeCheck = false
	policy.Verification.CrossModelReview = false

	state := taskstate.NewTaskState("r1-3-budget-halt")

	wf := Engine{
		RepoRoot:       repo,
		Task:           "R1-3 budget halt smoke",
		TaskType:       model.TaskTypeRefactor,
		WorktreeName:   "r1-3-budget-halt",
		AuthMode:       engine.AuthModeMode1,
		Policy:         policy,
		Pools:          nil,
		Worktrees:      stubManager{repo: repo},
		Runners:        engine.Registry{Claude: engine.NewClaudeRunner("claude")},
		Verifier:       verify.NewPipeline("", "", ""),
		State:          state,
		CostTracker:    tracker,
		EventBus:       bus,
		RunnerOverride: mock,
	}

	_, err := wf.Run(context.Background())

	// --- assert #1: halt error shape ---
	if err == nil {
		t.Fatal("expected budget-exceeded error from Engine.Run, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "budget exceeded") {
		t.Errorf("error %q missing substring 'budget exceeded'", msg)
	}
	if !strings.Contains(msg, "aborting") {
		t.Errorf("error %q missing substring 'aborting'", msg)
	}
	if !strings.Contains(msg, "$") {
		t.Errorf("error %q missing dollar-sign spent-amount marker", msg)
	}

	// --- assert #2: event emitted with populated payload ---
	// EmitAsync dispatches in a goroutine, so block (with timeout)
	// until the observer signals receipt before snapshotting.
	select {
	case <-eventCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for EventCostBudgetExceeded after Engine.Run returned")
	}
	capturedMu.Lock()
	snapshot := append([]*hub.Event(nil), captured...)
	capturedMu.Unlock()

	if len(snapshot) == 0 {
		t.Fatal("expected at least one EventCostBudgetExceeded, got none")
	}
	found := false
	for _, ev := range snapshot {
		if ev.Type != hub.EventCostBudgetExceeded {
			continue
		}
		found = true
		if ev.Cost == nil {
			t.Error("EventCostBudgetExceeded has nil Cost payload")
			continue
		}
		if ev.Cost.TotalSpent <= 0 {
			t.Errorf("Cost.TotalSpent = %v, want > 0", ev.Cost.TotalSpent)
		}
		if ev.Cost.TotalSpent <= budget {
			t.Errorf("Cost.TotalSpent = %v, want > budget %v", ev.Cost.TotalSpent, budget)
		}
		const eps = 1e-9
		if ev.Cost.BudgetLimit < budget-eps || ev.Cost.BudgetLimit > budget+eps {
			t.Errorf("Cost.BudgetLimit = %v, want %v (round-trip from Total+Remaining)", ev.Cost.BudgetLimit, budget)
		}
		if ev.Cost.PercentUsed < 100 {
			t.Errorf("Cost.PercentUsed = %v, want >= 100", ev.Cost.PercentUsed)
		}
		if ev.Cost.Threshold != "exceeded" {
			t.Errorf("Cost.Threshold = %q, want %q", ev.Cost.Threshold, "exceeded")
		}
		if ev.TaskID == "" {
			t.Error("event TaskID should be populated with the task name")
		}
	}
	if !found {
		t.Fatalf("none of the %d captured events were EventCostBudgetExceeded", len(snapshot))
	}

	// --- assert #3: tracker state matches the halt claim ---
	if !tracker.OverBudget() {
		t.Error("Tracker.OverBudget() should still be true after halt")
	}
	if tracker.BudgetRemaining() >= 0 {
		t.Errorf("Tracker.BudgetRemaining() = %v, want negative", tracker.BudgetRemaining())
	}
}

// TestWorkflowBudgetHaltHappyPath asserts the negative direction:
// when the tracker is under budget, Engine.Run does NOT emit an
// EventCostBudgetExceeded and does NOT return the halt error.
// Without this control, a future regression that makes OverBudget
// always-true would still pass the halt test above.
func TestWorkflowBudgetHaltHappyPath(t *testing.T) {
	repo := initTestRepo(t)

	// Generous budget — any spend the mock records stays well inside.
	tracker := costtrack.NewTracker(1000.0, nil)

	bus := hub.New()
	haltCh := make(chan hub.EventType, 4)
	bus.Register(hub.Subscriber{
		ID:     "r1-3-happy-path-observer",
		Events: []hub.EventType{hub.EventCostBudgetExceeded},
		Mode:   hub.ModeObserve,
		Handler: func(_ context.Context, ev *hub.Event) *hub.HookResponse {
			select {
			case haltCh <- ev.Type:
			default:
			}
			return nil
		},
	})

	mock := newMockRunner()
	policy := config.DefaultPolicy()
	policy.Verification.Build = false
	policy.Verification.Tests = false
	policy.Verification.Lint = false
	policy.Verification.ScopeCheck = false
	policy.Verification.CrossModelReview = false

	wf := Engine{
		RepoRoot:       repo,
		Task:           "R1-3 happy path",
		TaskType:       model.TaskTypeRefactor,
		WorktreeName:   "r1-3-happy",
		AuthMode:       engine.AuthModeMode1,
		Policy:         policy,
		Worktrees:      stubManager{repo: repo},
		Runners:        engine.Registry{Claude: engine.NewClaudeRunner("claude")},
		Verifier:       verify.NewPipeline("", "", ""),
		State:          taskstate.NewTaskState("r1-3-happy"),
		CostTracker:    tracker,
		EventBus:       bus,
		RunnerOverride: mock,
	}

	_, err := wf.Run(context.Background())
	// Merge may fail because we don't have a real git branch, but
	// anything before merge should succeed. The error (if any) must
	// NOT be a budget-halt error.
	if err != nil && strings.Contains(err.Error(), "budget exceeded") {
		t.Fatalf("unexpected budget-halt error when under budget: %v", err)
	}
	if tracker.OverBudget() {
		t.Errorf("tracker should not be over budget: total=%v, budget=%v", tracker.Total(), 1000.0)
	}

	// Drain briefly: if a halt event was going to fire, it would have
	// been dispatched via EmitAsync by the time Run returned. A 200ms
	// window gives the goroutine more than enough slack. If anything
	// lands, that's a regression.
	select {
	case ev := <-haltCh:
		t.Errorf("unexpected EventCostBudgetExceeded when under budget: %v", ev)
	case <-time.After(200 * time.Millisecond):
		// Good — no halt event, as expected.
	}
}
