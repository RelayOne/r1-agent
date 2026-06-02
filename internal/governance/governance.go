// Package governance wires the V2 governance layer (durable bus + ledger +
// deterministic supervisor) into the live v1 runtime.
//
// The v1 runtime drives its mission lifecycle through internal/hub (the
// typed, in-process event hub with struct payloads). The V2 governance
// stack (internal/bus + internal/ledger + internal/supervisor) is a
// separate, durable, JSON-payload world: the supervisor self-subscribes
// to the v2 bus and reacts to events via its deterministic rules engine.
//
// Governor is the bridge. It owns a private v2 bus, ledger, and mission
// supervisor, and exposes a hub.Subscriber that observes v1 hub events and
// translates the ones the supervisor cares about into v2 bus events /
// ledger nodes:
//
//   - model.post_call cost events -> accumulate spend and publish a
//     "mission.budget.update" v2 event so drift.NewBudgetThreshold fires
//     (warning / spawn / escalation / hard-stop as thresholds are crossed).
//   - task.started / task.completed -> append a "task" ledger node so the
//     governance graph is actually populated during a run.
//
// The Governor is observe-only on the hub side: its handler never blocks
// or vetoes v1 execution. It is constructed only when governance is
// explicitly enabled (default OFF), so it has zero impact otherwise.
package governance

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/ledger"
	"github.com/RelayOne/r1/internal/supervisor"
	"github.com/RelayOne/r1/internal/supervisor/manifests"
)

// Governor bridges the v1 hub to the V2 governance stack. It owns a durable
// bus, a ledger, and a mission supervisor running the mission rule manifest.
type Governor struct {
	missionID string
	budgetUSD float64

	bus    *bus.Bus
	ledger *ledger.Ledger
	sup    *supervisor.Supervisor

	mu        sync.Mutex
	spentUSD  float64 // cumulative spend accumulated from hub cost events
	closeOnce sync.Once
}

// New constructs a Governor rooted at stateDir for the given mission. It
// opens a durable bus under <stateDir>/governance/bus, a ledger under
// <stateDir>/governance/ledger, then starts a mission supervisor loaded
// with manifests.MissionRules() and subscribed to the bus.
//
// budgetUSD is the mission cost budget used to derive budget-update
// percentages from accumulated hub cost events. budgetUSD <= 0 disables
// budget-rule emission (the budget rule treats a non-positive budget as a
// no-op anyway).
//
// On any construction error, already-opened resources are cleaned up and
// the error is returned.
func New(ctx context.Context, stateDir, missionID string, budgetUSD float64) (*Governor, error) {
	if missionID == "" {
		return nil, fmt.Errorf("governance: missionID is required")
	}

	b, err := bus.New(filepath.Join(stateDir, "governance", "bus"))
	if err != nil {
		return nil, fmt.Errorf("governance: open bus: %w", err)
	}

	l, err := ledger.New(filepath.Join(stateDir, "governance", "ledger"))
	if err != nil {
		b.Close()
		return nil, fmt.Errorf("governance: open ledger: %w", err)
	}

	sup := supervisor.New(supervisor.Config{
		ID:    "governance.mission." + missionID,
		Type:  supervisor.TypeMission,
		Scope: bus.Scope{MissionID: missionID},
	}, b, l)
	sup.RegisterRules(manifests.MissionRules()...)

	if err := sup.Start(ctx); err != nil {
		l.Close()
		b.Close()
		return nil, fmt.Errorf("governance: start supervisor: %w", err)
	}

	return &Governor{
		missionID: missionID,
		budgetUSD: budgetUSD,
		bus:       b,
		ledger:    l,
		sup:       sup,
	}, nil
}

// Bus returns the Governor's durable v2 bus (for tests / introspection).
func (g *Governor) Bus() *bus.Bus { return g.bus }

// Ledger returns the Governor's v2 ledger (for tests / introspection).
func (g *Governor) Ledger() *ledger.Ledger { return g.ledger }

// HubSubscriber returns an observe-mode hub.Subscriber that translates v1
// hub events into V2 governance events. The handler never blocks v1
// execution and always returns nil (observe mode is fire-and-forget).
func (g *Governor) HubSubscriber() hub.Subscriber {
	return hub.Subscriber{
		ID:      "governance.bridge." + g.missionID,
		Events:  []hub.EventType{"*"},
		Mode:    hub.ModeObserve,
		Handler: g.handle,
	}
}

// handle is the hub event translator. It is observe-mode, so it must not
// block on slow I/O for long and always returns nil.
func (g *Governor) handle(ctx context.Context, ev *hub.Event) *hub.HookResponse {
	if ev == nil {
		return nil
	}
	switch ev.Type {
	case hub.EventModelPostCall:
		g.onCost(ev)
	case hub.EventTaskStarted:
		g.onTask(ctx, ev, "started")
	case hub.EventTaskCompleted:
		g.onTask(ctx, ev, "completed")
	}
	return nil
}

// onCost accumulates the cost from a model.post_call event and publishes a
// "mission.budget.update" v2 event carrying cumulative spend against the
// configured budget. The budget rule (drift.NewBudgetThreshold) then fires
// the appropriate action as thresholds are crossed.
func (g *Governor) onCost(ev *hub.Event) {
	if ev.Model == nil || ev.Model.CostUSD <= 0 {
		return
	}

	g.mu.Lock()
	g.spentUSD += ev.Model.CostUSD
	spent := g.spentUSD
	g.mu.Unlock()

	payload, err := json.Marshal(map[string]float64{
		"spent_usd":  spent,
		"budget_usd": g.budgetUSD,
	})
	if err != nil {
		return
	}

	// Publish without a CausalRef: this is a root cost-update event. The
	// budget rule's own emitted events use this event's auto-assigned ID
	// as their CausalRef.
	_ = g.bus.Publish(bus.Event{
		Type:      "mission.budget.update",
		EmitterID: "governance.bridge",
		Scope:     bus.Scope{MissionID: g.missionID},
		Payload:   payload,
	})
}

// taskNodeContent is the minimal task node body written to the ledger.
type taskNodeContent struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	State       string `json:"state"`
}

// onTask appends a "task" ledger node recording a task lifecycle
// transition, populating the governance graph during a run.
func (g *Governor) onTask(ctx context.Context, ev *hub.Event, state string) {
	title := ev.TaskID
	if title == "" {
		title = "task"
	}
	content, err := json.Marshal(taskNodeContent{
		Title:       title,
		Description: ev.Phase,
		State:       state,
	})
	if err != nil {
		return
	}
	_, _ = g.ledger.AddNode(ctx, ledger.Node{
		Type:          "task",
		SchemaVersion: 1,
		CreatedBy:     "governance.bridge",
		MissionID:     g.missionID,
		Content:       content,
	})
}

// Close stops the supervisor and releases the ledger and bus. Safe to call
// multiple times; only the first call has effect.
func (g *Governor) Close() error {
	var err error
	g.closeOnce.Do(func() {
		if g.sup != nil {
			if serr := g.sup.Stop(); serr != nil && err == nil {
				err = serr
			}
		}
		if g.ledger != nil {
			if lerr := g.ledger.Close(); lerr != nil && err == nil {
				err = lerr
			}
		}
		if g.bus != nil {
			if berr := g.bus.Close(); berr != nil && err == nil {
				err = berr
			}
		}
	})
	return err
}
