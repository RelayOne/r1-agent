// Package plan — status_hook.go
//
// CS-3 (plan half) stdout-event hook. SetState on a Node fires the
// package-level StatusChangeHook (if set) after a successful state
// transition. CloudSwarm's supervisor subscribes to
// "stoke.plan.node_updated" events to render the workspace plan
// pane in real time.
//
// Mirrors the seam in internal/ledger/emit_hook.go (LedgerAppendHook).
// A function-pointer seam keeps the transition site free of a
// streamjson-emitter dependency, which would otherwise require
// threading an emitter through every caller that flips a Node's
// state. The hook is set once at process startup from main (or
// stays nil for tests and standalone runs that don't need event
// emission).
//
// Guarantees:
//   - Nil hook → no-op, no allocation.
//   - Hook invoked AFTER the transition table accepts the move
//     and n.Status has been updated (no phantom events on
//     ErrInvalidTransition).
//   - Payload carries only {node_id, status, title} — no description,
//     no acceptance criteria — to stay safe under retention /
//     redaction policies.

package plan

import "sync/atomic"

// StatusChangeEvent is the payload published to StatusChangeHook.
// Small on purpose — richer detail belongs in the node itself.
type StatusChangeEvent struct {
	NodeID string `json:"node_id"`
	Status string `json:"status"`
	Title  string `json:"title"`
}

// statusChangeHook stores the optional callback. atomic.Value keeps
// reads lock-free on the hot path; Set happens once at startup.
var statusChangeHook atomic.Value // of type func(StatusChangeEvent)

// SetStatusChangeHook registers a callback invoked after every
// successful Node.SetState transition. Pass nil to clear. Safe to
// call before/after any number of SetState calls.
func SetStatusChangeHook(hook func(StatusChangeEvent)) {
	if hook == nil {
		statusChangeHook.Store((func(StatusChangeEvent))(nil))
		return
	}
	statusChangeHook.Store(hook)
}

// fireStatusChangeHook is called by SetState after the Node's
// Status field has been updated. Wraps the atomic.Value load and
// nil check so the transition site stays readable.
func fireStatusChangeHook(ev StatusChangeEvent) {
	raw := statusChangeHook.Load()
	if raw == nil {
		return
	}
	hook, _ := raw.(func(StatusChangeEvent))
	if hook == nil {
		return
	}
	hook(ev)
}
