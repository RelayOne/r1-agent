// Package memorycurator implements the MemoryCuratorLobe — a Haiku-driven
// Lobe that scans the recent conversation tail and proposes durable
// project-fact entries to write into internal/memory. Auto-applied
// categories are persisted; everything else is queued as a confirm-Note
// for user approval. Every auto-write is appended to a JSONL audit log
// at ~/.r1/cortex/curator-audit.jsonl.
//
// Spec: specs/cortex-concerns.md items 26–31 ("MemoryCuratorLobe").
//
// API adaptation note (recorded in the per-task commit message):
//
// The spec proposes the constructor signature
//
//	func NewMemoryCuratorLobe(mem *memory.Store, wis *wisdom.Store,
//	    client apiclient.Client, escalate Escalator,
//	    privacyCfg PrivacyConfig) *MemoryCuratorLobe
//
// but in r1 the wire client is internal/provider.Provider — there is no
// apiclient.Client type. The Lobe also needs a writable *cortex.Workspace
// (LobeInput.Workspace is the read-only adapter — Lobes that publish
// must capture the write handle at construction time, mirroring the
// PlanUpdateLobe / ClarifyingQLobe pattern). The in-process hub.Bus is
// needed for the task.completed event subscriber in TASK-29.
//
// The wisdom.Store argument is also dropped in this adaptation: the
// curator only writes to memory.Store; wisdom is a read-side concern
// owned by MemoryRecallLobe.
//
// PrivacyConfig.AuditLog is renamed AuditLogPath (the writer is opened
// per-write so a long-running daemon does not pin a file descriptor
// across restarts and so tests can swap the path with a tempdir).
//
// The actual constructor used in production is therefore:
//
//	NewMemoryCuratorLobe(client, escalate, mem, privacy, ws, hubBus)
//
// The "trigger every 5 turns or on task.completed; ask Haiku; auto-apply
// fact-category writes; queue everything else for user confirm; append
// each auto-write to the JSONL audit log; respect the private-message
// taxonomy" contract from the spec is preserved verbatim.
package memorycurator

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/cortex/lobes/llm"
	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/memory"
	"github.com/RelayOne/r1/internal/provider"
)

// PrivacyConfig captures the operator-controlled curator policy. The
// three fields map to the three privacy contracts from spec §6:
//
//   - AutoCurateCategories: only candidates whose Category appears here
//     auto-write to memory; everything else queues a confirm-Note.
//   - SkipPrivateMessages: when true, drop the entire haiku call if any
//     message in the source window is tagged "private" — the model is
//     never shown private message text and never decides about it.
//   - AuditLogPath: filesystem path that receives one JSONL record per
//     auto-write. Default ~/.r1/cortex/curator-audit.jsonl is set by the
//     caller (the cortex daemon expands $HOME at startup).
type PrivacyConfig struct {
	// AutoCurateCategories is the allowlist of memory.Category values
	// the Lobe is permitted to auto-write. Default per spec OQ-7 is
	// {memory.CatFact}; everything else queues for confirmation.
	AutoCurateCategories []memory.Category

	// SkipPrivateMessages, when true, makes the Lobe skip its Haiku call
	// entirely if ANY message in the recent-tail window carries the
	// "private" tag. Default true per spec §6 ("MUST skip these").
	SkipPrivateMessages bool

	// AuditLogPath is the JSONL audit-log file path. One line per
	// auto-write. Empty disables audit logging; production callers
	// always populate it from ~/.r1/cortex/curator-audit.jsonl.
	AuditLogPath string
}

// MemoryCuratorLobe is the cortex.Lobe implementation declared in spec
// items 26–31. It is KindLLM — every triggered Run makes one Haiku call
// gated by the shared LLM-Lobe semaphore.
type MemoryCuratorLobe struct {
	client   provider.Provider
	escalate llm.Escalator
	mem      *memory.Store
	privacy  PrivacyConfig

	// ws is the writable Workspace handle. Captured at construction
	// because LobeInput.Workspace is read-only (the cortex.Lobe contract
	// freezes Notes-as-the-only-write-channel; production callers always
	// pass the cortex's own Workspace through here).
	ws *cortex.Workspace

	// hubBus is the in-process hub.Bus the Lobe subscribes to for
	// EventTaskCompleted (TASK-29). May be nil in tests that exercise
	// only Run/constructor shape.
	hubBus *hub.Bus

	// turnCount counts the number of Run() ticks observed. It drives
	// the every-5th-tick cadence in TASK-29. Stored atomically so tests
	// can inspect it without taking a lock.
	turnCount atomic.Uint64

	// triggerCount counts the number of times the trigger predicate
	// fired (i.e. the number of times the Lobe would have called Haiku).
	// Test-facing accessor exposes it via TriggerCount.
	triggerCount atomic.Uint64

	// onTrigger is the per-Run callback invoked when the trigger
	// predicate evaluates true. TASK-30 wires the default (the actual
	// Haiku + privacy-filter pipeline); TASK-29 leaves the hook
	// pluggable so the cadence test can observe trigger fires without
	// the LLM round trip.
	onTrigger func(ctx context.Context, in cortex.LobeInput)

	// subscribed guards the once-per-Lobe hub subscription registration.
	// Run installs the EventTaskCompleted subscriber the first time it
	// ticks; subsequent Runs see subscribed==true and skip.
	mu         sync.Mutex
	subscribed bool
}

// NewMemoryCuratorLobe constructs the Lobe. See the package doc for the
// API-adaptation note documenting why the signature differs from the
// literal spec snippet.
//
// Arguments:
//
//   - client: the model wire client. The Lobe calls ChatStream during
//     the trigger fires (TASK-29 / TASK-30).
//   - escalate: the Haiku→Sonnet escalation policy from TASK-2. The
//     MemoryCuratorLobe does not currently escalate — this argument is
//     captured for parity with sibling Lobes and to satisfy the
//     forward-compatibility hook in spec §Escalation hook.
//   - mem: the memory store. The Lobe calls Remember/Save on auto-write
//     candidates whose Category is in privacy.AutoCurateCategories.
//   - privacy: the operator-controlled privacy / audit-log policy.
//   - ws: writable cortex.Workspace handle for Publish. May be nil in
//     tests that exercise only construction or schema shape.
//   - hubBus: in-process hub.Bus for the task.completed subscriber. May
//     be nil; the Lobe simply skips subscription registration and the
//     event-driven trigger never fires (the every-5th-turn cadence
//     still works via Run).
func NewMemoryCuratorLobe(
	client provider.Provider,
	escalate llm.Escalator,
	mem *memory.Store,
	privacy PrivacyConfig,
	ws *cortex.Workspace,
	hubBus *hub.Bus,
) *MemoryCuratorLobe {
	l := &MemoryCuratorLobe{
		client:   client,
		escalate: escalate,
		mem:      mem,
		privacy:  privacy,
		ws:       ws,
		hubBus:   hubBus,
	}
	// Default onTrigger is the TASK-30 pipeline: privacy gate +
	// haikuCall + per-candidate auto-apply / confirm-queue + audit log.
	// Tests SetOnTrigger to a counting hook to assert TASK-29 cadence
	// in isolation.
	l.onTrigger = l.defaultOnTrigger
	return l
}

// ID satisfies cortex.Lobe. Stable string used as Note.LobeID. Spec §6
// names this Lobe "MemoryCuratorLobe"; the cortex uses kebab-case for
// LobeIDs (matching memory-recall / plan-update / clarifying-q).
func (l *MemoryCuratorLobe) ID() string { return "memory-curator" }

// Description satisfies cortex.Lobe. Used by /status output.
func (l *MemoryCuratorLobe) Description() string {
	return "scans recent conversation for durable project-facts and persists them"
}

// Kind satisfies cortex.Lobe. KindLLM — every triggered Run makes one
// Haiku call gated by the shared LLM-Lobe semaphore.
func (l *MemoryCuratorLobe) Kind() cortex.LobeKind { return cortex.KindLLM }

// SetOnTrigger overrides the per-trigger callback. Production code calls
// this from TASK-30's wiring to install the haikuCall closure; tests
// call it to inject a counting hook so they can assert TASK-29's
// trigger cadence without a real provider.
func (l *MemoryCuratorLobe) SetOnTrigger(fn func(ctx context.Context, in cortex.LobeInput)) {
	l.onTrigger = fn
}

// TriggerCount reports the number of ticks that satisfied the trigger
// predicate. Test-facing accessor for the cadence test (TASK-29).
func (l *MemoryCuratorLobe) TriggerCount() uint64 { return l.triggerCount.Load() }

// TurnCount reports the current per-Run tick counter. Test-facing.
func (l *MemoryCuratorLobe) TurnCount() uint64 { return l.turnCount.Load() }

// Run is the per-Round entry point. TASK-29 wires the every-5-turns
// trigger: each Run increments turnCount; turns at indexes 5/10/15/...
// satisfy the predicate and fire onTrigger (the haikuCall pipeline once
// TASK-30 lands). The task.completed event additionally fires
// onTrigger out-of-cadence via the hub subscriber installed in
// ensureSubscribed.
//
// ctx.Done is observed defensively — a cancelled tick returns nil so
// the LobeRunner contract holds. The hub subscription is installed
// exactly once on the first call (deferred so tests that never call
// Run can inspect the Lobe without bus side effects).
func (l *MemoryCuratorLobe) Run(ctx context.Context, in cortex.LobeInput) error {
	if err := ctx.Err(); err != nil {
		return nil
	}
	l.ensureSubscribed()

	turn := l.turnCount.Add(1)
	if !l.shouldTrigger(turn) {
		return nil
	}

	l.fireTrigger(ctx, in)
	return nil
}

// shouldTrigger evaluates the spec-item-29 trigger predicate:
//
//	turn % curatorTurnInterval == 0
//
// LobeRunner.Tick fires once per Round, and a Round corresponds to one
// assistant turn boundary in the cortex.MidturnNote pipeline (TASK-14
// of cortex-core). Counting from 1, turns 5/10/15/... satisfy
// turn%5==0. The task.completed event is a separate trigger handled
// in trigger.go's subscriber — it fires fireTrigger directly so it is
// not gated by this predicate.
func (l *MemoryCuratorLobe) shouldTrigger(turn uint64) bool {
	return turn > 0 && turn%uint64(curatorTurnInterval) == 0
}

// fireTrigger increments triggerCount and dispatches to onTrigger if
// set. Used by both the per-Run cadence path (Run) and the
// task.completed subscriber (handleTaskCompleted) so the trigger
// counter sees both paths uniformly.
func (l *MemoryCuratorLobe) fireTrigger(ctx context.Context, in cortex.LobeInput) {
	l.triggerCount.Add(1)
	if l.onTrigger != nil {
		l.onTrigger(ctx, in)
	}
}

// ensureSubscribed registers the hub subscriber exactly once across the
// lifetime of the Lobe. Safe with a nil hubBus (skipped). The actual
// subscriber registration lives in trigger.go (landed in TASK-29) —
// the scaffold's default is a no-op so TASK-26 can land the constructor
// shape without wiring the subscriber in the same commit.
func (l *MemoryCuratorLobe) ensureSubscribed() {
	l.mu.Lock()
	if l.subscribed || l.hubBus == nil {
		l.mu.Unlock()
		return
	}
	l.subscribed = true
	l.mu.Unlock()
	l.subscribeImpl()
}

// subscribeImpl is implemented in trigger.go (TASK-29). Defined as a
// method on *MemoryCuratorLobe in this file's sister source so the
// scaffold here can call it from ensureSubscribed.
