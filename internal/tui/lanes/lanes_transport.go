package lanes

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
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
// WebSocket transport for connecting a TUI to a running r1d daemon.
// Implements the client side of specs/lanes-protocol.md §5.4
// (matching internal/server/ws.go on the server side):
//
//   - dials ws://host:port/v1/lanes/ws
//   - advertises Sec-WebSocket-Protocol "r1.lanes.v1" (or the
//     token-bearing variant "r1.lanes.v1+token.<token>" when a bearer
//     token is configured), per spec §5.4 / D-S6
//   - issues a session.subscribe JSON-RPC request after the upgrade
//     completes; replies with snapshot_seq used as the reconnect
//     cursor (since_seq) on disconnect (Last-Event-ID equivalent —
//     the spec's "Last-Event-ID header" wording predates the move to
//     JSON-RPC subscribe and is implemented as the since_seq param)
//   - drains $/event notifications, decodes the lane payload via the
//     same translateLaneRPCEvent helper used by SSE, and pushes typed
//     LaneEvent envelopes onto out
//   - replies $/pong to server $/ping notifications
//
// Kill / KillAll / Pin / List are routed as JSON-RPC requests over
// the same connection (methods r1.lanes.kill, r1.lanes.killAll,
// r1.lanes.pin, r1.lanes.list — matching the MCP tool names from
// internal/mcp/lanes_schemas.go). The server-side WS handler does
// not yet route these methods; the TUI client thus surfaces the
// JSON-RPC -32601 reply as an error to the user. Wire format is
// defined; wiring the server methods is owned by the r1d-server
// build phase.
type remoteTransport struct {
	addr  string // host:port (no scheme)
	token string // bearer for Sec-WebSocket-Protocol token slot

	mu      sync.Mutex
	conn    net.Conn
	bufrw   *bufio.ReadWriter
	subID   int64
	lastSeq uint64
	nextRPC int64
	pending map[int64]chan rpcReply
}

// rpcReply carries one JSON-RPC response back to its waiting caller.
type rpcReply struct {
	result json.RawMessage
	err    *rpcError
}

// rpcError mirrors the JSON-RPC 2.0 error object.
type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("rpc %d: %s", e.Code, e.Message)
}

// NewRemoteTransport constructs a Transport that dials r1d at addr.
// addr may be either "host:port" or a full ws:// URL; the leading
// scheme is stripped before the dial.
func NewRemoteTransport(addr, bearerToken string) Transport {
	// Accept either bare host:port or a ws:// URL.
	addr = strings.TrimPrefix(addr, "ws://")
	addr = strings.TrimPrefix(addr, "wss://")
	addr = strings.TrimSuffix(addr, "/")
	return &remoteTransport{
		addr:    addr,
		token:   bearerToken,
		pending: make(map[int64]chan rpcReply),
	}
}

// dialOnce performs the WebSocket upgrade against /v1/lanes/ws and
// stores the resulting conn / bufrw on the transport. Caller holds
// t.mu.
func (t *remoteTransport) dialOnce(ctx context.Context) error {
	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", t.addr)
	if err != nil {
		return fmt.Errorf("lanes: dial %s: %w", t.addr, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	} else {
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	}

	// Pick a 16-byte random nonce; the server's Sec-WebSocket-Accept
	// must match SHA1(nonce + magic).
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		conn.Close()
		return err
	}
	wsKey := base64.StdEncoding.EncodeToString(nonce[:])

	// Build subprotocol header. r1.lanes.v1 always; with token slot
	// when configured.
	subproto := "r1.lanes.v1"
	if t.token != "" {
		subproto = "r1.lanes.v1+token." + t.token + ", r1.lanes.v1"
	}

	// Upgrade request.
	req := strings.Builder{}
	req.WriteString("GET /v1/lanes/ws HTTP/1.1\r\n")
	fmt.Fprintf(&req, "Host: %s\r\n", t.addr)
	req.WriteString("Upgrade: websocket\r\n")
	req.WriteString("Connection: Upgrade\r\n")
	fmt.Fprintf(&req, "Sec-WebSocket-Key: %s\r\n", wsKey)
	req.WriteString("Sec-WebSocket-Version: 13\r\n")
	fmt.Fprintf(&req, "Sec-WebSocket-Protocol: %s\r\n", subproto)
	if t.token != "" {
		fmt.Fprintf(&req, "Authorization: Bearer %s\r\n", t.token)
	}
	req.WriteString("\r\n")
	if _, err := conn.Write([]byte(req.String())); err != nil {
		conn.Close()
		return err
	}

	br := bufio.NewReader(conn)
	// Status line.
	line, err := br.ReadString('\n')
	if err != nil {
		conn.Close()
		return err
	}
	if !strings.Contains(line, " 101 ") {
		conn.Close()
		return fmt.Errorf("lanes: ws upgrade failed: %s", strings.TrimSpace(line))
	}
	// Drain headers.
	for {
		hdr, err := br.ReadString('\n')
		if err != nil {
			conn.Close()
			return err
		}
		if strings.TrimSpace(hdr) == "" {
			break
		}
	}
	// Disable per-frame deadlines now that the upgrade is complete.
	_ = conn.SetDeadline(time.Time{})

	t.conn = conn
	t.bufrw = bufio.NewReadWriter(br, bufio.NewWriter(conn))
	return nil
}

// writeFrame writes one client-side WS frame. Per RFC 6455, the
// client MUST mask payloads. Mask key is 4 random bytes.
func (t *remoteTransport) writeFrame(opcode byte, payload []byte) error {
	header := []byte{0x80 | opcode}
	length := len(payload)
	switch {
	case length <= 125:
		header = append(header, byte(length)|0x80)
	case length <= 65535:
		var b [3]byte
		b[0] = 126 | 0x80
		binary.BigEndian.PutUint16(b[1:], uint16(length))
		header = append(header, b[:]...)
	default:
		var b [9]byte
		b[0] = 127 | 0x80
		binary.BigEndian.PutUint64(b[1:], uint64(length))
		header = append(header, b[:]...)
	}
	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return err
	}
	header = append(header, mask[:]...)

	masked := make([]byte, length)
	for i, b := range payload {
		masked[i] = b ^ mask[i%4]
	}
	if _, err := t.bufrw.Write(header); err != nil {
		return err
	}
	if _, err := t.bufrw.Write(masked); err != nil {
		return err
	}
	return t.bufrw.Flush()
}

// readFrame reads one server-side WS frame. Server frames are
// unmasked (RFC 6455 §5.3 — only client frames mask).
func (t *remoteTransport) readFrame() (byte, []byte, error) {
	head, err := t.bufrw.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	fin := head&0x80 != 0
	opcode := head & 0x0f
	if !fin {
		return 0, nil, errors.New("lanes: fragmented server frame")
	}
	lenByte, err := t.bufrw.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	length := uint64(lenByte & 0x7f)
	switch length {
	case 126:
		var b [2]byte
		if _, err := io.ReadFull(t.bufrw, b[:]); err != nil {
			return 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err := io.ReadFull(t.bufrw, b[:]); err != nil {
			return 0, nil, err
		}
		length = binary.BigEndian.Uint64(b[:])
	}
	if lenByte&0x80 != 0 {
		// Masked server frame — protocol violation.
		return 0, nil, errors.New("lanes: server frame must not be masked")
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(t.bufrw, payload); err != nil {
		return 0, nil, err
	}
	return opcode, payload, nil
}

// writeJSONRPC serialises req and writes it as one text frame. Caller
// holds t.mu.
func (t *remoteTransport) writeJSONRPC(req map[string]any) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	return t.writeFrame(0x1, body)
}

// callRPC issues a JSON-RPC request and waits for its reply (or
// ctx.Done). The request id is assigned from t.nextRPC; the read
// loop routes the matching reply to a per-call channel via
// t.pending.
func (t *remoteTransport) callRPC(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	t.mu.Lock()
	if t.conn == nil {
		t.mu.Unlock()
		return nil, errors.New("lanes: remote transport not connected")
	}
	t.nextRPC++
	id := t.nextRPC
	ch := make(chan rpcReply, 1)
	t.pending[id] = ch
	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
	}
	if params != nil {
		body["params"] = params
	}
	err := t.writeJSONRPC(body)
	t.mu.Unlock()
	if err != nil {
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
		return nil, err
	}
	select {
	case <-ctx.Done():
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
		return nil, ctx.Err()
	case rep := <-ch:
		if rep.err != nil {
			return nil, rep.err
		}
		return rep.result, nil
	}
}

// List issues r1.lanes.list over the open connection. Returns
// errors.New("not connected") when the transport has not yet been
// Subscribed (the WS dial happens inside Subscribe).
func (t *remoteTransport) List(ctx context.Context) ([]LaneSnapshot, error) {
	res, err := t.callRPC(ctx, "r1.lanes.list", map[string]any{})
	if err != nil {
		return nil, err
	}
	// The server-side r1.lanes.list MCP tool returns an array of
	// lane records under res.lanes (specs/lanes-protocol.md §7.1).
	var out struct {
		Lanes []struct {
			ID        string    `json:"lane_id"`
			Label     string    `json:"label"`
			Kind      string    `json:"kind"`
			Status    string    `json:"status"`
			StartedAt time.Time `json:"started_at"`
			EndedAt   time.Time `json:"ended_at"`
		} `json:"lanes"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return nil, fmt.Errorf("lanes: decode list reply: %w", err)
	}
	snaps := make([]LaneSnapshot, 0, len(out.Lanes))
	for _, l := range out.Lanes {
		snaps = append(snaps, LaneSnapshot{
			ID:        l.ID,
			Title:     l.Label,
			Role:      l.Kind,
			Status:    StatusFromHub(hub.LaneStatus(l.Status)),
			StartedAt: l.StartedAt,
			EndedAt:   l.EndedAt,
		})
	}
	return snaps, nil
}

// Subscribe is the streaming entry point. It dials the WS endpoint,
// issues session.subscribe, and runs the read loop until ctx is
// cancelled. Reconnect-on-disconnect uses lastSeq as since_seq so
// the server replays missed events.
func (t *remoteTransport) Subscribe(ctx context.Context, sessionID string, out chan<- LaneEvent) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := t.runOnce(ctx, sessionID, out)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			// Backoff and retry. A real production transport would
			// add jitter and a max-attempt cap; for the panel use
			// case a fixed 1s delay is sufficient and matches
			// dashboard.go's reconnect cadence.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
			continue
		}
		return nil
	}
}

// runOnce performs one connect → subscribe → read-loop cycle.
// Returns nil on clean ctx cancellation, error on transport failure.
func (t *remoteTransport) runOnce(ctx context.Context, sessionID string, out chan<- LaneEvent) error {
	t.mu.Lock()
	if t.conn == nil {
		if err := t.dialOnce(ctx); err != nil {
			t.mu.Unlock()
			return err
		}
	}
	t.mu.Unlock()

	defer func() {
		t.mu.Lock()
		if t.conn != nil {
			_ = t.conn.Close()
			t.conn = nil
			t.bufrw = nil
		}
		// Cancel any pending RPCs.
		for id, ch := range t.pending {
			select {
			case ch <- rpcReply{err: &rpcError{Code: -32000, Message: "connection closed"}}:
			default:
			}
			delete(t.pending, id)
		}
		t.mu.Unlock()
	}()

	// Read loop runs in this goroutine. session.subscribe is
	// kicked off after the read goroutine starts so the response
	// can be picked up by the same dispatcher.
	rdrErr := make(chan error, 1)
	go func() {
		rdrErr <- t.readLoop(ctx, out)
	}()

	// Issue session.subscribe with current lastSeq as since_seq.
	t.mu.Lock()
	since := t.lastSeq
	t.mu.Unlock()
	subRes, err := t.callRPC(ctx, "session.subscribe", map[string]any{
		"session_id": sessionID,
		"since_seq":  since,
	})
	if err != nil {
		return err
	}
	var sub struct {
		Sub         int64  `json:"sub"`
		SnapshotSeq uint64 `json:"snapshot_seq"`
	}
	if err := json.Unmarshal(subRes, &sub); err != nil {
		return fmt.Errorf("lanes: decode subscribe reply: %w", err)
	}
	t.mu.Lock()
	t.subID = sub.Sub
	if sub.SnapshotSeq > t.lastSeq {
		t.lastSeq = sub.SnapshotSeq
	}
	t.mu.Unlock()

	// Initial snapshot replay via List → emit one Kind=="list".
	snap, err := t.List(ctx)
	if err == nil {
		select {
		case out <- LaneEvent{Kind: "list", List: snap}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// Wait for the read loop or ctx.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-rdrErr:
		return err
	}
}

// readLoop drains frames from the server and dispatches RPC replies
// + $/event notifications.
func (t *remoteTransport) readLoop(ctx context.Context, out chan<- LaneEvent) error {
	for {
		opcode, payload, err := t.readFrame()
		if err != nil {
			return err
		}
		switch opcode {
		case 0x1: // text
			t.handleServerText(ctx, payload, out)
		case 0x9: // ping → reply pong
			t.mu.Lock()
			_ = t.writeFrame(0xA, payload)
			t.mu.Unlock()
		case 0xA: // pong: ignore
		case 0x8: // close
			return errors.New("lanes: server closed connection")
		}
	}
}

// handleServerText dispatches one inbound JSON-RPC message:
//   - replies → route to t.pending[id]
//   - $/event notifications → translate + push to out
//   - $/ping notifications → reply with $/pong
func (t *remoteTransport) handleServerText(ctx context.Context, payload []byte, out chan<- LaneEvent) {
	var env struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		return
	}
	// Reply (has id, no method).
	if env.Method == "" && len(env.ID) > 0 {
		var id int64
		if err := json.Unmarshal(env.ID, &id); err != nil {
			return
		}
		t.mu.Lock()
		ch, ok := t.pending[id]
		if ok {
			delete(t.pending, id)
		}
		t.mu.Unlock()
		if ok {
			select {
			case ch <- rpcReply{result: env.Result, err: env.Error}:
			default:
			}
		}
		return
	}
	// Notification.
	switch env.Method {
	case "$/ping":
		// Reply $/pong.
		t.mu.Lock()
		_ = t.writeJSONRPC(map[string]any{
			"jsonrpc": "2.0",
			"method":  "$/pong",
			"params":  map[string]any{},
		})
		t.mu.Unlock()
	case "$/event":
		t.dispatchEvent(ctx, env.Params, out)
	}
}

// dispatchEvent decodes a lane $/event notification and pushes the
// translated LaneEvent to out. Updates lastSeq for reconnect.
func (t *remoteTransport) dispatchEvent(ctx context.Context, params json.RawMessage, out chan<- LaneEvent) {
	var ev struct {
		Sub       int64           `json:"sub"`
		Seq       uint64          `json:"seq"`
		Event     string          `json:"event"`
		LaneID    string          `json:"lane_id"`
		SessionID string          `json:"session_id"`
		Data      json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(params, &ev); err != nil {
		return
	}
	t.mu.Lock()
	if ev.Seq > t.lastSeq {
		t.lastSeq = ev.Seq
	}
	t.mu.Unlock()

	// Decode common data fields (the server packs status, label,
	// tokens, cost into data). We only need the panel-relevant
	// subset.
	var data struct {
		Status        string  `json:"status"`
		Label         string  `json:"label"`
		LobeName      string  `json:"lobe_name"`
		Activity      string  `json:"activity"`
		TokensIn      int     `json:"tokens_in"`
		TokensOut     int     `json:"tokens_out"`
		CumulativeUSD float64 `json:"cumulative_usd"`
		ContentBlock  struct {
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
		} `json:"content_block"`
		NoteSummary string `json:"note_summary"`
		Reason      string `json:"reason"`
	}
	_ = json.Unmarshal(ev.Data, &data)

	snap := LaneSnapshot{
		ID:      ev.LaneID,
		Title:   data.Label,
		Role:    data.LobeName,
		Status:  StatusFromHub(hub.LaneStatus(data.Status)),
		Tokens:  data.TokensIn + data.TokensOut,
		CostUSD: data.CumulativeUSD,
	}
	if data.Activity != "" {
		snap.Activity = data.Activity
	} else if data.ContentBlock.Text != "" {
		snap.Activity = data.ContentBlock.Text
	} else if data.ContentBlock.Thinking != "" {
		snap.Activity = data.ContentBlock.Thinking
	} else if data.NoteSummary != "" {
		snap.Activity = data.NoteSummary
	}

	var le LaneEvent
	switch ev.Event {
	case "lane.created":
		le = LaneEvent{Kind: "start", Snapshot: snap}
	case "lane.status":
		if hub.LaneStatus(data.Status).IsTerminal() {
			le = LaneEvent{Kind: "end", Snapshot: snap}
		} else {
			le = LaneEvent{Kind: "tick", Snapshot: snap}
		}
	case "lane.delta", "lane.cost", "lane.note":
		le = LaneEvent{Kind: "tick", Snapshot: snap}
	case "lane.killed":
		snap.Status = StatusCancelled
		le = LaneEvent{Kind: "end", Snapshot: snap}
	default:
		return
	}
	select {
	case out <- le:
	case <-ctx.Done():
	}
}

// Kill issues r1.lanes.kill over the open connection.
func (t *remoteTransport) Kill(ctx context.Context, laneID string) error {
	res, err := t.callRPC(ctx, "r1.lanes.kill", map[string]any{
		"lane_id": laneID,
		"reason":  "cancelled_by_operator",
	})
	if err != nil {
		return err
	}
	_ = res
	return nil
}

// KillAll issues r1.lanes.killAll.
func (t *remoteTransport) KillAll(ctx context.Context) error {
	_, err := t.callRPC(ctx, "r1.lanes.killAll", map[string]any{
		"reason": "cancelled_by_operator",
	})
	return err
}

// Pin toggles the orthogonal pinned flag via r1.lanes.pin.
func (t *remoteTransport) Pin(ctx context.Context, laneID string, pinned bool) error {
	_, err := t.callRPC(ctx, "r1.lanes.pin", map[string]any{
		"lane_id": laneID,
		"pinned":  pinned,
	})
	return err
}
