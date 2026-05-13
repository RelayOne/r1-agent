// events.go: canonical browser.* event names + a tiny Hub.
//
// The Browserless and inhouse packages each define matching
// constants for in-package use; this file is the single source of
// truth that the executor / cmd/r1/ + downstream observability
// pipelines import. Both copies match — spec T11 § lists the names.
//
// The Hub gives callers a one-line subscribe path so the executor
// can fan events to multiple sinks (event log, CodeRadar, tests)
// without each provider learning about every sink.

package browser

import "sync"

// Event names (matches spec §T11). Mirrored in
// internal/browser/browserless/events.go for in-package use; the
// constants are byte-equal so subscribers can match on either set.
const (
	EvtSessionOpened   = "browser.session_opened"
	EvtSessionClosed   = "browser.session_closed"
	EvtNavigate        = "browser.navigate"
	EvtError           = "browser.error"
	EvtFallbackUsed    = "browser.fallback_used"
	EvtNetworkDenied   = "browser.network_denied"
	EvtTokenInvalid    = "browser.token_invalid"
	EvtBudgetExhausted = "browser.budget_exhausted"
)

// EventAttrs is the attribute payload for one event. Every provider
// emits at least the keys: tenant_id, provider, session_id,
// endpoint_host, duration_ms.
type EventAttrs map[string]any

// Hub is a thread-safe in-process event fanout. Construct with NewHub();
// register subscribers via Subscribe; the providers Emit through it.
type Hub struct {
	mu   sync.RWMutex
	subs []func(name string, attrs EventAttrs)
}

// NewHub returns an empty Hub.
func NewHub() *Hub { return &Hub{} }

// Subscribe registers a callback. Callbacks run synchronously in the
// emitter's goroutine; slow subscribers should buffer themselves.
func (h *Hub) Subscribe(fn func(name string, attrs EventAttrs)) {
	if fn == nil {
		return
	}
	h.mu.Lock()
	h.subs = append(h.subs, fn)
	h.mu.Unlock()
}

// Emit fans an event to every subscriber. Safe for concurrent use.
// Implements both this package's emit shape and the browserless /
// inhouse EventEmitter interface (a one-line adapter).
func (h *Hub) Emit(name string, attrs map[string]any) {
	h.mu.RLock()
	subs := make([]func(string, EventAttrs), len(h.subs))
	copy(subs, h.subs)
	h.mu.RUnlock()
	for _, fn := range subs {
		fn(name, EventAttrs(attrs))
	}
}
