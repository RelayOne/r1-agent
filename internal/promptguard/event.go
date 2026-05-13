// Package promptguard — event.go
//
// ThreatEvent is the structured wire format emitted whenever promptguard
// detects an injection-shaped payload at a governance gate or intake
// site. It is published on the durable bus when one is wired in (so
// supervisor rules and the per-session budget tracker (T5) can react),
// and falls back to a one-line stderr log when the bus is not available.
//
// See specs/promptguard-hardening.md §T1 items 1-5.
package promptguard

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/RelayOne/r1/internal/correlation"
)

// ThreatEvent is the canonical wire shape for a single detection. It
// flows through the bus as a `promptguard.threat_detected` event (T1
// item 1) and is also the input to the per-session budget tracker (T5).
type ThreatEvent struct {
	// Phase names the governance gate that produced the detection:
	// "plan" | "execute" | "verify" | "convergence" | "tool_input" |
	// "skill" | "research" | "feasibility".
	Phase string `json:"phase"`
	// Source is a free-form provenance tag, e.g.
	// "plan:feasibility:attacker.example.com" or
	// "tool_input:browse.fetch". Read by operators in audit logs.
	Source string `json:"source"`
	// PatternName matches Threat.PatternName from Scan.
	PatternName string `json:"pattern_name"`
	// Severity is one of "low" | "medium" | "high" | "critical".
	// Drives the per-session budget weight (T5).
	Severity string `json:"severity"`
	// Action is the disposition the caller applied:
	// "warn" | "strip" | "reject".
	Action string `json:"action"`
	// Excerpt is the already-redacted excerpt, capped at ~120 chars,
	// safe to log without re-emitting the injection payload at full
	// length.
	Excerpt string `json:"excerpt"`
	// SessionID is pulled from correlation.FromContext when available,
	// otherwise "" (standalone non-session runs).
	SessionID string `json:"session_id"`
	// Timestamp is set to time.Now().UTC() by ThreatToEvent.
	Timestamp time.Time `json:"timestamp"`
}

// EmitterFunc is the test-seam type passed to SetEmitter. It is invoked
// exactly once per Emit call (synchronously). Implementations MUST be
// safe to call from any goroutine.
type EmitterFunc func(ThreatEvent)

var (
	emitterMu       sync.RWMutex
	currentEmitter  EmitterFunc = defaultStderrEmitter
	currentPublisher BusPublisher
)

// BusPublisher is the narrow contract this package needs to publish to
// internal/bus/ without taking a direct import dependency (which would
// pull the whole WAL chain into unit tests that don't need it). The
// daemon's bootDaemon wires this up via SetBusPublisher. Tests can
// install a fake or leave it unset for stderr-only behaviour.
type BusPublisher interface {
	Publish(evt ThreatEvent) error
}

// SetEmitter installs fn as the active emitter. Pass nil to restore the
// default stderr emitter. Returns the previous emitter so a test can
// restore it in a defer.
func SetEmitter(fn EmitterFunc) EmitterFunc {
	emitterMu.Lock()
	defer emitterMu.Unlock()
	prev := currentEmitter
	if fn == nil {
		currentEmitter = defaultStderrEmitter
	} else {
		currentEmitter = fn
	}
	return prev
}

// SetBusPublisher installs an opaque publisher so threat events flow to
// internal/bus/ alongside the stderr/emitter path. Pass nil to detach.
// The daemon should call this once at startup; tests and unit-test
// callers can leave it unset.
func SetBusPublisher(p BusPublisher) {
	emitterMu.Lock()
	defer emitterMu.Unlock()
	currentPublisher = p
}

// Emit publishes a ThreatEvent to the currently registered emitter and
// (if configured) the bus. Safe to call with a nil context.
//
// Spec promptguard-hardening §T5 item 20: after publishing the threat
// event Emit calls IncrementBudget for the session. When the
// increment trips the threshold a second event,
// promptguard.budget.exceeded, is published synchronously so the
// supervisor rule can dispatch a daemon.session.kill action.
func Emit(ctx context.Context, evt ThreatEvent) {
	if evt.Timestamp.IsZero() {
		evt.Timestamp = time.Now().UTC()
	}
	if evt.SessionID == "" {
		evt.SessionID = correlation.FromContext(ctx).SessionID
	}
	emitterMu.RLock()
	em := currentEmitter
	pub := currentPublisher
	emitterMu.RUnlock()

	if em != nil {
		em(evt)
	}
	if pub != nil {
		// Bus publication is best-effort: a failed publish does not
		// suppress the stderr/test-seam path, and we never panic out
		// of an Emit call on the hot governance path.
		_ = pub.Publish(evt)
	}

	// Budget bookkeeping (§T5 item 20). IncrementBudget is a no-op
	// when SessionID is empty (standalone non-session runs do not
	// accumulate budget). When the trip threshold is breached we
	// publish the budget-exceeded event so the supervisor can react.
	exceeded, snapshot := IncrementBudget(evt.SessionID, evt.Severity)
	if exceeded && pub != nil {
		// Cast the payload to ThreatEvent-shape via the publisher's
		// envelope. The BusPublisher contract is intentionally
		// narrow (Publish(ThreatEvent)); to send the budget-exceeded
		// payload we use the dedicated BudgetPublisher seam if the
		// publisher implements it. Daemons that wire the bus install
		// a publisher satisfying BOTH interfaces; tests can install
		// either independently.
		if bp, ok := pub.(BudgetPublisher); ok {
			_ = bp.PublishBudgetExceeded(SnapshotToPayload(snapshot))
		}
	}
}

// BudgetPublisher is the bus publisher's surface for the second
// event in the Emit pipeline. Kept separate from BusPublisher so a
// publisher implementation can choose whether to expose the
// budget-exceeded surface (the daemon does; some tests only need
// threat events).
type BudgetPublisher interface {
	PublishBudgetExceeded(p BudgetExceededPayload) error
}

// ThreatToEvent maps a Threat (from Scan) into a ThreatEvent suitable
// for Emit. Phase/source/action come from the caller's context; the
// pattern name, severity, and excerpt come from the Threat itself. The
// session ID is pulled from ctx (correlation.FromContext) so wire-site
// callers do not need to thread the session ID separately.
func ThreatToEvent(phase, source string, t Threat) ThreatEvent {
	return ThreatToEventWithAction(phase, source, "", t)
}

// ThreatToEventWithAction is ThreatToEvent with an explicit action
// string. Useful when the caller already knows whether the gate ran
// warn / strip / reject and wants the audit log to reflect that.
func ThreatToEventWithAction(phase, source, action string, t Threat) ThreatEvent {
	sev := t.Severity
	if sev == "" {
		sev = "medium"
	}
	return ThreatEvent{
		Phase:       phase,
		Source:      source,
		PatternName: t.PatternName,
		Severity:    sev,
		Action:      action,
		Excerpt:     t.Excerpt,
		Timestamp:   time.Now().UTC(),
	}
}

// EmitReport is a convenience wrapper that walks every threat in a
// Sanitize Report and emits one ThreatEvent per threat. The action
// string is derived from the Report's Action field (warn/strip/reject).
// No-op for clean reports.
func EmitReport(ctx context.Context, phase string, rep Report) {
	if len(rep.Threats) == 0 {
		return
	}
	action := actionName(rep.Action)
	for _, t := range rep.Threats {
		Emit(ctx, ThreatToEventWithAction(phase, rep.Source, action, t))
	}
}

// defaultStderrEmitter writes the spec-mandated single-line format to
// the standard log facility (which writes to stderr by default in Go).
// The format matches the spec: "promptguard: threat <phase>/<source>:
// <pattern> [<severity>] action=<action>".
func defaultStderrEmitter(evt ThreatEvent) {
	log.Printf("promptguard: threat %s/%s: %s [%s] action=%s",
		evt.Phase, evt.Source, evt.PatternName, evt.Severity, evt.Action)
}

// formatEmitterLine is the single-line format used by the default
// emitter. Exposed so tests can build the expected string without
// duplicating the format string.
func formatEmitterLine(evt ThreatEvent) string {
	return fmt.Sprintf("promptguard: threat %s/%s: %s [%s] action=%s",
		evt.Phase, evt.Source, evt.PatternName, evt.Severity, evt.Action)
}
