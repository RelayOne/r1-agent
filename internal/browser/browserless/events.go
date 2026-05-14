// events.go: Browserless event-name constants and a minimal Hub.
//
// Constants match spec C6 §T11. The Hub is the in-process fanout
// the embedder wires into the existing CodeRadar / event-log
// pipeline; tests inject a SliceEmitter to capture the emission
// sequence.

package browserless

import (
	"sync"
)

// Event names — kept as exported constants so tests, the cost tracker,
// and the event-log sink share one source of truth. These mirror the
// canonical names in spec §T11 (browser.session_*, browser.navigate,
// browser.error, browser.fallback_used, browser.network_denied,
// browser.token_invalid, browser.budget_exhausted).
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

// SliceEmitter is a thread-safe in-memory EventEmitter that captures
// every Emit call. Used by tests; not intended for production.
type SliceEmitter struct {
	mu     sync.Mutex
	events []CapturedEvent
}

// CapturedEvent is one captured emission.
type CapturedEvent struct {
	Name  string
	Attrs map[string]any
}

// Emit records the event for later inspection.
func (s *SliceEmitter) Emit(name string, attrs map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make(map[string]any, len(attrs))
	for k, v := range attrs {
		cp[k] = v
	}
	s.events = append(s.events, CapturedEvent{Name: name, Attrs: cp})
}

// Events returns a stable snapshot of captured events.
func (s *SliceEmitter) Events() []CapturedEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]CapturedEvent, len(s.events))
	copy(out, s.events)
	return out
}

// Reset clears the captured event list.
func (s *SliceEmitter) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = nil
}

// Compile-time assertion.
var _ EventEmitter = (*SliceEmitter)(nil)
