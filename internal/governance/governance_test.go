package governance

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/ledger"
)

// newTestGovernor constructs a Governor rooted at a temp dir and registers
// cleanup. budgetUSD is the mission budget the budget rule evaluates against.
func newTestGovernor(t *testing.T, budgetUSD float64) *Governor {
	t.Helper()
	g, err := New(context.Background(), t.TempDir(), "mission-test", budgetUSD)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = g.Close() })
	return g
}

// collect subscribes to the Governor's bus for the given type prefix and
// returns a snapshot accessor plus a cancel func.
func collect(t *testing.T, b *bus.Bus, prefix string) (func() []bus.Event, func()) {
	t.Helper()
	var mu sync.Mutex
	var got []bus.Event
	sub := b.Subscribe(bus.Pattern{TypePrefix: prefix}, func(evt bus.Event) {
		mu.Lock()
		got = append(got, evt)
		mu.Unlock()
	})
	return func() []bus.Event {
		mu.Lock()
		defer mu.Unlock()
		out := make([]bus.Event, len(got))
		copy(out, got)
		return out
	}, func() { sub.Cancel() }
}

// TestGovernorBudgetRuleFires drives a synthetic hub cost event through the
// Governor's hub handler and asserts the budget rule publishes a
// supervisor/budget action event once the 80% threshold is crossed.
func TestGovernorBudgetRuleFires(t *testing.T) {
	g := newTestGovernor(t, 1.0) // $1 budget

	// Subscribe on the governance bus for the budget rule's action events.
	// At >=80% the rule publishes "supervisor.spawn.requested"; we also
	// collect the "mission.budget.*" derived events for diagnostics.
	spawnEvents, cancelSpawn := collect(t, g.Bus(), "supervisor.")
	defer cancelSpawn()
	budgetEvents, cancelBudget := collect(t, g.Bus(), "mission.budget.")
	defer cancelBudget()

	sub := g.HubSubscriber()
	// $0.90 spend against a $1 budget = 90% -> crosses CheckPct (80%),
	// so the rule must publish supervisor.spawn.requested.
	sub.Handler(context.Background(), &hub.Event{
		Type:  hub.EventModelPostCall,
		Model: &hub.ModelEvent{Provider: "anthropic", Model: "claude", CostUSD: 0.90},
	})

	// Delivery + rule evaluation are async; poll up to ~2s.
	deadline := time.Now().Add(2 * time.Second)
	var sawSpawn bool
	for time.Now().Before(deadline) {
		for _, e := range spawnEvents() {
			if e.Type == "supervisor.spawn.requested" {
				sawSpawn = true
			}
		}
		if sawSpawn {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if !sawSpawn {
		// Surface what we did see to make failures diagnosable.
		var seen []string
		for _, e := range budgetEvents() {
			seen = append(seen, string(e.Type))
		}
		for _, e := range spawnEvents() {
			seen = append(seen, string(e.Type))
		}
		t.Fatalf("budget rule did not publish supervisor.spawn.requested within deadline; saw: %s", strings.Join(seen, ", "))
	}
}

// TestGovernorWritesLedgerTaskNode drives a synthetic hub task-started event
// through the handler and asserts a "task" ledger node is written.
func TestGovernorWritesLedgerTaskNode(t *testing.T) {
	g := newTestGovernor(t, 0)

	sub := g.HubSubscriber()
	sub.Handler(context.Background(), &hub.Event{
		Type:   hub.EventTaskStarted,
		TaskID: "task-42",
		Phase:  "execute",
	})

	nodes, err := g.Ledger().Query(context.Background(), ledger.QueryFilter{Type: "task"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(nodes) < 1 {
		t.Fatalf("expected >=1 task node, got %d", len(nodes))
	}
	if nodes[0].MissionID != "mission-test" {
		t.Errorf("task node MissionID = %q, want mission-test", nodes[0].MissionID)
	}
}

// TestGovernorCloseIsClean asserts New then Close returns nil and that a
// second Close is safe (idempotent).
func TestGovernorCloseIsClean(t *testing.T) {
	g, err := New(context.Background(), t.TempDir(), "mission-close", 0)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := g.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Second Close must be safe.
	if err := g.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestGovernorDrainLandsTerminalCostNode is the R3 regression. The Governor is
// registered as a wildcard ModeObserve subscriber on the hub bus, so its
// terminal cost write runs on the hub's fire-and-forget observe goroutine.
// Emitting the cost event via EmitAsync (exactly as workflow.emitEventAsync
// fires terminal lifecycle events) and then draining the hub bus BEFORE the
// governor closes must guarantee the cost_record ledger node has landed.
//
// Before R3 the hub had no Drain: the observe goroutine raced the deferred
// governor.Close(), and the terminal write hit a closed bus+ledger
// nondeterministically. Drain closes that race by blocking until the observe
// chain (EmitAsync goroutine -> Emit -> observe goroutine -> ledger.AddNode)
// completes.
func TestGovernorDrainLandsTerminalCostNode(t *testing.T) {
	g := newTestGovernor(t, 0)

	h := hub.New()
	h.Register(g.HubSubscriber())

	// Terminal cost event, fired fire-and-forget like the real workflow.
	h.EmitAsync(&hub.Event{
		Type:   hub.EventModelPostCall,
		TaskID: "task-terminal",
		Model: &hub.ModelEvent{
			Provider:     "anthropic",
			Model:        "claude",
			CostUSD:      0.25,
			InputTokens:  10,
			OutputTokens: 5,
		},
	})

	// Drain must block until the observe goroutine ran the Governor handler
	// -> costBridge.PublishUsage -> ledger.AddNode.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.Drain(ctx); err != nil {
		t.Fatalf("Drain: %v", err)
	}

	// The terminal cost_record node must exist BEFORE any Close — proving the
	// write landed rather than racing teardown.
	nodes, err := g.Ledger().Query(context.Background(), ledger.QueryFilter{Type: "cost_record"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(nodes) < 1 {
		t.Fatalf("expected >=1 cost_record node after Drain, got %d", len(nodes))
	}
	if nodes[0].MissionID != "mission-test" {
		t.Errorf("cost_record node MissionID = %q, want mission-test", nodes[0].MissionID)
	}
}

// TestGovernorBudgetWarningBelowSpawn checks that a sub-80% (but >=50%)
// spend produces a warning rather than a spawn — proving the threshold
// ladder is wired, not just "any event fires".
func TestGovernorBudgetWarningBelowSpawn(t *testing.T) {
	g := newTestGovernor(t, 1.0)

	warnEvents, cancelWarn := collect(t, g.Bus(), "mission.budget.warning")
	defer cancelWarn()
	spawnEvents, cancelSpawn := collect(t, g.Bus(), "supervisor.spawn.requested")
	defer cancelSpawn()

	sub := g.HubSubscriber()
	// $0.60 of $1 = 60% -> warning, not spawn.
	sub.Handler(context.Background(), &hub.Event{
		Type:  hub.EventModelPostCall,
		Model: &hub.ModelEvent{CostUSD: 0.60},
	})

	deadline := time.Now().Add(2 * time.Second)
	var sawWarn bool
	for time.Now().Before(deadline) {
		if len(warnEvents()) > 0 {
			sawWarn = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !sawWarn {
		t.Fatalf("expected a mission.budget.warning at 60%% spend")
	}
	if n := len(spawnEvents()); n != 0 {
		t.Fatalf("did not expect supervisor.spawn.requested at 60%% spend, got %d", n)
	}
}
