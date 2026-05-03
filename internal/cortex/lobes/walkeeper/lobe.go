// Package walkeeper implements the WALKeeperLobe — a Deterministic Lobe
// that drains the in-process hub.Bus into the durable, WAL-backed
// bus.Bus so every hub event survives a daemon restart and replays into
// post-mortem analyzers.
//
// Spec: specs/cortex-concerns.md item 10 ("WALKeeperLobe — frame and forward").
//
// Design summary:
//
//   - Construction: NewWALKeeperLobe(h, w, ws, framing). The Lobe holds
//     a writable *cortex.Workspace handle (LobeInput.Workspace is the
//     read-only adapter — Lobes that publish must capture the write
//     handle at construction time). ws may be nil; downstream tasks
//     (TASK-11) use it to publish backpressure warning Notes.
//
//   - Subscription: on Run, the Lobe registers a hub.Subscriber with
//     Events=["*"] (wildcard match-all) and Mode=ModeObserve. Each
//     incoming hub.Event is JSON-marshalled and forwarded as a
//     bus.Event{Type: framing.TypePrefix+evt.Type, Payload: ...,
//     CausalRef: evt.ID} on the durable bus.
//
//   - Restart safety: the Lobe holds no persistent state beyond the
//     durable bus's WAL. Stopping the Lobe cancels the Run context,
//     which Unregisters the subscriber and returns the drainer
//     goroutine. Restarting registers a fresh subscriber on a fresh
//     Run; the durable bus continues to assign monotonic sequence
//     numbers and unique IDs to each forwarded event so no duplicates
//     appear in replay.
package walkeeper

import (
	"context"
	"encoding/json"
	"sync"

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
// Future tasks (TASK-11) apply backpressure when this fills past 90%.
const pendingCap = 1000

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
//   - ws:      writable cortex.Workspace. May be nil; held for use by
//     downstream tasks (TASK-11 backpressure warning Notes).
//   - framing: TypePrefix override. Empty TypePrefix selects "cortex.hub.".
func NewWALKeeperLobe(h *hub.Bus, w *bus.Bus, ws *cortex.Workspace, framing WALFraming) *WALKeeperLobe {
	if framing.TypePrefix == "" {
		framing.TypePrefix = defaultTypePrefix
	}
	return &WALKeeperLobe{
		h:       h,
		w:       w,
		ws:      ws,
		framing: framing,
		pending: make(chan pendingItem, pendingCap),
	}
}

// ID satisfies cortex.Lobe.
func (l *WALKeeperLobe) ID() string { return "wal-keeper" }

// Description satisfies cortex.Lobe.
func (l *WALKeeperLobe) Description() string {
	return "drains hub events into the durable WAL-backed bus"
}

// Kind satisfies cortex.Lobe. Deterministic — no LLM calls.
func (l *WALKeeperLobe) Kind() cortex.LobeKind { return cortex.KindDeterministic }

// Run registers the hub subscriber, starts the drainer goroutine, and
// blocks until ctx is cancelled.
//
// Lifecycle: registration and drainer are torn down on ctx.Done so a
// subsequent Run call (after a daemon restart) can re-register cleanly.
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
	// into the bounded channel.
	l.registerHubSubscriber()
	defer l.h.Unregister(subscriberID)

	// Drainer goroutine: dequeues pending items and Publishes them.
	drainerDone := make(chan struct{})
	go l.runDrainer(ctx, drainerDone)
	defer func() { <-drainerDone }()

	<-ctx.Done()
	return nil
}

// registerHubSubscriber installs the wildcard observer that forwards
// every hub.Event into the bounded pending channel.
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
// enqueues it onto the pending channel. The non-blocking send falls
// back to a brief blocking send so a transient overflow does not
// silently lose events.
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

	// Non-blocking send. A blocking-send fallback (with backpressure
	// drop policy) is added in TASK-11.
	select {
	case l.pending <- pendingItem{evt: durEvt}:
	default:
		// Channel full: a future task (TASK-11) wires drop counter +
		// backpressure note. For now, fall back to a blocking send so
		// the event is never silently dropped.
		l.pending <- pendingItem{evt: durEvt}
	}
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

// PendingLen returns the current depth of the pending channel.
// Test-facing accessor; production callers should not depend on this.
func (l *WALKeeperLobe) PendingLen() int {
	return len(l.pending)
}
