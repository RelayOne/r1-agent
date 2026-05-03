// Package clarifyq implements the ClarifyingQLobe — a Haiku-driven Lobe
// that watches the user's most recent message and, when it detects
// actionable ambiguity, queues up to 3 clarifying-question Notes for
// the main thread to surface at idle.
//
// Spec: specs/cortex-concerns.md items 21–25 ("ClarifyingQLobe").
//
// API adaptation note (recorded in the per-task commit message):
//
// The spec proposes the constructor signature
//
//	func NewClarifyingQLobe(runtime *conversation.Runtime,
//	    client apiclient.Client, escalate Escalator) *ClarifyingQLobe
//
// but in r1 the wire client is internal/provider.Provider — there is no
// apiclient.Client type. The Lobe also needs a writable *cortex.Workspace
// (LobeInput.Workspace is the read-only adapter — Lobes that publish
// must capture the write handle at construction time, mirroring the
// PlanUpdateLobe pattern). The in-process hub.Bus is needed for the
// turn-after-user trigger (cortex.user.message) and the resolve hook
// (cortex.user.answered_question).
//
// The actual constructor used in production is therefore:
//
//	NewClarifyingQLobe(client, escalate, ws, hubBus)
//
// The "watch the latest user message; propose ≤3 clarifying questions
// via tool calls; cap outstanding at 3; resolve on user answer"
// contract from the spec is preserved verbatim.
package clarifyq

import (
	"context"
	"sync"

	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/cortex/lobes/llm"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/provider"
)

// ClarifyingQLobe is the cortex.Lobe implementation declared in spec
// items 21–25. It is KindLLM — every triggered turn-after-user fires
// one Haiku call gated by the shared LLM-Lobe semaphore.
type ClarifyingQLobe struct {
	client   provider.Provider
	escalate llm.Escalator

	// ws is the writable Workspace handle. Captured at construction
	// because LobeInput.Workspace is read-only (the cortex.Lobe contract
	// freezes Notes-as-the-only-write-channel; production callers always
	// pass the cortex's own Workspace through here).
	ws *cortex.Workspace

	// hubBus is the in-process hub.Bus the Lobe subscribes to for
	// EventCortexUserMessage (TASK-24) and EventCortexUserAnsweredQuestion
	// (TASK-25). May be nil in tests that exercise only Run/constructor
	// shape.
	hubBus *hub.Bus

	// mu guards outstanding. Maps question_id -> note_id so the resolve
	// path can publish a follow-on Note with Resolves=note_id when the
	// user answers. The map's size also drives the cap-at-3 check in
	// haikuOnce: before publishing a tool-use's Note we count
	// len(outstanding) and silently drop overflow.
	mu          sync.Mutex
	outstanding map[string]string

	// subscribed guards subscribe(): tests may invoke Run multiple times
	// across the same Lobe, but registering twice on the bus would
	// produce duplicate handlers (hub.Bus dedups by ID, but we still
	// guard the call site for clarity).
	subscribed bool
}

// NewClarifyingQLobe constructs the Lobe. See the package doc for the
// API-adaptation note documenting why the signature differs from the
// literal spec snippet.
//
// Arguments:
//
//   - client: the model wire client. The Lobe calls ChatStream during
//     the once-per-user-turn trigger.
//   - escalate: the Haiku→Sonnet escalation policy from TASK-2. The
//     ClarifyingQLobe does not currently escalate — this argument is
//     captured for parity with sibling Lobes and to satisfy the
//     forward-compatibility hook in spec §Escalation hook.
//   - ws: writable cortex.Workspace handle for Publish. May be nil in
//     tests that exercise only construction or schema shape.
//   - hubBus: in-process hub.Bus for the user-message + answered-question
//     subscribers. May be nil; the Lobe simply skips subscription
//     registration and the trigger never fires.
func NewClarifyingQLobe(
	client provider.Provider,
	escalate llm.Escalator,
	ws *cortex.Workspace,
	hubBus *hub.Bus,
) *ClarifyingQLobe {
	return &ClarifyingQLobe{
		client:      client,
		escalate:    escalate,
		ws:          ws,
		hubBus:      hubBus,
		outstanding: make(map[string]string),
	}
}

// ID satisfies cortex.Lobe. Stable string used as Note.LobeID. The spec
// names this Lobe "ClarifyingQLobe" but uses kebab-case for LobeIDs in
// the cortex (matching plan-update / memory-recall / rule-check).
func (l *ClarifyingQLobe) ID() string { return "clarifying-q" }

// Description satisfies cortex.Lobe. Used by /status output.
func (l *ClarifyingQLobe) Description() string {
	return "drafts clarifying questions when the user request is ambiguous"
}

// Kind satisfies cortex.Lobe. KindLLM — every triggered Run makes one
// Haiku call gated by the shared LLM-Lobe semaphore.
func (l *ClarifyingQLobe) Kind() cortex.LobeKind { return cortex.KindLLM }

// Run is the per-Round entry point. The ClarifyingQLobe's main work
// path is event-driven (the cortex.user.message subscriber installed in
// TASK-24); Run itself only ensures the subscribers are registered the
// first time the runner ticks. Subsequent Runs are no-ops.
//
// ctx.Done is observed defensively — a cancelled tick returns nil so
// the LobeRunner contract holds.
func (l *ClarifyingQLobe) Run(ctx context.Context, in cortex.LobeInput) error {
	if err := ctx.Err(); err != nil {
		return nil
	}
	l.ensureSubscribed()
	return nil
}

// ensureSubscribed registers the hub subscribers exactly once across
// the lifetime of the Lobe. Safe with a nil hubBus (skipped). The
// production path calls this from Run; the constructor leaves
// subscription registration deferred so tests that never call Run can
// inspect the Lobe without bus side effects.
func (l *ClarifyingQLobe) ensureSubscribed() {
	l.mu.Lock()
	if l.subscribed || l.hubBus == nil {
		l.mu.Unlock()
		return
	}
	l.subscribed = true
	l.mu.Unlock()
	l.subscribe()
}

// subscribe is the no-op stub for TASK-21. TASK-24 and TASK-25 register
// the user-message and answered-question subscribers here. Defining
// the method now keeps the Run scaffolding stable across the per-task
// commits.
func (l *ClarifyingQLobe) subscribe() {
	// TASK-24 / TASK-25 land here.
}

// OutstandingCount reports the number of unresolved clarifying-question
// Notes the Lobe is currently tracking. Test-facing accessor; production
// callers do not need this. Used by TASK-24's cap-at-3 test and by
// TASK-25's resolve test.
func (l *ClarifyingQLobe) OutstandingCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.outstanding)
}
