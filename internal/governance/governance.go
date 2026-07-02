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
	"github.com/RelayOne/r1/internal/ledger/loops"
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

	// Persist consensus loop state transitions (audit A066): the
	// consensus rules announce transitions as
	// consensus.loop.state.changed bus events but cannot write the
	// ledger from their Action hook; the loops tracker subscription is
	// the component that applies them. The subscription dies with the
	// bus on Close.
	loops.NewTracker(l).SubscribeStateChanges(b)

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

// MissionID returns the mission identifier this Governor is scoped to.
// Used by the app orchestrator when publishing bridge events (audit A037).
func (g *Governor) MissionID() string { return g.missionID }

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
		g.onDeclaration(ev)
	case hub.EventTaskFailed:
		g.onTask(ctx, ev, "failed")
	case hub.EventVerifyCrossModelReview:
		g.onReview(ctx, ev)
	case hub.EventVerifyBuildResult:
		g.onVerify(ctx, ev)
	case hub.EventMissionPlanStart:
		g.onTask(ctx, ev, "started")
		g.publishLifecycle("plan.started", ev.TaskID)
	case hub.EventMissionPlanDone:
		g.onTask(ctx, ev, "planned")
		g.publishLifecycle("plan.done", ev.TaskID)
	case hub.EventMissionExecuteStart:
		g.onTask(ctx, ev, "executing")
		g.publishLifecycle("execute.started", ev.TaskID)
	case hub.EventCostBudgetExceeded:
		g.onBudgetExceeded()
	case hub.EventGitPreCommit:
		g.publishLifecycle("commit.started", ev.TaskID)
	case hub.EventGitPreMerge:
		g.onDecision(ctx, ev, "merge_start")
		g.publishLifecycle("merge.started", ev.TaskID)
	case hub.EventGitPostMerge:
		g.onDecision(ctx, ev, "merge_complete")
		g.publishLifecycle("merge.done", ev.TaskID)
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

	// budgetUSD <= 0 disables budget-rule emission (see New's doc contract:
	// the budget rule treats a non-positive budget as a no-op). Short-circuit
	// before publishing so no mission.budget.update is emitted on default-on
	// runs with no configured governance budget.
	if g.budgetUSD <= 0 {
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

// onReview writes the cross-model review verdict to the ledger so the trust
// rules can find a recorded second opinion.
//
// NAMING SPLIT (resolved via option A — zero supervisor-rule edits):
// the registered node struct is Type "agree"/"dissent"
// (internal/ledger/nodes/agree_dissent.go:23/:70) and the loops tracker
// counts those struct Types — BUT the trust + partner-timeout rules query
// the LITERAL ledger Types "review.agree"/"review.dissent"
// (trust/completion_requires_second_opinion.go:69,
// trust/fix_requires_second_opinion.go:53, consensus/partner_timeout.go:72-73).
// So onReview writes the literal queried strings directly. nodes.Agree is
// intentionally NOT used because it requires a real DraftRef that the
// per-task v1 workflow has no concept of. AddNode skips Validate(), so a raw
// node with the literal Type is the contract.
//
// CreatedBy MUST be "governance.reviewer" (distinct from the declaration's
// EmitterID "governance.worker") — otherwise the trust rule's
// n.CreatedBy == evt.EmitterID skip (trust.go:72) would drop the node and an
// approved task would be (wrongly) flagged as un-reviewed. The Content shape
// {"task_id":...} matches the trust rule's agreeContent (trust.go:48-49).
func (g *Governor) onReview(ctx context.Context, ev *hub.Event) {
	if ev.Lifecycle == nil {
		return
	}
	var nodeType string
	switch ev.Lifecycle.State {
	case "agree":
		nodeType = "review.agree"
	case "dissent":
		nodeType = "review.dissent"
	default:
		return
	}
	content, err := json.Marshal(map[string]string{"task_id": ev.TaskID})
	if err != nil {
		return
	}
	_, _ = g.ledger.AddNode(ctx, ledger.Node{
		Type:          nodeType,
		SchemaVersion: 1,
		CreatedBy:     "governance.reviewer",
		MissionID:     g.missionID,
		Content:       content,
	})
}

// onDeclaration publishes a "worker.declaration.done" v2 event after a task
// completes, triggering the trust second-opinion rule. The review.agree node
// (written by onReview when the cross-model review event arrives, which
// precedes task completion in the workflow) satisfies the rule on approved
// work so it does not fire. EmitterID "governance.worker" MUST differ from
// the review node's CreatedBy "governance.reviewer" (trust.go:72).
func (g *Governor) onDeclaration(ev *hub.Event) {
	payload, err := json.Marshal(map[string]string{
		"task_id":     ev.TaskID,
		"worker_id":   "governance.worker",
		"artifact_id": ev.TaskID,
	})
	if err != nil {
		return
	}
	_ = g.bus.Publish(bus.Event{
		Type:      bus.EvtWorkerDeclarationDone,
		EmitterID: "governance.worker",
		Scope:     bus.Scope{MissionID: g.missionID, TaskID: ev.TaskID},
		Payload:   payload,
	})
}

// onVerify writes one "verification_evidence" ledger node per build/test/lint
// outcome. The workflow emits EventVerifyBuildResult once per outcome (the
// outcome name is carried in ev.Test.Phase), so this writes a single node per
// call. ProducerModel != VerifierModel is kept distinct for forward-compat
// even though AddNode skips Validate() (which would enforce that invariant).
func (g *Governor) onVerify(ctx context.Context, ev *hub.Event) {
	if ev.Test == nil {
		return
	}
	verdict := "disagree"
	if ev.Test.Passed > 0 && ev.Test.Failed == 0 {
		verdict = "agree"
	}
	content, err := json.Marshal(map[string]string{
		"subject_ref":    ev.TaskID,
		"subject_kind":   "task",
		"producer_model": "executor",
		"verifier_model": "governance.verify",
		"verdict":        verdict,
		"notes":          ev.Test.Phase,
	})
	if err != nil {
		return
	}
	_, _ = g.ledger.AddNode(ctx, ledger.Node{
		Type:          "verification_evidence",
		SchemaVersion: 1,
		CreatedBy:     "governance.bridge",
		MissionID:     g.missionID,
		Content:       content,
	})
}

// onDecision writes a raw "decision" ledger node recording a merge intent or
// merge completion.
func (g *Governor) onDecision(ctx context.Context, ev *hub.Event, state string) {
	content, err := json.Marshal(taskNodeContent{
		Title:       ev.TaskID,
		Description: ev.Phase,
		State:       state,
	})
	if err != nil {
		return
	}
	_, _ = g.ledger.AddNode(ctx, ledger.Node{
		Type:          "decision",
		SchemaVersion: 1,
		CreatedBy:     "governance.bridge",
		MissionID:     g.missionID,
		Content:       content,
	})
}

// publishLifecycle publishes a fire-and-forget v2 lifecycle bus event of the
// given Type scoped to the mission and task.
func (g *Governor) publishLifecycle(eventType, taskID string) {
	_ = g.bus.Publish(bus.Event{
		Type:      bus.EventType(eventType),
		EmitterID: "governance.bridge",
		Scope:     bus.Scope{MissionID: g.missionID, TaskID: taskID},
	})
}

// onBudgetExceeded forces a hard-stop "mission.budget.update" event with
// spent_usd == budget_usd (100% used) when the v1 runtime reports the budget
// has been exceeded, so the budget rule escalates.
func (g *Governor) onBudgetExceeded() {
	// budgetUSD <= 0 disables budget-rule emission entirely (mirrors the
	// New doc contract and the budget rule's non-positive-budget no-op).
	// Without this guard a default-on run with no configured budget would
	// synthesize a spurious spent_usd=0,budget_usd=0 update that can trip
	// the budget rule. Short-circuit before publishing.
	if g.budgetUSD <= 0 {
		return
	}

	payload, err := json.Marshal(map[string]float64{
		"spent_usd":  g.budgetUSD,
		"budget_usd": g.budgetUSD,
	})
	if err != nil {
		return
	}
	_ = g.bus.Publish(bus.Event{
		Type:      "mission.budget.update",
		EmitterID: "governance.bridge",
		Scope:     bus.Scope{MissionID: g.missionID},
		Payload:   payload,
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
