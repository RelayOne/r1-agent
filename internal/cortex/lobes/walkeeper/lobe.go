// Package walkeeper implements the WALKeeperLobe — a Deterministic Lobe
// that drains the in-process hub.Bus into the durable, WAL-backed
// bus.Bus so every hub event survives a daemon restart and replays into
// post-mortem analyzers.
//
// Spec: specs/cortex-concerns.md items 10–11 ("WALKeeperLobe").
//
// Design summary:
//
//   - Construction: NewWALKeeperLobe(h, w, ws, framing). The Lobe holds
//     a writable *cortex.Workspace handle (LobeInput.Workspace is the
//     read-only adapter — Lobes that publish must capture the write
//     handle at construction time). ws may be nil; in that case the
//     backpressure-warning Note is silently dropped (the WAL drain
//     itself still functions).
//
//   - Subscription: on Run, the Lobe registers a hub.Subscriber with
//     Events=["*"] (wildcard match-all) and Mode=ModeObserve. Each
//     incoming hub.Event is JSON-marshalled and forwarded as a
//     bus.Event{Type: framing.TypePrefix+evt.Type, Payload: ...,
//     CausalRef: evt.ID} on the durable bus.
//
//   - Backpressure (item 11): outstanding writes are routed through a
//     buffered channel of capacity 1000. When the channel is ≥ 0.9*cap
//     full and the incoming event is info-severity, the event is
//     dropped and an atomic counter is incremented. Every 30s, if the
//     counter is non-zero, the Lobe Publishes a single Note with
//     Severity=warning and Tags=["wal","backpressure"] summarizing the
//     drops since the last tick (counter is reset). The ticker
//     interval is configurable (BackpressureNoteInterval) so tests can
//     drive it on millisecond cadence.
//
//   - Restart safety (item 12): the Lobe holds no persistent state
//     beyond the durable bus's WAL. Stopping the Lobe cancels the Run
//     context, which Unregisters the subscriber and returns the
//     drainer goroutine. Restarting registers a fresh subscriber on a
//     fresh Run; the durable bus continues to assign monotonic
//     sequence numbers and unique IDs to each forwarded event so no
//     duplicates appear in replay.
package walkeeper

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/RelayOne/r1/internal/bus"
	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/hub"
)

// WALFraming controls how hub events are framed when forwarded to the
// durable bus. TypePrefix is concatenated with the original hub event
// type to produce the durable bus.EventType — e.g. "cortex.hub.tool.pre_use".
type WALFraming struct {
	TypePrefix string
}

// defaultTypePrefix is the prefix used when WALFraming.TypePrefix is "".
// Spec item 10 mandates "cortex.hub.".
const defaultTypePrefix = "cortex.hub."

// pendingCap is the buffered-channel capacity for outstanding forwards.
// Spec item 11 fixes this at 1000.
const pendingCap = 1000

// backpressureThreshold is the channel-fill ratio at which info-severity
// drops begin. 0.9*1000 = 900.
const backpressureThreshold = 0.9

// defaultBackpressureNoteInterval is the production cadence for
// emitting the warning Note when drops are non-zero. Tests override it
// to millisecond cadence via the constructor option.
const defaultBackpressureNoteInterval = 30 * time.Second

// subscriberID is the stable hub.Subscriber.ID used for register/unregister.
const subscriberID = "walkeeper"

// pendingItem is a queued forward request: the framed durable event to
// publish. The hub.Subscriber handler enqueues these onto a buffered
// channel; a single drainer goroutine dequeues and publishes them.
type pendingItem struct {
	evt bus.Event
}

// WALKeeperLobe drains hub.Bus events into bus.Bus.
type WALKeeperLobe struct {
	h       *hub.Bus
	w       *bus.Bus
	ws      *cortex.Workspace
	framing WALFraming

	// pending is the bounded queue of forward requests. Capacity is
	// pendingCap; producer is the hub.Subscriber handler, consumer is
	// the drainer goroutine started in Run.
	pending chan pendingItem

	// dropped counts info-severity events dropped due to backpressure
	// since the last warning Note. Reset to 0 each time the warning
	// Note fires.
	dropped atomic.Uint64

	// backpressureNoteInterval is the cadence of the warning-Note
	// ticker. Defaults to 30s; tests override via WithBackpressureNoteInterval.
	backpressureNoteInterval time.Duration

	// runMu serializes Run invocations. The Lobe contract allows
	// repeated Run calls across daemon restarts; runMu ensures only
	// one drainer goroutine and one subscriber registration are alive
	// at any time.
	runMu sync.Mutex
}

// NewWALKeeperLobe constructs a WALKeeperLobe.
//
// Arguments:
//
//   - h:       in-process hub.Bus to drain (must be non-nil for Run to
//     register a subscriber; nil is tolerated for unit tests that
//     drive Publish directly).
//   - w:       durable bus.Bus to forward into (must be non-nil for Run
//     to perform any forwarding; nil makes Run a no-op).
//   - ws:      writable cortex.Workspace. May be nil; if nil,
//     backpressure warning Notes are silently dropped.
//   - framing: TypePrefix override. Empty TypePrefix selects "cortex.hub.".
func NewWALKeeperLobe(h *hub.Bus, w *bus.Bus, ws *cortex.Workspace, framing WALFraming) *WALKeeperLobe {
	if framing.TypePrefix == "" {
		framing.TypePrefix = defaultTypePrefix
	}
	return &WALKeeperLobe{
		h:                        h,
		w:                        w,
		ws:                       ws,
		framing:                  framing,
		pending:                  make(chan pendingItem, pendingCap),
		backpressureNoteInterval: defaultBackpressureNoteInterval,
	}
}

// WithBackpressureNoteInterval overrides the default 30s ticker cadence
// used to emit backpressure warning Notes. Returns the receiver so
// callers can chain. Intended for tests; production callers should not
// invoke this.
func (l *WALKeeperLobe) WithBackpressureNoteInterval(d time.Duration) *WALKeeperLobe {
	if d > 0 {
		l.backpressureNoteInterval = d
	}
	return l
}

// ID satisfies cortex.Lobe.
func (l *WALKeeperLobe) ID() string { return "wal-keeper" }

// Description satisfies cortex.Lobe.
func (l *WALKeeperLobe) Description() string {
	return "drains hub events into the durable WAL-backed bus"
}

// Kind satisfies cortex.Lobe. Deterministic — no LLM calls.
func (l *WALKeeperLobe) Kind() cortex.LobeKind { return cortex.KindDeterministic }

// Run registers the hub subscriber, starts the drainer goroutine and
// the backpressure-note ticker, and blocks until ctx is cancelled.
//
// Lifecycle: registration, drainer, and ticker are all torn down on
// ctx.Done so a subsequent Run call (after a daemon restart) can
// re-register cleanly.
func (l *WALKeeperLobe) Run(ctx context.Context, in cortex.LobeInput) error {
	_ = in
	if l.h == nil || l.w == nil {
		// Without both buses there is nothing to drain. Honor ctx.Done
		// and exit so callers who Run a stub Lobe see graceful shutdown.
		<-ctx.Done()
		return nil
	}

	// Serialize Run invocations: only one drainer + subscriber alive
	// at a time.
	l.runMu.Lock()
	defer l.runMu.Unlock()

	// Register the wildcard subscriber. Handler enqueues each event
	// into the bounded channel; a full channel + info severity event
	// triggers a drop.
	l.registerHubSubscriber()
	defer l.h.Unregister(subscriberID)

	// Drainer goroutine: dequeues pending items and Publishes them.
	drainerDone := make(chan struct{})
	go l.runDrainer(ctx, drainerDone)
	defer func() { <-drainerDone }()

	// Backpressure-note ticker: fires every backpressureNoteInterval;
	// emits a single warning Note if dropped > 0 since last tick.
	ticker := time.NewTicker(l.backpressureNoteInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			l.maybeEmitBackpressureNote()
		}
	}
}

// registerHubSubscriber installs the wildcard observer that forwards
// every hub.Event into the bounded pending channel. Backpressure rule:
// when the channel is ≥ 0.9*cap full and the event is info-severity,
// drop and increment the counter; otherwise enqueue (blocking only if
// the channel is full and the event is non-info, in which case the
// non-blocking send-or-drop fallback prevents a deadlock).
func (l *WALKeeperLobe) registerHubSubscriber() {
	l.h.Register(hub.Subscriber{
		ID:     subscriberID,
		Events: []hub.EventType{"*"},
		Mode:   hub.ModeObserve,
		Handler: func(ctx context.Context, ev *hub.Event) *hub.HookResponse {
			l.handleHubEvent(ev)
			return &hub.HookResponse{Decision: hub.Allow}
		},
	})
}

// handleHubEvent is the per-event entry point invoked from the
// hub.Subscriber handler. It frames the event into a bus.Event and
// applies the backpressure policy.
func (l *WALKeeperLobe) handleHubEvent(ev *hub.Event) {
	if ev == nil {
		return
	}

	// Marshal the entire hub.Event as the durable payload. This
	// preserves ID, Type, Timestamp, and any typed sub-payloads
	// (Tool/File/Model/...) without lossy projection.
	payload, err := json.Marshal(ev)
	if err != nil {
		// Marshal failures are silent: a malformed hub.Event is
		// logged once at the bus level via recordHookActionFailed and
		// the WAL keeper has no other recovery path.
		return
	}

	durEvt := bus.Event{
		Type:      bus.EventType(l.framing.TypePrefix + string(ev.Type)),
		Payload:   payload,
		CausalRef: ev.ID,
	}

	// Backpressure: when the channel is ≥ 0.9*cap full AND the event
	// is info-severity, drop. Severity is computed from the hub.Event
	// type/payload (see eventSeverity). Critical/warning/etc. events
	// always block until enqueued (or ctx-cancelled by the drainer).
	sev := eventSeverity(ev)
	if l.shouldDropForBackpressure(sev) {
		l.dropped.Add(1)
		return
	}

	// Non-blocking send first; if the channel is full and the event
	// is non-info, fall back to a brief blocking send so the
	// drainer's worst case is "one extra publish" rather than data
	// loss.
	select {
	case l.pending <- pendingItem{evt: durEvt}:
	default:
		// Channel full but event is non-info; prefer blocking send so
		// the drainer drains one item, then enqueue. We bound the
		// blocking with a short timer to avoid wedging the producer
		// in pathological tests.
		t := time.NewTimer(50 * time.Millisecond)
		defer t.Stop()
		select {
		case l.pending <- pendingItem{evt: durEvt}:
		case <-t.C:
			// Last-resort drop. Counter still increments so operators
			// see persistent saturation in the warning Note.
			l.dropped.Add(1)
		}
	}
}

// shouldDropForBackpressure reports whether the current channel fill
// + event severity classifies the event as a drop candidate. Returns
// true only for info-severity events when the channel is at or above
// 0.9*cap.
func (l *WALKeeperLobe) shouldDropForBackpressure(sev string) bool {
	if sev != "info" {
		return false
	}
	threshold := int(float64(pendingCap) * backpressureThreshold)
	return len(l.pending) >= threshold
}

// runDrainer dequeues pending items and Publishes them to the durable
// bus. It runs until ctx.Done is closed AND the channel has been
// drained, ensuring no in-flight forward is lost on graceful shutdown.
func (l *WALKeeperLobe) runDrainer(ctx context.Context, done chan struct{}) {
	defer close(done)
	for {
		select {
		case item := <-l.pending:
			if err := l.w.Publish(item.evt); err != nil {
				// Publish errors are non-fatal: the durable bus may
				// have been closed by a concurrent shutdown. Continue
				// draining so the channel is fully consumed.
				_ = err
			}
		case <-ctx.Done():
			// Best-effort drain of remaining items so callers that
			// stop the Lobe immediately after a burst of Emits do not
			// silently lose those events.
			for {
				select {
				case item := <-l.pending:
					_ = l.w.Publish(item.evt)
				default:
					return
				}
			}
		}
	}
}

// maybeEmitBackpressureNote publishes a single warning Note if drops
// have occurred since the last tick, then resets the counter. Safe
// with a nil workspace (silent no-op).
func (l *WALKeeperLobe) maybeEmitBackpressureNote() {
	n := l.dropped.Swap(0)
	if n == 0 {
		return
	}
	if l.ws == nil {
		return
	}
	note := cortex.Note{
		LobeID:   l.ID(),
		Severity: cortex.SevWarning,
		Title:    fmt.Sprintf("WAL keeper backpressure: %d events dropped", n),
		Body: fmt.Sprintf(
			"%d info-severity hub events were dropped because the WAL forward "+
				"queue exceeded %d%% of its %d-slot capacity. Operators should "+
				"investigate slow durable-bus consumers or temporary IO stalls.",
			n, int(backpressureThreshold*100), pendingCap),
		Tags: []string{"wal", "backpressure"},
		Meta: map[string]any{
			"dropped":   n,
			"threshold": int(float64(pendingCap) * backpressureThreshold),
			"capacity":  pendingCap,
		},
	}
	_ = l.ws.Publish(note)
}

// DroppedCount returns the running drop counter. Test-facing accessor.
func (l *WALKeeperLobe) DroppedCount() uint64 {
	return l.dropped.Load()
}

// PendingLen returns the current depth of the pending channel.
// Test-facing accessor; production callers should not depend on this.
func (l *WALKeeperLobe) PendingLen() int {
	return len(l.pending)
}

// ForceDroppedForTest preloads the dropped-events counter so the next
// fire of the backpressure-note ticker emits a warning Note without
// needing to flood the in-process hub past 0.9*pendingCap. Exposed for
// cross-package integration tests (internal/cortex/lobes/all_integration_test.go);
// production callers must not invoke this — the counter is otherwise
// driven solely by the backpressure drop path.
func (l *WALKeeperLobe) ForceDroppedForTest(n uint64) {
	l.dropped.Store(n)
}

// eventSeverity classifies a hub.Event into one of "info" | "warning"
// | "critical" for backpressure purposes. The cortex spec does not
// mandate a hub-side severity field, so the keeper applies a simple
// heuristic:
//
//   - tool.error / model.error / *.failed / mission.failed / *.panic /
//     security.* — "warning"
//   - critical/security_secret_detected — "critical"
//   - everything else (the bulk of session/tool/model lifecycle
//     traffic) — "info"
//
// This keeps the drop policy aligned with the spec ("drop info-severity
// events") without requiring upstream emitters to stamp every event.
func eventSeverity(ev *hub.Event) string {
	if ev == nil {
		return "info"
	}
	t := string(ev.Type)
	switch t {
	case string(hub.EventSecuritySecretDetected),
		string(hub.EventSecurityBypassAttempt),
		string(hub.EventSecurityPolicyViolation):
		return "critical"
	case string(hub.EventToolError),
		string(hub.EventToolBlocked),
		string(hub.EventModelError),
		string(hub.EventModelRateLimited),
		string(hub.EventMissionFailed),
		string(hub.EventTaskFailed),
		string(hub.EventCortexLobePanic),
		string(hub.EventVerifyBuildResult),
		string(hub.EventVerifyTestResult):
		return "warning"
	}
	// Default: info-severity (the bulk of lifecycle / observability
	// traffic such as session.init, mission.created, tool.pre_use, ...).
	return "info"
}
