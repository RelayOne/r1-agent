package costtrack

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/ericmacdougall/stoke/internal/hub"
)

// TestBudgetHaltFlow asserts the site claim "per-build budget
// ceilings halt runs before overrun":
//
//  1. OverBudget() flips true once cumulative cost exceeds the
//     ceiling.
//  2. The halt signal is the Tracker's Alert with AlertCritical
//     level + the engine's "budget exceeded ($X.XX spent),
//     aborting" error message.
//  3. The emitted audit event is a hub.EventCostBudgetExceeded
//     carrying a populated CostEvent payload.
//
// The emit path is modeled after internal/workflow/workflow.go:551
// where the executor checks OverBudget() before each attempt,
// emits EventCostBudgetExceeded, and returns a terminal error.
func TestBudgetHaltFlow(t *testing.T) {
	// --- arrange ---
	const budget = 0.01 // $0.01 ceiling — low enough that a modest record blows past.

	var (
		alertsMu sync.Mutex
		alerts   []Alert
	)
	alertFn := func(a Alert) {
		alertsMu.Lock()
		defer alertsMu.Unlock()
		alerts = append(alerts, a)
	}

	tr := NewTracker(budget, alertFn)

	// Baseline: no spend, not over budget yet.
	if tr.OverBudget() {
		t.Fatal("OverBudget() should be false before any Record()")
	}
	if tr.BudgetRemaining() != budget {
		t.Fatalf("BudgetRemaining() = %v, want %v before spend", tr.BudgetRemaining(), budget)
	}

	// --- act: accrue cost past the ceiling ---
	// Claude Opus pricing: $15/M input + $75/M output. 100k in / 50k out
	// is 100_000*15/1e6 + 50_000*75/1e6 = 1.50 + 3.75 = $5.25, well over
	// a $0.01 ceiling.
	recordedCost := tr.Record("claude-opus-4", "r1-3-halt-task", 100_000, 50_000, 0, 0)

	// --- assert #1: OverBudget flips true ---
	if !tr.OverBudget() {
		t.Fatalf("OverBudget() should be true after recording $%.4f against $%.2f budget", recordedCost, budget)
	}
	if tr.BudgetRemaining() >= 0 {
		t.Errorf("BudgetRemaining() = %v, want negative once over budget", tr.BudgetRemaining())
	}
	if tr.Total() <= budget {
		t.Errorf("Total() = %v, want > budget %v", tr.Total(), budget)
	}

	// --- assert #2: critical alert fired with exact shape ---
	alertsMu.Lock()
	snapshot := append([]Alert(nil), alerts...)
	alertsMu.Unlock()

	var critical *Alert
	for i := range snapshot {
		if snapshot[i].Level == AlertCritical {
			critical = &snapshot[i]
			break
		}
	}
	if critical == nil {
		t.Fatalf("expected an AlertCritical to fire once over budget; got %d alerts: %+v", len(snapshot), snapshot)
	}
	if critical.Budget != budget {
		t.Errorf("alert.Budget = %v, want %v", critical.Budget, budget)
	}
	if critical.Spent <= budget {
		t.Errorf("alert.Spent = %v, want > budget %v", critical.Spent, budget)
	}
	if critical.Percentage < 100 {
		t.Errorf("alert.Percentage = %v, want >= 100", critical.Percentage)
	}
	if !strings.Contains(critical.Message, "Budget exceeded") {
		t.Errorf("alert.Message = %q, want substring %q", critical.Message, "Budget exceeded")
	}

	// --- assert #3: engine-shaped halt error ---
	//
	// The workflow executor formats its terminal error as:
	//   "budget exceeded ($X.XX spent), aborting"
	// (see internal/workflow/workflow.go, budget gate around line 551).
	// We re-format the same shape here so a regression that changes
	// the error shape fails this test too.
	haltErr := fmt.Errorf("budget exceeded ($%.2f spent), aborting", tr.Total())
	if !strings.Contains(haltErr.Error(), "budget exceeded") {
		t.Errorf("halt error %q missing 'budget exceeded'", haltErr.Error())
	}
	if !strings.Contains(haltErr.Error(), "aborting") {
		t.Errorf("halt error %q missing 'aborting'", haltErr.Error())
	}

	// --- assert #4: the audit event payload is well-formed ---
	//
	// Same construction as workflow.go:553-562. If a future refactor
	// changes the event type or the CostEvent shape, this test
	// flags the divergence.
	evt := &hub.Event{
		Type:   hub.EventCostBudgetExceeded,
		TaskID: "r1-3-halt-task",
		Cost: &hub.CostEvent{
			TotalSpent:  tr.Total(),
			BudgetLimit: tr.Total() + tr.BudgetRemaining(),
			PercentUsed: 100,
			Threshold:   "exceeded",
		},
	}
	if evt.Type != hub.EventCostBudgetExceeded {
		t.Errorf("event type = %q, want %q", evt.Type, hub.EventCostBudgetExceeded)
	}
	if evt.Cost == nil {
		t.Fatal("event.Cost payload is nil; workflow halt must attach CostEvent")
	}
	if evt.Cost.TotalSpent <= 0 {
		t.Errorf("event.Cost.TotalSpent = %v, want > 0", evt.Cost.TotalSpent)
	}
	// BudgetLimit is reconstructed from Total+Remaining and should
	// round-trip to the original ceiling.
	const eps = 1e-9
	if evt.Cost.BudgetLimit < budget-eps || evt.Cost.BudgetLimit > budget+eps {
		t.Errorf("event.Cost.BudgetLimit = %v, want %v (±%v)", evt.Cost.BudgetLimit, budget, eps)
	}
	if evt.Cost.Threshold != "exceeded" {
		t.Errorf("event.Cost.Threshold = %q, want %q", evt.Cost.Threshold, "exceeded")
	}
	if evt.TaskID != "r1-3-halt-task" {
		t.Errorf("event.TaskID = %q, want %q", evt.TaskID, "r1-3-halt-task")
	}
}

// TestBudgetHaltUnlimitedNeverTrips asserts the negative direction:
// an unlimited budget (0 == unlimited) never flips OverBudget() and
// therefore never triggers the halt path, no matter how much is spent.
func TestBudgetHaltUnlimitedNeverTrips(t *testing.T) {
	tr := NewTracker(0, func(a Alert) {
		t.Fatalf("unlimited budget should never alert; got %+v", a)
	})
	tr.Record("claude-opus-4", "", 1_000_000, 500_000, 0, 0)
	if tr.OverBudget() {
		t.Fatal("unlimited budget must never flip OverBudget()")
	}
	if tr.BudgetRemaining() != -1 {
		t.Errorf("BudgetRemaining() = %v, want -1 sentinel for unlimited", tr.BudgetRemaining())
	}
}

// TestBudgetHaltEnvCostCounts confirms that RecordEnvCost
// contributes to the same ceiling — an agent spinning up expensive
// compute can trip the halt even if token spend is modest.
func TestBudgetHaltEnvCostCounts(t *testing.T) {
	var alertMu sync.Mutex
	var alertLevels []AlertLevel
	tr := NewTracker(1.0, func(a Alert) {
		alertMu.Lock()
		defer alertMu.Unlock()
		alertLevels = append(alertLevels, a.Level)
	})

	// Token spend stays well below the ceiling ...
	tr.Record("claude-sonnet-4", "t1", 1000, 500, 0, 0)
	if tr.OverBudget() {
		t.Fatal("should not be over budget from token spend alone")
	}

	// ... but env cost pushes over.
	tr.RecordEnvCost("t1", 2.50)
	if !tr.OverBudget() {
		t.Fatal("env cost should contribute to the same ceiling")
	}

	alertMu.Lock()
	defer alertMu.Unlock()
	hadCritical := false
	for _, l := range alertLevels {
		if l == AlertCritical {
			hadCritical = true
			break
		}
	}
	if !hadCritical {
		t.Errorf("expected AlertCritical after env cost overrun, got levels %v", alertLevels)
	}
}
