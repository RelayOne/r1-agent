// Package sessionhub — promptguard_kill.go
//
// daemon.session.kill consumer. Spec
// specs/promptguard-hardening.md §T5 item 22.
//
// The supervisor's promptguard.budget_exceeded rule publishes a
// daemon.session.kill event when the per-session injection budget
// trips. This file owns the sessionhub-side dispatcher that consumes
// that event, looks up the session by ID, and tears it down via the
// existing Delete primitive.
//
// Kill-latency target: ≤100ms from event publication to teardown.
// The implementation is intentionally non-blocking: the consumer
// invokes Delete inline (Delete itself only invokes cancelRun, which
// is a non-blocking close-of-context), so the dispatch path is
// constant-time independent of how long the in-flight Run goroutine
// takes to unwind.

package sessionhub

import (
	"encoding/json"
	"sync/atomic"
)

// KillReason carries the structured fields a kill-action event ships.
// Stable wire shape so test code can construct fixtures without
// reaching into the supervisor package.
type KillReason struct {
	SessionID string `json:"session_id"`
	Reason    string `json:"reason"`
	Source    string `json:"source,omitempty"`
}

// killCounter is exported via KillCount() for operator dashboards.
// atomic.Int64 so concurrent kills do not race on a plain counter.
var killCounter atomic.Int64

// KillCount returns the cumulative number of promptguard-driven
// session kills since process start. Operator dashboards surface
// this alongside the budget-exceeded event count.
func KillCount() int64 { return killCounter.Load() }

// ConsumeKillEvent is the sessionhub-side entry point invoked by the
// daemon's bus dispatcher when a daemon.session.kill event lands. It
// unmarshals the payload, looks up the session, and calls Delete.
// Returns nil on success (session terminated) and an error when the
// payload is malformed or the session id is unknown — the daemon
// dispatcher logs the error and continues.
func (h *SessionHub) ConsumeKillEvent(rawPayload []byte) error {
	var k KillReason
	if err := json.Unmarshal(rawPayload, &k); err != nil {
		return err
	}
	if k.SessionID == "" {
		return ErrEmptySessionID
	}
	// Non-blocking: Delete cancels the run context and removes the
	// in-memory entry; the in-flight goroutine winds down
	// asynchronously. Total dispatch latency is bounded by the
	// sync.Map LoadAndDelete plus cancelRun, both constant-time.
	if err := h.Delete(k.SessionID); err != nil {
		return err
	}
	killCounter.Add(1)
	return nil
}

// ErrEmptySessionID is returned by ConsumeKillEvent when the
// dispatched event has no session id. Distinct sentinel so
// callers (the bus subscriber) can suppress the log line for
// events that legitimately have no target (extremely rare in
// practice; the supervisor rule's Evaluate already filters these).
var ErrEmptySessionID = errEmptySessionID{}

type errEmptySessionID struct{}

func (errEmptySessionID) Error() string { return "sessionhub: empty session id in kill event" }
