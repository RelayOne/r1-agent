// Package ledger — CS-3 stdout-event hook.
//
// WriteNode calls the package-level LedgerAppendHook (if set) after
// successfully persisting a chain-tier record. CloudSwarm's supervisor
// subscribes to "stoke.ledger.appended" events to render the workspace
// ledger pane in real time.
//
// The hook is a function-pointer seam, not a struct field, because
// Store is used from many call sites across the codebase and adding a
// field would touch every caller. The hook is set once at process
// startup from main (or stays nil for tests / standalone runs that
// don't care about event emission).
//
// Guarantees:
//   - Nil hook → no-op, no allocation.
//   - Hook invoked AFTER chain-tier write succeeds (no phantom events
//     on IO error).
//   - Payload never includes content — only {node_id, type, parent_hash}
//     — so this path stays safe even before crypto-shred/retention
//     enforcement runs.

package ledger

import "sync/atomic"

// LedgerAppendEvent is the payload published to LedgerAppendHook.
// Kept intentionally small — richer detail belongs in the file on disk.
type LedgerAppendEvent struct {
	NodeID     string `json:"node_id"`
	Type       string `json:"type"`
	ParentHash string `json:"parent_hash"`
}

// ledgerAppendHook stores the optional callback. atomic.Value keeps
// reads lock-free on the hot path; Set happens once at startup.
var ledgerAppendHook atomic.Value // of type func(LedgerAppendEvent)

// SetLedgerAppendHook registers a callback invoked after every
// successful WriteNode. Pass nil to clear. Safe to call before/after
// any number of WriteNode calls.
func SetLedgerAppendHook(hook func(LedgerAppendEvent)) {
	if hook == nil {
		ledgerAppendHook.Store((func(LedgerAppendEvent))(nil))
		return
	}
	ledgerAppendHook.Store(hook)
}

// fireLedgerAppendHook is called by WriteNode after the chain record
// is persisted. Wraps the atomic.Value load and nil check so WriteNode
// stays readable.
func fireLedgerAppendHook(ev LedgerAppendEvent) {
	raw := ledgerAppendHook.Load()
	if raw == nil {
		return
	}
	hook, _ := raw.(func(LedgerAppendEvent))
	if hook == nil {
		return
	}
	hook(ev)
}
