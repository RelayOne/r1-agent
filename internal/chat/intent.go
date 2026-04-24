// Package chat — intent.go
//
// STOKE-008 partial: intent classifier + priority arbiter primitives
// for the dual-mode chat interactive track. Declares the 7 intents
// the interactive state machine recognizes and the priority ordering
// used when multiple user inputs collide during a session.
//
// Scope of this file:
//   - Intent taxonomy (7 constants) so downstream code has a single
//     shared vocabulary instead of string literals scattered across
//     dispatcher / clarify_responder / session.
//   - ClassifyIntent: a deterministic keyword-based classifier that's
//     good enough for unambiguous inputs ("abort", "stop") and
//     intentionally conservative on ambiguous ones — ambiguous inputs
//     return IntentQuery so they flow through the normal
//     conversational path rather than being mis-triaged as a
//     preemption. A future pass will back this with an LLM
//     classifier; the deterministic shell is in place so call sites
//     don't have to change when that lands.
//   - Priority: the 4-tier priority scale the arbiter uses to choose
//     between colliding inputs (abort > redirect > inject > continue)
//     per the SOW spec.
//
// This file is strictly additive: no existing intent handler reads
// from these constants yet. The dual-mode session loop (STOKE-008
// proper) will migrate onto this taxonomy in a follow-up commit so
// the migration can be reviewed independently of the scaffolding.
package chat

import "strings"

// Intent is a classification of user input a running agent receives.
// The four preemption intents (Abort / Redirect / Inject / Pause)
// race against the active plan; the three non-preemption intents
// (StatusQuery / Approve / Reject) are responses inside the normal
// turn flow.
type Intent string

const (
	// IntentAbort: the user wants the session to stop immediately.
	// Compensating transactions run; the plan isn't resumed.
	IntentAbort Intent = "abort"

	// IntentRedirect: the user wants the session to cancel its
	// current plan and execute a new directive. Distinct from
	// Abort because state is preserved and a new plan replaces
	// the old one.
	IntentRedirect Intent = "redirect"

	// IntentInject: the user wants to add a constraint or hint to
	// the active session without canceling. The agent acknowledges
	// and continues. Contrasts with Redirect (which cancels the
	// old plan) and StatusQuery (which doesn't change behavior).
	IntentInject Intent = "inject"

	// IntentPause: the user wants the session to halt with state
	// preserved. Resumption requires an explicit resume action
	// (not auto after N seconds).
	IntentPause Intent = "pause"

	// IntentStatusQuery: the user is asking about the session's
	// state ("where are you?", "what task are you on?"). No plan
	// changes.
	IntentStatusQuery Intent = "status_query"

	// IntentApprove: the user is approving a pending HITL request.
	// Matches the "approved" branch of an HITLResponse node.
	IntentApprove Intent = "approve"

	// IntentReject: the user is rejecting a pending HITL request.
	// Matches the "rejected" branch.
	IntentReject Intent = "reject"

	// IntentQuery is the fallback classification: the input is
	// ambiguous or doesn't fit any of the preemption / approval
	// intents, so it flows through the normal conversational
	// handler. Intentionally NOT one of the 7 explicit intents in
	// the SOW — it's the bucket the classifier uses when it
	// shouldn't claim higher confidence than it has.
	IntentQuery Intent = "query"
)

// AllIntents returns the 7 classified intents (excluding the
// fallback IntentQuery). Used by tests and by the priority arbiter
// to iterate the recognized set.
func AllIntents() []Intent {
	return []Intent{
		IntentAbort, IntentRedirect, IntentInject, IntentPause,
		IntentStatusQuery, IntentApprove, IntentReject,
	}
}

// Priority is the preemption rank assigned to each intent. Higher
// values preempt lower. The arbiter orders colliding inputs by
// Priority descending so the winning input is always the
// most-urgent one in the queue.
//
// Scale:
//
//	30 — Abort
//	20 — Redirect / Pause
//	10 — Inject
//	 0 — everything else (StatusQuery, Approve, Reject, Query)
//
// Abort is strictly above Redirect because "stop everything" should
// never lose to "start something else" when both arrive in the same
// tick. Pause shares a rank with Redirect because neither cancels
// compensating work but both interrupt the active plan; arrival
// order breaks the tie.
func Priority(i Intent) int {
	switch i {
	case IntentAbort:
		return 30
	case IntentRedirect, IntentPause:
		return 20
	case IntentInject:
		return 10
	case IntentStatusQuery, IntentApprove, IntentReject, IntentQuery:
		return 0
	default:
		return 0
	}
}

// HigherPriority reports whether intent a should preempt intent b.
// Ties return false so the earlier-arrived input wins — the arbiter
// is responsible for tracking arrival order and using this predicate
// only for strict-priority decisions.
func HigherPriority(a, b Intent) bool {
	return Priority(a) > Priority(b)
}

// ClassifyIntent is the deterministic first-pass intent classifier.
// Returns the best-match Intent. Ambiguous input returns IntentQuery
// rather than guessing — the cost of misclassifying "stop the
// timer" as IntentAbort is orders of magnitude higher than missing
// a legitimate abort request (the user can always say "abort" again
// and the second try will hit the exact-match rule).
//
// Matching is intentionally conservative:
//
//   - Exact tokens ("abort", "cancel", "stop") → IntentAbort
//   - Leading directive phrases ("instead do X", "now do X") →
//     IntentRedirect
//   - Soft hints ("also", "but make sure") → IntentInject
//   - Approval/rejection words bound to the "request" context
//     surface only when HITL state is active; see
//     ClassifyIntentInHITLContext.
//   - Everything else → IntentQuery
func ClassifyIntent(input string) Intent {
	s := strings.ToLower(strings.TrimSpace(input))
	if s == "" {
		return IntentQuery
	}

	// Exact single-word abort tokens. No substring matching here
	// — "aborting the launch timer" shouldn't preempt a running
	// plan.
	abortExact := map[string]bool{
		"abort": true, "cancel": true, "stop": true,
		"halt": true, "kill": true, "quit": true,
	}
	if abortExact[s] {
		return IntentAbort
	}
	// Leading-phrase abort ("abort the session", "stop now").
	for _, prefix := range []string{
		"abort ", "cancel the", "stop the", "halt the",
		"kill the session", "shut it down",
	} {
		if strings.HasPrefix(s, prefix) {
			return IntentAbort
		}
	}

	// Pause signals. Exact + leading-phrase.
	pauseExact := map[string]bool{"pause": true, "wait": true, "hold": true}
	if pauseExact[s] {
		return IntentPause
	}
	for _, prefix := range []string{"pause the", "hold on", "wait a moment", "wait for me"} {
		if strings.HasPrefix(s, prefix) {
			return IntentPause
		}
	}

	// Redirect: explicit "instead" / "now do" / "change of plan".
	for _, phrase := range []string{
		"instead ", "instead,", "instead:",
		"change of plan", "scrap that", "forget that",
		"now do ", "new plan", "different approach",
	} {
		if strings.Contains(s, phrase) {
			return IntentRedirect
		}
	}

	// Inject: soft hint phrases that add constraints without
	// canceling.
	for _, phrase := range []string{
		"also ", "and also ", "but make sure ", "also make sure",
		"remember to ", "don't forget ", "make sure you ",
		"additionally ", "additionally,", "by the way ",
	} {
		if strings.Contains(s, phrase) {
			return IntentInject
		}
	}

	// Status queries.
	if strings.HasPrefix(s, "status") || strings.HasPrefix(s, "where are you") ||
		strings.HasPrefix(s, "what task") || strings.HasPrefix(s, "how far") ||
		s == "?" || s == "status?" {
		return IntentStatusQuery
	}

	// Approval / rejection are only safe to classify deterministic-
	// ally when they're bare — "yes do it" shouldn't approve a
	// pending HITL request if the user is just answering "yes"
	// to an unrelated question in their head. Callers with HITL
	// context should use ClassifyIntentInHITLContext which
	// lowers the threshold.
	if s == "approved" || s == "approve" || s == "lgtm" || s == "looks good" {
		return IntentApprove
	}
	// Bare "no" is deliberately NOT classified as IntentReject here —
	// outside HITL context it's too ambiguous (could be answering an
	// ambient question). ClassifyIntentInHITLContext relaxes this.
	if s == "rejected" || s == "reject" {
		return IntentReject
	}

	return IntentQuery
}

// ClassifyIntentInHITLContext lowers the bar for Approve / Reject
// when the session is currently awaiting an HITL response.
// Callers set hitlPending=true when an HITLRequest is open; in that
// case bare "yes" / "no" / "ok" classify as Approve / Reject
// respectively. Outside of an HITL context those same words stay
// IntentQuery (conservative default) so ambient "yes I'm following"
// doesn't approve something.
func ClassifyIntentInHITLContext(input string, hitlPending bool) Intent {
	if !hitlPending {
		return ClassifyIntent(input)
	}
	s := strings.ToLower(strings.TrimSpace(input))
	switch s {
	case "yes", "y", "ok", "okay", "sure", "do it", "go ahead":
		return IntentApprove
	case "no", "n", "nope", "don't", "dont", "cancel that":
		return IntentReject
	}
	return ClassifyIntent(input)
}
