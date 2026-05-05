// Package rulecheck implements the RuleCheckLobe — a Deterministic Lobe
// that mirrors supervisor rule firings into the cortex Workspace as
// Notes. Critical Notes (trust.*, consensus.dissent.*) are picked up by
// cortex-core's PreEndTurnCheckFn so the agent loop refuses end_turn
// while a rule violation is unaddressed.
//
// Spec: specs/cortex-concerns.md items 13–15 ("RuleCheckLobe").
//
// API adaptation note (recorded in the per-task commit message):
//
// The spec proposes the constructor signature
//
//	func NewRuleCheckLobe(sup *supervisor.Supervisor, wal *bus.Bus) *RuleCheckLobe
//
// but the actual supervisor implementation publishes its rule firings on
// the durable bus.Bus as bus.Event{Type: bus.EvtSupervisorRuleFired} —
// the *Supervisor type itself does not expose a "fire stream" handle.
// The Lobe therefore subscribes to the durable bus directly via
// bus.Pattern{TypePrefix:"supervisor.rule.fired"} and never consults the
// *Supervisor pointer. To match the file-level invariant that Lobes
// PUBLISH Notes (and LobeInput.Workspace is read-only), the constructor
// also takes a writable *cortex.Workspace handle, mirroring the WALKeeper
// pattern in internal/cortex/lobes/walkeeper/lobe.go.
//
// The actual constructor used in production is:
//
//	NewRuleCheckLobe(durable *bus.Bus, ws *cortex.Workspace) *RuleCheckLobe
//
// The "subscribe to supervisor.rule.fired" contract from the spec is
// preserved verbatim — every rule firing the supervisor publishes to the
// durable bus is converted to a Note exactly once.
package rulecheck

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/cortex/lobes/llm"
)

// firedPattern is the subscription pattern the Lobe registers on the
// durable bus. It matches every supervisor.rule.fired event regardless of
// scope; per-mission filtering is the supervisor's responsibility.
var firedPattern = bus.Pattern{TypePrefix: string(bus.EvtSupervisorRuleFired)}

// subscriberID identifies the Lobe's bus subscription for diagnostics.
// Currently bus.Subscription assigns its own UUID id; we keep this
// constant for stable log/metric labels in future iterations.
const subscriberID = "rule-check"

// RuleCheckLobe converts supervisor rule firings into cortex.Notes.
//
// The Lobe is KindDeterministic — it makes no LLM calls and does not bind
// against LobeSemaphore. The conversion runs synchronously inside the
// bus.Subscription handler goroutine and does not require LobeRunner
// ticks to fire (Note publication is event-driven, not Round-driven).
type RuleCheckLobe struct {
	durable *bus.Bus
	ws      *cortex.Workspace

	// runMu serializes Run invocations. The Lobe contract allows
	// repeated Run calls across daemon restarts; runMu ensures only one
	// active subscription is alive at a time and that a re-Run after a
	// graceful Stop registers a fresh subscription rather than
	// deadlocking on the prior cancellation.
	runMu sync.Mutex

	// sub is the active bus.Subscription handle. Captured here so Run
	// can Cancel it on ctx.Done. nil between Run invocations.
	sub *bus.Subscription
}

// NewRuleCheckLobe constructs the Lobe.
//
// Arguments (per the API-adaptation note in the package doc):
//
//   - durable: the durable bus.Bus the supervisor publishes rule firings
//     to. May be nil; Run becomes a no-op that simply blocks on ctx.Done
//     so the LobeRunner contract still observes graceful shutdown.
//   - ws: writable cortex.Workspace handle. May be nil; in that case
//     incoming rule events are silently dropped (Publish has nowhere to
//     land). Production callers always pass the cortex's own Workspace.
func NewRuleCheckLobe(durable *bus.Bus, ws *cortex.Workspace) *RuleCheckLobe {
	return &RuleCheckLobe{
		durable: durable,
		ws:      ws,
	}
}

// ID satisfies cortex.Lobe.
func (l *RuleCheckLobe) ID() string { return "rule-check" }

// Description satisfies cortex.Lobe.
func (l *RuleCheckLobe) Description() string {
	return "publishes Notes on supervisor rule firings"
}

// Kind satisfies cortex.Lobe. Deterministic — no LLM calls.
func (l *RuleCheckLobe) Kind() cortex.LobeKind { return cortex.KindDeterministic }

// Run subscribes to supervisor.rule.fired events on the durable bus and
// blocks until ctx is cancelled. The subscription is cancelled on exit
// so a subsequent Run call after a daemon restart can re-register
// cleanly. With a nil durable bus the Lobe degenerates into a wait-on-
// ctx loop so the LobeRunner contract still observes graceful shutdown.
func (l *RuleCheckLobe) Run(ctx context.Context, in cortex.LobeInput) error {
	_ = in
	if l.durable == nil {
		<-ctx.Done()
		return nil
	}

	// Serialize Run invocations: only one active subscription at a time.
	l.runMu.Lock()
	defer l.runMu.Unlock()

	sub := l.durable.Subscribe(firedPattern, func(evt bus.Event) {
		l.handleRuleFired(evt)
	})
	l.sub = sub
	defer func() {
		sub.Cancel()
		l.sub = nil
	}()

	<-ctx.Done()
	return nil
}

// SubscriberID returns the Lobe's stable subscriber identifier. Exposed
// for tests that want to assert subscription registration without
// touching bus internals.
func (l *RuleCheckLobe) SubscriberID() string { return subscriberID }

// handleRuleFired is the per-event entry point invoked from the
// bus.Subscription handler goroutine. Decodes the supervisor's rule-
// fired payload, derives a severity from the rule name, and publishes a
// sticky Note (ExpiresAfterRound=0) into the Workspace. Errors are
// silent: a Note we cannot validate (e.g. missing rule name) is dropped
// rather than crashing the bus subscription goroutine. With a nil
// workspace the call is a no-op so tests can drive subscription
// registration without spinning up a workspace.
func (l *RuleCheckLobe) handleRuleFired(evt bus.Event) {
	if l.ws == nil {
		return
	}
	pl, ok := decodeRuleFiredPayload(evt)
	if !ok {
		return
	}
	note := l.noteFromRuleFired(evt, pl)
	_ = l.ws.Publish(note)
}

// ruleFiredPayload mirrors supervisor.publishRuleFired's schema. We
// re-declare it locally to avoid importing the supervisor package
// (which would create a tight coupling between cortex and the rules
// engine for what is otherwise a pure JSON contract).
type ruleFiredPayload struct {
	SupervisorID   string `json:"supervisor_id"`
	SupervisorType string `json:"supervisor_type"`
	RuleName       string `json:"rule_name"`
	RulePriority   int    `json:"rule_priority"`
	TriggerEventID string `json:"trigger_event_id"`
	TriggerType    string `json:"trigger_type"`
	Rationale      string `json:"rationale"`
}

// decodeRuleFiredPayload best-effort decodes the JSON payload of a
// supervisor.rule.fired event. Returns ok=false if Payload is empty or
// unmarshalling fails — both cases collapse to "drop the event" rather
// than propagate an error to the bus subscription.
func decodeRuleFiredPayload(evt bus.Event) (ruleFiredPayload, bool) {
	var pl ruleFiredPayload
	if len(evt.Payload) == 0 {
		return pl, false
	}
	if err := json.Unmarshal(evt.Payload, &pl); err != nil {
		return pl, false
	}
	if pl.RuleName == "" {
		return pl, false
	}
	return pl, true
}

// noteFromRuleFired builds the cortex.Note for a rule firing. ID is
// "rule-"+evt.ID per spec item 15; the Workspace assigns its own
// monotonic ID on Publish, so this string is stored on Note.Meta as a
// causal ref instead.
func (l *RuleCheckLobe) noteFromRuleFired(evt bus.Event, pl ruleFiredPayload) cortex.Note {
	sev := severityFor(pl.RuleName)
	title := truncateRunes(fmt.Sprintf("rule fired: %s", pl.RuleName), 80)
	body := pl.Rationale
	if body == "" {
		body = fmt.Sprintf("supervisor rule %s fired without a rationale (trigger: %s)",
			pl.RuleName, pl.TriggerType)
	}
	return cortex.Note{
		LobeID:   l.ID(),
		Severity: sev,
		Title:    title,
		Body:     body,
		Tags:     []string{"rule:" + pl.RuleName},
		Meta: map[string]any{
			llm.MetaActionKind:        "rule-violation",
			llm.MetaExpiresAfterRound: uint64(0),
			llm.MetaRefs:              []string{"rule-" + evt.ID},
		},
	}
}

// severityFor maps a supervisor rule name to a cortex.Severity per spec
// item 14:
//
//   - trust.*               → critical
//   - consensus.dissent.*   → critical
//   - drift.*               → warning
//   - cross_team.*          → warning
//   - everything else       → info
//
// API adaptation: the spec writes "consensus.dissent.*" with a trailing
// dot, but the actual supervisor rule names use underscores after the
// "consensus.dissent" stem (e.g. "consensus.dissent_requires_address").
// We HasPrefix-match on "consensus.dissent" so the spec's intent —
// "every dissent rule is critical" — holds against the real names.
func severityFor(ruleName string) cortex.Severity {
	switch {
	case strings.HasPrefix(ruleName, "trust."):
		return cortex.SevCritical
	case strings.HasPrefix(ruleName, "consensus.dissent"):
		return cortex.SevCritical
	case strings.HasPrefix(ruleName, "drift."):
		return cortex.SevWarning
	case strings.HasPrefix(ruleName, "cross_team."):
		return cortex.SevWarning
	default:
		return cortex.SevInfo
	}
}

// truncateRunes returns at most n runes from s. Mirrors the helper in
// memoryrecall/lobe.go; duplicated here so the two Lobes do not share an
// internal package.
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}
