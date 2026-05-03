// Package planupdate implements the PlanUpdateLobe — a Haiku-driven Lobe
// that watches the conversation transcript and proposes minimal updates
// to plan.json. Edits to existing items auto-apply; additions and
// removals are queued as user-confirm Notes that the main thread
// surfaces to the user at idle.
//
// Spec: specs/cortex-concerns.md items 16–20 ("PlanUpdateLobe").
//
// API adaptation note (recorded in the per-task commit message):
//
// The spec proposes the constructor signature
//
//	func NewPlanUpdateLobe(planPath string, runtime *conversation.Runtime,
//	    client apiclient.Client, escalate Escalator) *PlanUpdateLobe
//
// but in r1 the wire client is internal/provider.Provider — there is no
// apiclient.Client type. The Lobe also needs a writable *cortex.Workspace
// (LobeInput.Workspace is the read-only adapter — Lobes that publish must
// capture the write handle at construction time, mirroring the
// MemoryRecallLobe pattern). The durable bus.Bus is needed for the
// confirmation-event subscriber in TASK-20.
//
// The actual constructor used in production is therefore:
//
//	NewPlanUpdateLobe(planPath, runtime, client, escalate, ws, durable)
//
// The "watch conversation; propose plan deltas; auto-apply edits;
// queue adds+removes for user confirmation" contract from the spec is
// preserved verbatim.
//
// cortex.ActionVerbs is referenced in the spec but does NOT exist as a
// helper in cortex-core. The verb-scan logic lives in this package as a
// local actionVerbs slice — see verbs.go.
package planupdate

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/conversation"
	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/cortex/lobes/llm"
	"github.com/RelayOne/r1/internal/provider"
)

// PlanUpdateLobe is the cortex.Lobe implementation declared in spec
// items 16–20. It is KindLLM — every Run that triggers makes one Haiku
// call gated by the shared LLM-Lobe semaphore.
type PlanUpdateLobe struct {
	planPath string
	runtime  *conversation.Runtime
	client   provider.Provider
	escalate llm.Escalator

	// ws is the writable Workspace handle. Captured at construction
	// because LobeInput.Workspace is read-only (the cortex.Lobe contract
	// freezes Notes-as-the-only-write-channel; production callers always
	// pass the cortex's own Workspace through here).
	ws *cortex.Workspace

	// durable is the WAL-backed bus.Bus the Lobe subscribes to for
	// "cortex.user.confirmed_plan_change" events (TASK-20). May be nil
	// in tests that exercise only Run/triggers without confirmation.
	durable *bus.Bus

	// turnCount counts the number of triggered Run() calls. It drives
	// the every-3rd-tick cadence in TASK-17. Stored atomically so tests
	// can inspect it without taking a lock.
	turnCount atomic.Uint64

	// queuedMu guards queued. Maps queue_id -> the proposed adds/removes
	// awaiting user confirmation. TASK-19 populates the map; TASK-20's
	// confirmation handler pops a key and applies it via plan.Save.
	queuedMu sync.Mutex
	queued   map[string]planChange
}

// planChange is the payload queued for user confirmation: the additions
// to write and the removals to apply when the user confirms. The
// concrete proposedAddition / proposedRemoval shapes are defined in
// parse.go (landed in TASK-19) so this struct's fields use anonymous
// any slices in the scaffold and are populated with typed values once
// the parse layer exists.
type planChange struct {
	additions []any
	removals  []any
}

// NewPlanUpdateLobe constructs the Lobe. See the package doc for the
// API-adaptation note documenting why the signature differs from the
// literal spec snippet.
//
// Arguments:
//
//   - planPath: filesystem path that backs plan.Load / plan.Save. May
//     point to a non-existent file on first call; the Lobe writes the
//     plan as JSON when edits/confirmations arrive.
//   - runtime: the multi-turn conversation runtime. The Lobe reads
//     Messages() to compose the user-message context for the Haiku call.
//   - client: the model wire client. The Lobe calls ChatStream during
//     trigger fires.
//   - escalate: the Haiku→Sonnet escalation policy from TASK-2. Called
//     when Haiku output cannot be parsed; an empty return string keeps
//     the Lobe on Haiku.
//   - ws: writable cortex.Workspace handle for Publish. May be nil in
//     tests that drive Run without observing Notes.
//   - durable: durable bus.Bus for the confirmation-event subscriber.
//     May be nil; the Lobe simply skips subscription registration.
func NewPlanUpdateLobe(
	planPath string,
	runtime *conversation.Runtime,
	client provider.Provider,
	escalate llm.Escalator,
	ws *cortex.Workspace,
	durable *bus.Bus,
) *PlanUpdateLobe {
	return &PlanUpdateLobe{
		planPath: planPath,
		runtime:  runtime,
		client:   client,
		escalate: escalate,
		ws:       ws,
		durable:  durable,
		queued:   make(map[string]planChange),
	}
}

// ID satisfies cortex.Lobe. Stable string used as Note.LobeID.
func (l *PlanUpdateLobe) ID() string { return "plan-update" }

// Description satisfies cortex.Lobe. Used by /status output.
func (l *PlanUpdateLobe) Description() string {
	return "keeps plan.json synchronized with the conversation"
}

// Kind satisfies cortex.Lobe. KindLLM — every triggered Run makes one
// Haiku call gated by the shared LLM-Lobe semaphore.
func (l *PlanUpdateLobe) Kind() cortex.LobeKind { return cortex.KindLLM }

// Run is the per-Round entry point. TASK-16 lands the scaffold with a
// stub Run that increments the turn counter and returns nil; trigger,
// Haiku call, JSON parsing, and confirmation handling land in TASKs
// 17–20 in subsequent commits.
func (l *PlanUpdateLobe) Run(ctx context.Context, in cortex.LobeInput) error {
	_ = ctx
	_ = in
	l.turnCount.Add(1)
	return nil
}

// PlanPath returns the configured plan.json path. Test-facing accessor
// so tests can assert constructor parameter capture without reaching
// into unexported fields.
func (l *PlanUpdateLobe) PlanPath() string { return l.planPath }

// TurnCount reports the current trigger counter. Test-facing.
func (l *PlanUpdateLobe) TurnCount() uint64 { return l.turnCount.Load() }
