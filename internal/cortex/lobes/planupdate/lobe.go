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
// MemoryRecallLobe pattern). The in-process hub.Bus is needed for the
// confirmation-event subscriber in TASK-20.
//
// The actual constructor used in production is therefore:
//
//	NewPlanUpdateLobe(planPath, runtime, client, escalate, ws, hubBus)
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
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/RelayOne/r1/internal/conversation"
	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/cortex/lobes/llm"
	"github.com/RelayOne/r1/internal/hub"
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

	// hubBus is the in-process hub.Bus the Lobe subscribes to for
	// EventCortexUserConfirmedPlanChange events (TASK-20). May be nil
	// in tests that exercise only Run/triggers without confirmation.
	hubBus *hub.Bus

	// turnCount counts the number of Run() ticks observed. It drives
	// the every-3rd-tick cadence in TASK-17. Stored atomically so tests
	// can inspect it without taking a lock. NOTE: turnCount is bumped
	// on EVERY Run, not just triggered ones — that is what makes the
	// "every 3rd boundary" cadence well-defined.
	turnCount atomic.Uint64

	// triggerCount counts the number of times the trigger predicate
	// fired (i.e. the number of times the Lobe would have called
	// Haiku). Test-facing accessor exposes it via TriggerCount; the
	// production code path increments it just before invoking
	// onTrigger.
	triggerCount atomic.Uint64

	// onTrigger is the per-Run callback invoked when the trigger
	// predicate evaluates true. TASK-18 lands the default (the actual
	// Haiku call); TASK-17 leaves it nil so triggered ticks are
	// observable via triggerCount without the LLM round trip. Tests
	// can override via SetOnTrigger to assert call cadence.
	onTrigger func(ctx context.Context, in cortex.LobeInput)

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
//   - hubBus: in-process hub.Bus for the confirmation-event subscriber.
//     May be nil; the Lobe simply skips subscription registration.
func NewPlanUpdateLobe(
	planPath string,
	runtime *conversation.Runtime,
	client provider.Provider,
	escalate llm.Escalator,
	ws *cortex.Workspace,
	hubBus *hub.Bus,
) *PlanUpdateLobe {
	l := &PlanUpdateLobe{
		planPath: planPath,
		runtime:  runtime,
		client:   client,
		escalate: escalate,
		ws:       ws,
		hubBus:   hubBus,
		queued:   make(map[string]planChange),
	}
	// Default onTrigger: invoke haikuCall, parse output, auto-apply
	// edits, queue adds/removes for user-confirm. Tests SetOnTrigger
	// to a counting hook to assert TASK-17 cadence in isolation.
	l.onTrigger = l.defaultOnTrigger
	// TASK-20: subscribe to the user-confirmed event so queued
	// proposals get applied when the user confirms. Safe with a nil
	// bus (skipped).
	l.registerConfirmSubscriber()
	return l
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

// Run is the per-Round entry point. TASK-17 lands the trigger logic:
// every 3rd tick fires the Haiku path, OR any tick whose last user
// message contains an action-verb. The actual Haiku call is wired in
// TASK-18 as the default onTrigger; TASK-17 simply increments
// triggerCount and invokes onTrigger when set, so the cadence test can
// observe the trigger predicate independently of the LLM round trip.
//
// ctx.Done is observed defensively — a cancelled tick drops out
// without firing the trigger so the LobeRunner contract holds.
func (l *PlanUpdateLobe) Run(ctx context.Context, in cortex.LobeInput) error {
	if err := ctx.Err(); err != nil {
		return nil
	}

	turn := l.turnCount.Add(1)
	if !l.shouldTrigger(turn, in) {
		return nil
	}

	l.triggerCount.Add(1)
	if l.onTrigger != nil {
		l.onTrigger(ctx, in)
	}
	return nil
}

// shouldTrigger evaluates the spec-item-17 trigger predicate:
//
//	turn % 3 == 0  OR  lastUserText(in.History) contains an actionVerb
//
// The first clause expresses "every 3rd assistant turn boundary":
// LobeRunner.Tick fires once per Round, and a Round corresponds to one
// assistant turn boundary in the cortex.MidturnNote pipeline (TASK-14
// of cortex-core). Counting from 1, turns 3/6/9/... satisfy turn%3==0.
//
// The second clause uses the verb-scan helper in verbs.go. Because the
// verb-scan looks at ONLY the last user message, ticks that arrive
// without a fresh user turn (e.g. tool-result rounds) never re-fire on
// the same prior message — by the time a verb-bearing user turn shows
// up, the message has already been observed by the previous tick's
// turn%3 cadence or one of its own (the test asserts both paths).
func (l *PlanUpdateLobe) shouldTrigger(turn uint64, in cortex.LobeInput) bool {
	if turn > 0 && turn%3 == 0 {
		return true
	}
	if scanVerbs(lastUserText(in.History), actionVerbs) {
		return true
	}
	return false
}

// SetOnTrigger overrides the per-trigger callback. Production code calls
// this from the constructor (TASK-18) to install the Haiku-call closure;
// tests call it to inject a counting hook so they can assert TASK-17's
// trigger cadence without a real provider.
func (l *PlanUpdateLobe) SetOnTrigger(fn func(ctx context.Context, in cortex.LobeInput)) {
	l.onTrigger = fn
}

// TriggerCount reports the number of ticks that satisfied the trigger
// predicate. Test-facing accessor for the cadence test.
func (l *PlanUpdateLobe) TriggerCount() uint64 { return l.triggerCount.Load() }

// defaultOnTrigger is the production trigger callback installed by
// the constructor. It runs the spec-item-19 pipeline:
//
//  1. Acquire one LLM-Lobe slot via in.Provider/Bus side effects (the
//     LobeRunner already wraps Run in semaphore Acquire/Release for
//     KindLLM Lobes, so this method does NOT take the slot itself).
//  2. Call haikuCall to get the raw model output text.
//  3. parsePlanUpdate -> on error log + return (no Note, no plan
//     change).
//  4. confidence < 0.6 -> return (the model self-suppressed).
//  5. applyEditsAndSave for any edits (auto-apply).
//  6. queuePendingNote for additions+removals (user-confirm).
//
// All errors are logged at warn level; none escalate to crash the
// runner — the LobeRunner contract is "Run MUST observe ctx.Done() and
// return nil on graceful shutdown".
func (l *PlanUpdateLobe) defaultOnTrigger(ctx context.Context, in cortex.LobeInput) {
	raw, err := l.haikuCall(ctx, in)
	if err != nil {
		// haikuCall already logged; just bail.
		return
	}
	parsed, err := parsePlanUpdate(raw)
	if err != nil {
		slog.Warn("plan-update: malformed model output",
			"err", err, "lobe", l.ID(), "raw_len", len(raw))
		return
	}
	if parsed.Confidence < 0.6 {
		// Model self-suppressed per the system-prompt rule
		// "If confidence < 0.6, return empty arrays".
		return
	}

	if applied, err := applyEditsAndSave(l.planPath, parsed.Edits); err != nil {
		slog.Warn("plan-update: apply edits failed", "err", err, "lobe", l.ID())
	} else if applied > 0 {
		slog.Debug("plan-update: applied edits", "count", applied, "lobe", l.ID())
	}

	if len(parsed.Additions) > 0 || len(parsed.Removals) > 0 {
		l.queuePendingNote(parsed.Additions, parsed.Removals, parsed.Rationale)
	}
}

// PlanPath returns the configured plan.json path. Test-facing accessor
// so tests can assert constructor parameter capture without reaching
// into unexported fields.
func (l *PlanUpdateLobe) PlanPath() string { return l.planPath }

// TurnCount reports the current trigger counter. Test-facing.
func (l *PlanUpdateLobe) TurnCount() uint64 { return l.turnCount.Load() }
