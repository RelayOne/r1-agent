// Package agentloop — circuit.go
//
// STOKE-007 anti-laziness circuit breakers. Detects runtime
// patterns that indicate an agent is either stuck (token-rate
// anomaly, reasoning thrash, circular output) or cutting
// corners (step-count overrun, silent text-only turns).
// Callers plumb this into their per-turn hooks so circuit-
// opens surface immediately rather than after a post-hoc review.
//
// Scope of this file:
//
//   - StepCounter with per-node-class limit
//   - TokenRateAnomaly detector (alerts at 3× rolling average)
//   - CircularOutputDetector (hash-based cycle detection)
//   - ReasoningThrash detector (no-tool-call streak)
//   - ForcedToolUse check (every turn must call a tool; explicit
//     `done` tool terminates)
//
// No hook into the existing loop.go wiring here — this file
// provides primitives callers can compose. A follow-up commit
// will plug these into the agent loop's per-turn path.
package agentloop

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

// NodeClass tags a node by its intended effort level so callers
// can configure different step-count ceilings per class. Matches
// the SOW's "configurable per node class" requirement.
type NodeClass string

// NodeClass tiers drive per-class step-count ceilings (see
// DefaultStepLimits). Strings are stable; they appear in plans and
// in telemetry.
const (
	NodeClassLight  NodeClass = "light"  // trivial edit: cap ~25 steps
	NodeClassMedium NodeClass = "medium" // typical refactor / feature: cap ~75
	NodeClassHeavy  NodeClass = "heavy"  // large rewrite: cap ~200
	NodeClassXL     NodeClass = "xl"     // multi-file mission: cap ~400
)

// DefaultStepLimits maps node classes to default step ceilings.
// Operators can override via StepCounter.SetLimit.
var DefaultStepLimits = map[NodeClass]int{
	NodeClassLight:  25,
	NodeClassMedium: 75,
	NodeClassHeavy:  200,
	NodeClassXL:     400,
}

// ErrStepLimitExceeded is returned by StepCounter.Increment when
// a new step would exceed the class's declared ceiling. Callers
// surface this as a blocking hard-stop — the agent must either
// justify why it needs more steps (triggering a replan) or end.
var ErrStepLimitExceeded = errors.New("agentloop: step limit exceeded for node class")

// StepCounter tracks per-node-class step counts with configurable
// ceilings. Thread-safe.
type StepCounter struct {
	mu     sync.Mutex
	limits map[NodeClass]int
	counts map[string]int // keyed by node ID
}

// NewStepCounter returns a counter seeded with DefaultStepLimits.
func NewStepCounter() *StepCounter {
	limits := make(map[NodeClass]int, len(DefaultStepLimits))
	for k, v := range DefaultStepLimits {
		limits[k] = v
	}
	return &StepCounter{
		limits: limits,
		counts: map[string]int{},
	}
}

// SetLimit overrides the step ceiling for a class. Safe to call
// at any time; existing counts aren't affected.
func (s *StepCounter) SetLimit(cls NodeClass, limit int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.limits[cls] = limit
}

// Increment records one step for nodeID under class cls. Returns
// ErrStepLimitExceeded when the incremented count would exceed
// the ceiling. Returns the new count on success.
func (s *StepCounter) Increment(nodeID string, cls NodeClass) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	limit, ok := s.limits[cls]
	if !ok {
		// Unknown class falls through to the most permissive
		// class so an unconfigured node doesn't hard-fail.
		limit = s.limits[NodeClassXL]
	}
	next := s.counts[nodeID] + 1
	if next > limit {
		return s.counts[nodeID], fmt.Errorf("%w: %q (limit=%d)", ErrStepLimitExceeded, cls, limit)
	}
	s.counts[nodeID] = next
	return next, nil
}

// Reset clears the step count for nodeID. Called when a replan
// cycle starts (fresh counter for the regenerated plan).
func (s *StepCounter) Reset(nodeID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.counts, nodeID)
}

// Count returns the current step count for nodeID.
func (s *StepCounter) Count(nodeID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counts[nodeID]
}

// ------------------------------------------------------------
// Token-rate anomaly detection
// ------------------------------------------------------------

// TokenRateDetector watches per-step token consumption and fires
// when the instantaneous rate exceeds 3× the rolling average
// (SOW threshold). Alert is advisory — the detector doesn't halt
// execution; callers inspect HasAlert and decide what to do.
type TokenRateDetector struct {
	mu         sync.Mutex
	window     []int // rolling samples (tokens per step)
	windowSize int
	mult       float64
	alerted    bool
}

// NewTokenRateDetector returns a detector with a 10-sample
// window and the SOW's 3× multiplier. Callers that want tighter
// or looser detection can tune via setters (not shipped yet —
// defaults are sufficient for the initial cut).
func NewTokenRateDetector() *TokenRateDetector {
	return &TokenRateDetector{windowSize: 10, mult: 3.0}
}

// Observe records a step's token count. Returns true when this
// step triggered the alert (first breach only); subsequent
// breaches return false until Reset. This avoids alert spam on
// persistent anomaly.
func (t *TokenRateDetector) Observe(tokens int) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if tokens < 0 {
		tokens = 0
	}
	// Need at least 3 samples before computing a meaningful
	// average; too-short windows produce false positives.
	if len(t.window) >= 3 {
		var sum int
		for _, n := range t.window {
			sum += n
		}
		avg := float64(sum) / float64(len(t.window))
		if avg > 0 && float64(tokens) > avg*t.mult && !t.alerted {
			t.alerted = true
			t.window = append(t.window, tokens)
			if len(t.window) > t.windowSize {
				t.window = t.window[len(t.window)-t.windowSize:]
			}
			return true
		}
	}
	t.window = append(t.window, tokens)
	if len(t.window) > t.windowSize {
		t.window = t.window[len(t.window)-t.windowSize:]
	}
	return false
}

// HasAlert reports whether the detector has fired in the
// current window. Cleared by Reset.
func (t *TokenRateDetector) HasAlert() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.alerted
}

// Reset clears the alert state + rolling window. Called at
// phase transitions so each phase measures independently.
func (t *TokenRateDetector) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.window = t.window[:0]
	t.alerted = false
}

// ------------------------------------------------------------
// Circular-output detector
// ------------------------------------------------------------

// CircularOutputDetector hashes each step's output and flags
// when the same hash has appeared ThresholdReps times within
// WindowSize recent steps — the agent is looping without
// producing progress.
type CircularOutputDetector struct {
	mu            sync.Mutex
	recent        []string // hashes
	windowSize    int
	thresholdReps int
}

// NewCircularOutputDetector returns a detector with a 6-step
// window and 3-repetition threshold (same output 3× in last 6
// steps → cycle).
func NewCircularOutputDetector() *CircularOutputDetector {
	return &CircularOutputDetector{windowSize: 6, thresholdReps: 3}
}

// Observe records an output chunk. Returns true when the chunk's
// hash has now appeared >= thresholdReps times in the window.
func (c *CircularOutputDetector) Observe(output string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	h := hashOutput(output)
	c.recent = append(c.recent, h)
	if len(c.recent) > c.windowSize {
		c.recent = c.recent[len(c.recent)-c.windowSize:]
	}
	count := 0
	for _, r := range c.recent {
		if r == h {
			count++
		}
	}
	return count >= c.thresholdReps
}

// Reset clears the window. Called at phase transitions.
func (c *CircularOutputDetector) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recent = c.recent[:0]
}

func hashOutput(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8]) // 8 bytes is plenty for cycle detection
}

// ------------------------------------------------------------
// Reasoning thrash detector
// ------------------------------------------------------------

// ReasoningThrashDetector fires when the agent has produced N
// consecutive text-only turns (no tool calls) — a common
// failure mode where the model narrates its reasoning but
// doesn't actually do anything. SOW STOKE-007 #4 wants forced
// tool usage: every step should make a tool call or call the
// explicit `done` tool.
type ReasoningThrashDetector struct {
	mu                sync.Mutex
	consecutiveTextOnly int
	threshold         int
}

// NewReasoningThrashDetector returns a detector with the SOW's
// implicit threshold of 3 consecutive text-only turns.
func NewReasoningThrashDetector() *ReasoningThrashDetector {
	return &ReasoningThrashDetector{threshold: 3}
}

// Observe records a turn. toolCalled=true on turns that invoked
// at least one tool (including the `done` tool); false for pure-
// text narration. Returns true when the consecutive text-only
// streak has reached threshold.
func (r *ReasoningThrashDetector) Observe(toolCalled bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if toolCalled {
		r.consecutiveTextOnly = 0
		return false
	}
	r.consecutiveTextOnly++
	return r.consecutiveTextOnly >= r.threshold
}

// Reset clears the streak. Called when the caller accepts the
// thrash state and wants to give the agent another chance after
// an intervention.
func (r *ReasoningThrashDetector) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.consecutiveTextOnly = 0
}

// ------------------------------------------------------------
// Heartbeat check
// ------------------------------------------------------------

// HeartbeatMonitor watches a logical clock of "last productive
// activity" per task. Fires Stale() when no activity has been
// recorded within the configured stalenessWindow. Useful as a
// watchdog on long-running sessions where a hung LLM call
// wouldn't otherwise surface.
type HeartbeatMonitor struct {
	mu             sync.Mutex
	lastPulse      map[string]time.Time
	stalenessWindow time.Duration
}

// NewHeartbeatMonitor returns a monitor with a 10-minute staleness
// window — the SOW's default for heartbeat-based stall detection.
func NewHeartbeatMonitor() *HeartbeatMonitor {
	return &HeartbeatMonitor{
		lastPulse:       map[string]time.Time{},
		stalenessWindow: 10 * time.Minute,
	}
}

// Pulse records activity for taskID right now.
func (h *HeartbeatMonitor) Pulse(taskID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastPulse[taskID] = time.Now()
}

// Stale reports whether taskID hasn't pulsed within the
// staleness window. Returns false for unknown task IDs (no
// pulse ever = "not started" not "stale").
func (h *HeartbeatMonitor) Stale(taskID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	last, ok := h.lastPulse[taskID]
	if !ok {
		return false
	}
	return time.Since(last) > h.stalenessWindow
}
