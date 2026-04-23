// Package plan — env_blocker.go
//
// Spec-1 item 6: report_env_issue worker tool.
//
// A worker dispatches report_env_issue when it hits an environment
// blocker it cannot fix (missing binary, network outage, credential,
// protected file). The tool:
//
//   1. Publishes a worker.env_blocked marker via an in-memory scratch
//      keyed by (sessionID, acID) so the descent engine's T3 classifier
//      can short-circuit to T5 (env fix) without paying 5 LLM calls.
//   2. Emits a log line the operator can see.
//   3. Returns a terse "reported" response so the worker ends cleanly.
//
// The scratch is package-level because it's crossed by two goroutines
// that don't share a struct pointer: the native-runner's tool handler
// and the descent engine's T3 logic. A sync.Map gives us free
// concurrency with no locking ceremony.
package plan

import (
	"sync"
)

// EnvBlockerScratch stores env_blocked markers keyed by sessionID +
// acID. A non-zero entry indicates the worker explicitly reported
// the AC as environmentally blocked.
type EnvBlockerScratch struct {
	mu      sync.RWMutex
	entries map[string]EnvBlockerReport
}

// EnvBlockerReport is the marker payload the worker supplies when
// invoking report_env_issue.
type EnvBlockerReport struct {
	SessionID           string
	TaskID              string
	ACID                string
	Issue               string
	WorkaroundAttempted string
	Suggestion          string
}

// globalEnvBlockerScratch is the process-wide default scratch. Tests
// can reset it via NewEnvBlockerScratch + SetDefaultEnvBlockerScratch
// when they need isolation.
var globalEnvBlockerScratch = NewEnvBlockerScratch()

// NewEnvBlockerScratch returns an empty scratch. Most callers use
// DefaultEnvBlockerScratch().
func NewEnvBlockerScratch() *EnvBlockerScratch {
	return &EnvBlockerScratch{entries: map[string]EnvBlockerReport{}}
}

// DefaultEnvBlockerScratch returns the process-wide scratch. Safe to
// call from any goroutine.
func DefaultEnvBlockerScratch() *EnvBlockerScratch {
	return globalEnvBlockerScratch
}

// envBlockerKey composes the scratch key from session + ac ids.
func envBlockerKey(sessionID, acID string) string {
	return sessionID + "\x00" + acID
}

// Record stores an env-blocked report. A subsequent descent T3 lookup
// via Get(sessionID, acID) will surface the report and short-circuit
// to T5.
func (s *EnvBlockerScratch) Record(r EnvBlockerReport) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[envBlockerKey(r.SessionID, r.ACID)] = r
}

// Get returns the recorded report for (sessionID, acID) if present.
// ok=false when no report was recorded.
func (s *EnvBlockerScratch) Get(sessionID, acID string) (EnvBlockerReport, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.entries[envBlockerKey(sessionID, acID)]
	return r, ok
}

// Clear wipes a specific (sessionID, acID) pair. Used when a descent
// retry wants to re-check from scratch.
func (s *EnvBlockerScratch) Clear(sessionID, acID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, envBlockerKey(sessionID, acID))
}

// ClearSession removes every entry tagged with sessionID. Called
// between descent cycles so a stale marker from a prior run doesn't
// poison a fresh T3.
func (s *EnvBlockerScratch) ClearSession(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix := sessionID + "\x00"
	for k := range s.entries {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(s.entries, k)
		}
	}
}

// EnvBlockerFastPathCategory is the verdict Category value set by
// T3 when a worker reported an env blocker. Constant so callers
// can test for it without stringly-typed comparisons.
const EnvBlockerFastPathCategory = "environment"
