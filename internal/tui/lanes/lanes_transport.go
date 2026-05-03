package lanes

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/hub"
)

// Transport feeds lane lifecycle events into the panel and accepts
// operator commands. Two implementations ship: a local in-process
// transport that wires directly to a cortex.Workspace + hub.Bus, and a
// remote transport that dials r1d over WebSocket. See specs/tui-lanes.md
// §"Subscription Wiring" / "Implementation Checklist" item 8.
//
// Implementations MUST:
//   - On the first Subscribe call, replay the full current lane list as
//     a laneListMsg before any laneStartMsg / laneTickMsg.
//   - On disconnect-and-reconnect, replay the lane list again so
//     surfaces can resync without state loss (§Acceptance Criteria
//     "WHEN the daemon connection drops...").
//   - Translate hub.EventLane* events into the panel's tea.Msg taxonomy
//     (laneStartMsg / laneTickMsg / laneEndMsg / killAckMsg).
//   - Honor ctx cancellation by closing the producer's outbound channel
//     within ~one tick window.
type Transport interface {
	// List returns the current snapshot of all lanes. Used by the
	// producer to seed the panel before the streaming subscription
	// starts and on reconnect.
	List(ctx context.Context) ([]LaneSnapshot, error)

	// Subscribe streams lane events through the supplied channel until
	// ctx is cancelled or an unrecoverable transport error occurs.
	// The implementation OWNS the goroutine; it returns once the
	// subscription terminates. Callers should run Subscribe under
	// runProducer's coalescer.
	Subscribe(ctx context.Context, sessionID string, out chan<- LaneEvent) error

	// Kill issues the operator-initiated termination for one lane. The
	// implementation acknowledges via a separate killAckMsg routed
	// through Subscribe's stream; Kill itself returns the dispatch
	// error (transport / RPC failure), not the lane outcome.
	Kill(ctx context.Context, laneID string) error

	// KillAll terminates every non-terminal lane. Used for the
	// double-confirm K command per spec §"Keybinding Map".
	KillAll(ctx context.Context) error

	// Pin toggles the orthogonal pinned flag (spec §3.2). Surfaces
	// render pinned lanes above unpinned regardless of status.
	Pin(ctx context.Context, laneID string, pinned bool) error
}

// LaneEvent is the transport-level event envelope. The producer
// translates these into typed tea.Msgs (laneStartMsg, laneTickMsg,
// laneEndMsg, laneListMsg, killAckMsg). Keeping the transport
// interface event-typed (rather than tea.Msg-typed) means the same
// transport can serve other consumers (e.g. the desktop-augmentation
// surface) without dragging Bubble Tea symbols across the boundary.
type LaneEvent struct {
	// Kind tags the envelope. One of:
	//   "list"   — snapshot (Snapshot populated)
	//   "start"  — lane created (Snapshot populated)
	//   "tick"   — incremental update (Snapshot populated; treat as
	//              partial — only Status/Activity/Tokens/CostUSD/etc.
	//              guaranteed valid).
	//   "end"    — lane reached terminal state.
	//   "kill_ack" — daemon accepted/rejected a kill (LaneID + Err).
	//   "budget" — budget update (SpentUSD + LimitUSD).
	Kind string

	// Snapshot carries lane state on list/start/tick/end events.
	Snapshot LaneSnapshot

	// List carries the full snapshot on Kind=="list".
	List []LaneSnapshot

	// LaneID + Err for kill_ack.
	LaneID string
	Err    string

	// SpentUSD + LimitUSD for budget.
	SpentUSD float64
	LimitUSD float64
}

// --- localTransport ---
//
// The in-process transport for `r1 chat-interactive --lanes`. Reads the
// current lane list directly from a cortex.Workspace and subscribes to
// the same workspace's hub.Bus for streaming updates.
//
// Per spec §"Subscription Wiring" embedded mode bullet 2: "if the
// socket is missing, the transport spins up an in-process bus shim that
// reads from the cortex Workspace directly (so r1 chat-interactive
// works without r1d)". This implementation IS that shim.
type localTransport struct {
	ws *cortex.Workspace
}

// NewLocalTransport returns a Transport backed by the supplied cortex
// workspace. The workspace's Bus() must be non-nil (the panel
// subscribes to it).
func NewLocalTransport(ws *cortex.Workspace) Transport {
	return &localTransport{ws: ws}
}

// List returns a snapshot of every lane currently registered on the
// workspace. Order is unspecified at the cortex layer; the model sorts
// by createdAt then laneID before rendering.
func (t *localTransport) List(_ context.Context) ([]LaneSnapshot, error) {
	if t.ws == nil {
		return nil, errors.New("lanes: local transport unbound (workspace nil)")
	}
	src := t.ws.Lanes()
	out := make([]LaneSnapshot, 0, len(src))
	for _, l := range src {
		out = append(out, snapshotFromCortex(l))
	}
	return out, nil
}

// Subscribe registers a hub subscriber that translates EventLane*
// events into typed LaneEvent envelopes on out. Returns when ctx is
// cancelled; the subscriber is unregistered before return.
func (t *localTransport) Subscribe(ctx context.Context, sessionID string, out chan<- LaneEvent) error {
	if t.ws == nil {
		return errors.New("lanes: local transport unbound (workspace nil)")
	}
	bus := t.ws.Bus()
	if bus == nil {
		return errors.New("lanes: local transport workspace has nil Bus")
	}

	// Replay current snapshot first per spec §"Subscription Wiring"
	// (List on subscribe + reconnect). Send blocking with ctx so a
	// hung consumer terminates with the panel's ctx.
	snap, err := t.List(ctx)
	if err != nil {
		return err
	}
	select {
	case out <- LaneEvent{Kind: "list", List: snap}:
	case <-ctx.Done():
		return ctx.Err()
	}

	// Subscribe in observe mode (async, fire-and-forget). The handler
	// pushes into out under a non-blocking select so a slow consumer
	// drops events rather than back-pressuring the bus (the producer
	// goroutine in lanes_producer.go already coalesces, so a drop
	// here is recoverable on the next coalesce flush).
	subID := fmt.Sprintf("tui.lanes.%s.%d", sessionID, time.Now().UnixNano())
	bus.Register(hub.Subscriber{
		ID:   subID,
		Mode: hub.ModeObserve,
		Events: []hub.EventType{
			hub.EventLaneCreated,
			hub.EventLaneStatus,
			hub.EventLaneDelta,
			hub.EventLaneCost,
			hub.EventLaneNote,
			hub.EventLaneKilled,
		},
		Handler: func(_ context.Context, ev *hub.Event) *hub.HookResponse {
			le := translateHubLaneEvent(t.ws, ev)
			if le.Kind == "" {
				return nil
			}
			select {
			case out <- le:
			default:
				// drop: producer will flush latest state on next tick.
			}
			return nil
		},
	})

	<-ctx.Done()
	// hub.Bus has no public Unregister yet (the project's existing
	// Subscriber lifecycle is process-scoped). That's fine: the
	// handler closes over ctx.Done() implicitly via the out channel —
	// the producer that owns ctx also owns out, and on ctx cancel the
	// producer stops draining out. Subsequent handler invocations
	// see a full channel and drop. For long-lived processes that
	// repeatedly subscribe/unsubscribe we'll add Bus.Unregister in a
	// follow-up; the current panel use case (one subscribe per
	// session) keeps the leak bounded to one subscriber per panel
	// instance.
	return ctx.Err()
}

// Kill terminates a lane by routing through the cortex workspace's
// canonical Lane.Kill (spec §"Subscription Wiring" mentions
// r1.lanes.kill as the JSON-RPC name; for in-process this is just the
// direct call). The kill_ack envelope is synthesized synchronously
// because Lane.Kill returns the result inline.
func (t *localTransport) Kill(_ context.Context, laneID string) error {
	if t.ws == nil {
		return errors.New("lanes: local transport unbound (workspace nil)")
	}
	l, ok := t.ws.GetLane(laneID)
	if !ok {
		return fmt.Errorf("lanes: kill: unknown lane %q", laneID)
	}
	return l.Kill("cancelled_by_operator")
}

// KillAll iterates every non-terminal lane and kills it. Errors are
// joined with errors.Join so the operator sees every failure.
func (t *localTransport) KillAll(ctx context.Context) error {
	if t.ws == nil {
		return errors.New("lanes: local transport unbound (workspace nil)")
	}
	var errs []error
	for _, l := range t.ws.Lanes() {
		if l == nil || l.IsTerminal() {
			continue
		}
		if err := l.Kill("cancelled_by_operator"); err != nil {
			errs = append(errs, fmt.Errorf("kill %s: %w", l.ID, err))
		}
		// best-effort context check to bail out fast on cancellation.
		if err := ctx.Err(); err != nil {
			return errors.Join(append(errs, err)...)
		}
	}
	return errors.Join(errs...)
}

// Pin toggles the orthogonal pinned flag on the workspace's canonical
// lane record. Spec §3.2: this does not change Status.
func (t *localTransport) Pin(_ context.Context, laneID string, pinned bool) error {
	if t.ws == nil {
		return errors.New("lanes: local transport unbound (workspace nil)")
	}
	l, ok := t.ws.GetLane(laneID)
	if !ok {
		return fmt.Errorf("lanes: pin: unknown lane %q", laneID)
	}
	l.SetPinned(pinned)
	return nil
}

// snapshotFromCortex copies the cortex Lane fields into a LaneSnapshot.
// The TUI carries fewer fields than the cortex Lane (no ParentID,
// LobeName, etc.) — those are not surfaced in the panel render.
func snapshotFromCortex(l *cortex.Lane) LaneSnapshot {
	if l == nil {
		return LaneSnapshot{}
	}
	c := l.Clone()
	return LaneSnapshot{
		ID:        c.ID,
		Title:     c.Label,
		Role:      string(c.Kind),
		Status:    StatusFromHub(c.Status),
		StartedAt: c.StartedAt,
		EndedAt:   c.EndedAt,
	}
}

// translateHubLaneEvent maps a hub.Event with Lane payload to a
// transport LaneEvent. Returns zero-Kind LaneEvent (= drop) for events
// that don't have a corresponding panel reaction.
func translateHubLaneEvent(ws *cortex.Workspace, ev *hub.Event) LaneEvent {
	if ev == nil || ev.Lane == nil {
		return LaneEvent{}
	}
	laneID := ev.Lane.LaneID
	// Pull the canonical lane (best effort) so the snapshot carries
	// up-to-date status / label / cost.
	var snap LaneSnapshot
	if ws != nil {
		if l, ok := ws.GetLane(laneID); ok {
			snap = snapshotFromCortex(l)
		}
	}
	if snap.ID == "" {
		// Fall back to the event's own fields when the lane
		// disappeared mid-event (rare; lane.killed for a terminal
		// lane).
		snap.ID = laneID
		snap.Title = ev.Lane.Label
		if ev.Lane.StartedAt != nil {
			snap.StartedAt = *ev.Lane.StartedAt
		}
		if ev.Lane.EndedAt != nil {
			snap.EndedAt = *ev.Lane.EndedAt
		}
		snap.Status = StatusFromHub(ev.Lane.Status)
	}

	switch ev.Type {
	case hub.EventLaneCreated:
		return LaneEvent{Kind: "start", Snapshot: snap}
	case hub.EventLaneStatus:
		// Terminal status → end; non-terminal → tick.
		if ev.Lane.Status.IsTerminal() {
			return LaneEvent{Kind: "end", Snapshot: snap}
		}
		return LaneEvent{Kind: "tick", Snapshot: snap}
	case hub.EventLaneDelta:
		// Pull latest activity text from the content block, if any.
		if ev.Lane.Block != nil && ev.Lane.Block.Text != "" {
			snap.Activity = ev.Lane.Block.Text
		} else if ev.Lane.Block != nil && ev.Lane.Block.Thinking != "" {
			snap.Activity = ev.Lane.Block.Thinking
		}
		return LaneEvent{Kind: "tick", Snapshot: snap}
	case hub.EventLaneCost:
		snap.Tokens = ev.Lane.TokensIn + ev.Lane.TokensOut
		snap.CostUSD = ev.Lane.CumulativeUSD
		return LaneEvent{Kind: "tick", Snapshot: snap}
	case hub.EventLaneNote:
		snap.Activity = ev.Lane.NoteSummary
		return LaneEvent{Kind: "tick", Snapshot: snap}
	case hub.EventLaneKilled:
		snap.Status = StatusCancelled
		return LaneEvent{Kind: "end", Snapshot: snap}
	default:
		return LaneEvent{}
	}
}

// StatusFromHub converts the wire-format hub.LaneStatus string enum to
// the panel's int8 LaneStatus. Unknown values map to StatusPending so
// the panel never blocks on a bad enum value (defensive — the cortex
// validator already gates ingress).
func StatusFromHub(s hub.LaneStatus) LaneStatus {
	switch s {
	case hub.LaneStatusPending:
		return StatusPending
	case hub.LaneStatusRunning:
		return StatusRunning
	case hub.LaneStatusBlocked:
		return StatusBlocked
	case hub.LaneStatusDone:
		return StatusDone
	case hub.LaneStatusErrored:
		return StatusErrored
	case hub.LaneStatusCancelled:
		return StatusCancelled
	default:
		return StatusPending
	}
}

// StatusToHub is the inverse of StatusFromHub.
func StatusToHub(s LaneStatus) hub.LaneStatus {
	switch s {
	case StatusPending:
		return hub.LaneStatusPending
	case StatusRunning:
		return hub.LaneStatusRunning
	case StatusBlocked:
		return hub.LaneStatusBlocked
	case StatusDone:
		return hub.LaneStatusDone
	case StatusErrored:
		return hub.LaneStatusErrored
	case StatusCancelled:
		return hub.LaneStatusCancelled
	default:
		return hub.LaneStatusPending
	}
}

// --- remoteTransport ---
//
// The WebSocket transport for connecting a TUI to a running r1d
// daemon. Per spec §"Subscription Wiring" remote-mode bullets:
//   - dial ws://127.0.0.1:<port>
//   - bearer token via Sec-WebSocket-Protocol: ["r1.bearer", token]
//   - reconnect with Last-Event-ID on disconnect
//
// The r1d server endpoints (/v1/lanes/subscribe, /v1/lanes/kill, ...)
// are owned by the r1d-server build phase (specs/r1d-server.md). Until
// that phase ships, this transport returns an explicit "not connected"
// error from every method so the embedded mode (localTransport) is the
// only working path. The structure is in place so the wsTransport can
// be wired up by populating Dial without changing the panel surface.
type remoteTransport struct {
	addr  string // ws://host:port
	token string // bearer

	mu      sync.Mutex
	lastSeq uint64 // for Last-Event-ID on reconnect
}

// NewRemoteTransport constructs a Transport that dials r1d at addr.
// Until the r1d-server phase ships, every method returns
// errRemoteNotImplemented. The constructor exists so the
// runner.go --lanes flag has a stable type to instantiate against.
func NewRemoteTransport(addr, bearerToken string) Transport {
	return &remoteTransport{addr: addr, token: bearerToken}
}

// errRemoteNotImplemented is returned by every remoteTransport method
// until the r1d-server phase ships the matching endpoints. Surfaces
// (the producer goroutine) treat this as a recoverable
// configuration error: the panel renders empty + a helpful status bar
// hint rather than crashing.
var errRemoteNotImplemented = errors.New("lanes: remote transport not yet wired (pending r1d-server phase)")

func (t *remoteTransport) List(_ context.Context) ([]LaneSnapshot, error) {
	return nil, errRemoteNotImplemented
}

func (t *remoteTransport) Subscribe(ctx context.Context, _ string, _ chan<- LaneEvent) error {
	// Block until ctx cancels so the producer doesn't busy-loop on
	// the immediate error return.
	<-ctx.Done()
	return errRemoteNotImplemented
}

func (t *remoteTransport) Kill(_ context.Context, _ string) error {
	return errRemoteNotImplemented
}

func (t *remoteTransport) KillAll(_ context.Context) error {
	return errRemoteNotImplemented
}

func (t *remoteTransport) Pin(_ context.Context, _ string, _ bool) error {
	return errRemoteNotImplemented
}
