package drift

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1-agent/internal/bus"
	"github.com/RelayOne/r1-agent/internal/ledger"
)

// collectPublished subscribes to the bus, runs the given function, and
// waits up to 1s for at least `min` events. Returns the captured events.
func collectPublished(t *testing.T, b *bus.Bus, min int, fn func()) []bus.Event {
	t.Helper()
	var published []bus.Event
	var mu sync.Mutex
	b.Subscribe(bus.Pattern{}, func(e bus.Event) {
		mu.Lock()
		published = append(published, e)
		mu.Unlock()
	})
	fn()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(published)
		mu.Unlock()
		if n >= min {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	out := make([]bus.Event, len(published))
	copy(out, published)
	return out
}

// TestBudgetThreshold_Evaluate_ZeroBudgetNoFire asserts that a zero or
// negative budget does not fire the rule — we cannot compute a ratio
// and must not divide by zero.
func TestBudgetThreshold_Evaluate_ZeroBudgetNoFire(t *testing.T) {
	ctx := context.Background()
	lDir := t.TempDir()
	l, err := ledger.New(lDir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	rule := NewBudgetThreshold()
	payload, _ := json.Marshal(budgetPayload{SpentUSD: 99.0, BudgetUSD: 0.0})
	evt := bus.Event{
		ID:      "z",
		Type:    "mission.budget.update",
		Scope:   bus.Scope{MissionID: "m1"},
		Payload: payload,
	}
	fire, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if fire {
		t.Error("expected rule NOT to fire with zero budget (div by zero guard)")
	}
}

// TestBudgetThreshold_Evaluate_BadPayload asserts that an unparseable
// JSON payload surfaces as an error (operators see the parse failure
// in logs rather than a silent no-op).
func TestBudgetThreshold_Evaluate_BadPayload(t *testing.T) {
	rule := NewBudgetThreshold()
	evt := bus.Event{
		ID:      "bad",
		Type:    "mission.budget.update",
		Payload: []byte("not-json"),
	}
	fire, err := rule.Evaluate(context.Background(), evt, nil)
	if err == nil {
		t.Fatal("expected error on unparseable payload, got nil")
	}
	if fire {
		t.Error("expected fire=false when payload invalid")
	}
}

// TestBudgetThreshold_Action_CheckPct fires at 85% and expects a Judge
// spawn request (role: Judge, reason: budget_check).
func TestBudgetThreshold_Action_CheckPct(t *testing.T) {
	b, err := bus.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	rule := NewBudgetThreshold()
	payload, _ := json.Marshal(budgetPayload{SpentUSD: 8.5, BudgetUSD: 10.0}) // 85%
	evt := bus.Event{ID: "c", Type: "mission.budget.update", Scope: bus.Scope{MissionID: "m1"}, Payload: payload}

	events := collectPublished(t, b, 1, func() {
		if err := rule.Action(context.Background(), evt, b); err != nil {
			t.Fatalf("Action: %v", err)
		}
	})
	if len(events) < 1 {
		t.Fatal("expected spawn.requested event")
	}
	if events[0].Type != "supervisor.spawn.requested" {
		t.Errorf("type = %q, want supervisor.spawn.requested", events[0].Type)
	}
	var body map[string]any
	if err := json.Unmarshal(events[0].Payload, &body); err != nil {
		t.Fatalf("payload JSON: %v", err)
	}
	if body["role"] != "Judge" {
		t.Errorf("role = %v, want Judge", body["role"])
	}
	if body["reason"] != "budget_check" {
		t.Errorf("reason = %v, want budget_check", body["reason"])
	}
	// pct_used should be 85 (spent/budget * 100).
	if pct, _ := body["pct_used"].(float64); pct != 85 {
		t.Errorf("pct_used = %v, want 85", body["pct_used"])
	}
}

// TestBudgetThreshold_Action_EscalatePct fires at 100% and expects a
// PO escalation event (supervisor.escalation.requested, role: PO).
func TestBudgetThreshold_Action_EscalatePct(t *testing.T) {
	b, err := bus.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	rule := NewBudgetThreshold()
	payload, _ := json.Marshal(budgetPayload{SpentUSD: 10.0, BudgetUSD: 10.0}) // 100%
	evt := bus.Event{ID: "e", Type: "mission.budget.update", Scope: bus.Scope{MissionID: "m1"}, Payload: payload}

	events := collectPublished(t, b, 1, func() {
		if err := rule.Action(context.Background(), evt, b); err != nil {
			t.Fatalf("Action: %v", err)
		}
	})
	if len(events) < 1 {
		t.Fatal("expected escalation event")
	}
	if events[0].Type != "supervisor.escalation.requested" {
		t.Errorf("type = %q, want supervisor.escalation.requested", events[0].Type)
	}
	var body map[string]any
	_ = json.Unmarshal(events[0].Payload, &body)
	if body["role"] != "PO" {
		t.Errorf("role = %v, want PO", body["role"])
	}
	if body["action"] != "escalate_to_user" {
		t.Errorf("action = %v, want escalate_to_user", body["action"])
	}
}

// TestBudgetThreshold_Action_ZeroBudgetNoOp verifies Action returns nil
// and publishes nothing when the payload has a non-positive budget.
func TestBudgetThreshold_Action_ZeroBudgetNoOp(t *testing.T) {
	b, err := bus.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	rule := NewBudgetThreshold()
	payload, _ := json.Marshal(budgetPayload{SpentUSD: 99.0, BudgetUSD: 0.0})
	evt := bus.Event{ID: "z2", Type: "mission.budget.update", Payload: payload}

	events := collectPublished(t, b, 0, func() {
		if err := rule.Action(context.Background(), evt, b); err != nil {
			t.Fatalf("Action returned %v, want nil for zero budget", err)
		}
	})
	if len(events) != 0 {
		t.Errorf("expected 0 events for zero budget, got %d: %+v", len(events), events)
	}
}

// TestBudgetThreshold_PayloadSchema_NilMatchesContract documents the
// current behavior: this rule does not declare a single payload schema
// (it emits different event types with different shapes), so
// PayloadSchema returns nil.
func TestBudgetThreshold_PayloadSchema_NilMatchesContract(t *testing.T) {
	rule := NewBudgetThreshold()
	if got := rule.PayloadSchema(); got != nil {
		t.Errorf("PayloadSchema = %v, want nil (mixed emit shapes)", got)
	}
}

// TestBudgetThreshold_DefaultConfig_PctConstants locks the default
// threshold configuration so nobody silently shifts the bands.
func TestBudgetThreshold_DefaultConfig_PctConstants(t *testing.T) {
	r := NewBudgetThreshold()
	if r.WarningPct != 50 {
		t.Errorf("WarningPct = %v, want 50", r.WarningPct)
	}
	if r.CheckPct != 80 {
		t.Errorf("CheckPct = %v, want 80", r.CheckPct)
	}
	if r.EscalatePct != 100 {
		t.Errorf("EscalatePct = %v, want 100", r.EscalatePct)
	}
	if r.StopPct != 120 {
		t.Errorf("StopPct = %v, want 120", r.StopPct)
	}
}

// TestJudgeScheduled_Evaluate_UnrelatedEventType verifies that the rule
// short-circuits to false for events it does not handle (anything
// other than drift.judge.timeout or ledger.node.added).
func TestJudgeScheduled_Evaluate_UnrelatedEventType(t *testing.T) {
	ctx := context.Background()
	l, err := ledger.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	rule := NewJudgeScheduled()
	evt := bus.Event{ID: "x", Type: "mission.started"}
	fire, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if fire {
		t.Error("expected rule NOT to fire on unrelated event type")
	}
}

// TestJudgeScheduled_Evaluate_TimeoutEmptyDraftID covers the payload
// guard: a timeout with no DraftNodeID must not fire (prevents a
// Resolve() on an empty ID).
func TestJudgeScheduled_Evaluate_TimeoutEmptyDraftID(t *testing.T) {
	ctx := context.Background()
	l, err := ledger.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	rule := NewJudgeScheduled()
	payload, _ := json.Marshal(judgeTimeoutPayload{DraftNodeID: ""})
	evt := bus.Event{ID: "t", Type: "drift.judge.timeout", Payload: payload}
	fire, err := rule.Evaluate(ctx, evt, l)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if fire {
		t.Error("expected rule NOT to fire when DraftNodeID is empty")
	}
}

// TestSimpleSimilarity_IdenticalAndDisjoint exercises the Jaccard
// similarity helper on two boundary cases: identical strings → 1.0,
// completely disjoint → 0.0.
func TestSimpleSimilarity_IdenticalAndDisjoint(t *testing.T) {
	if sim := simpleSimilarity("foo bar baz", "foo bar baz"); sim != 1.0 {
		t.Errorf("identical similarity = %v, want 1.0", sim)
	}
	if sim := simpleSimilarity("foo bar", "xxx yyy"); sim != 0.0 {
		t.Errorf("disjoint similarity = %v, want 0.0", sim)
	}
	// Empty input returns 0.0 (division guard).
	if sim := simpleSimilarity("", "anything"); sim != 0.0 {
		t.Errorf("empty similarity = %v, want 0.0", sim)
	}
}

// TestSimpleSimilarity_PartialOverlap locks the Jaccard math for a
// known overlap: "a b c" vs "b c d" → intersection=2, union=4 → 0.5.
func TestSimpleSimilarity_PartialOverlap(t *testing.T) {
	got := simpleSimilarity("a b c", "b c d")
	if got != 0.5 {
		t.Errorf("simpleSimilarity('a b c','b c d') = %v, want 0.5", got)
	}
}

// TestIntentAlignmentCheck_Action_PayloadContents verifies the spawn
// request payload carries the expected role, reason, trigger_id and
// task_id so downstream rules can route properly.
func TestIntentAlignmentCheck_Action_PayloadContents(t *testing.T) {
	b, err := bus.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	rule := NewIntentAlignmentCheck()
	evt := bus.Event{
		ID:    "milestone-xyz",
		Type:  "task.milestone.reached",
		Scope: bus.Scope{MissionID: "m1", TaskID: "task-77"},
	}

	events := collectPublished(t, b, 1, func() {
		if err := rule.Action(context.Background(), evt, b); err != nil {
			t.Fatalf("Action: %v", err)
		}
	})
	if len(events) < 1 {
		t.Fatal("expected spawn event")
	}
	var body map[string]any
	if err := json.Unmarshal(events[0].Payload, &body); err != nil {
		t.Fatalf("payload JSON: %v", err)
	}
	if body["role"] != "Judge" {
		t.Errorf("role = %v, want Judge", body["role"])
	}
	if body["reason"] != "intent_alignment_check" {
		t.Errorf("reason = %v, want intent_alignment_check", body["reason"])
	}
	if body["trigger_id"] != "milestone-xyz" {
		t.Errorf("trigger_id = %v, want milestone-xyz", body["trigger_id"])
	}
	if body["task_id"] != "task-77" {
		t.Errorf("task_id = %v, want task-77", body["task_id"])
	}
	// CausalRef on the event itself should also reference the trigger.
	if events[0].CausalRef != "milestone-xyz" {
		t.Errorf("CausalRef = %q, want milestone-xyz", events[0].CausalRef)
	}
}
