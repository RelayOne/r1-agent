// ws/webbridge.go — the web-chat typed-frame WebSocket bridge
// (audit A008; specs/web-chat-ui.md wire contract).
//
// The shipped web client (web/src/lib/api/r1d.ts + ws.ts) does NOT
// speak JSON-RPC: it sends raw typed frames —
//
//	{type:"subscribe",   sessionId, lastEventId?}
//	{type:"unsubscribe", sessionId}
//	{type:"chat",        sessionId, content}
//	{type:"interrupt",   sessionId}
//	{type:"ping"}
//
// — and expects flat server envelopes ({type:"lane.*"|"pong"|"error",
// seq, ts, ...} per web/src/lib/api/types.ts) back on the same
// socket. Before this file, no route on the daemon parsed those
// frames: /v1/rpc is JSON-RPC-only and /v1/lanes/ws dispatches only
// session.subscribe/unsubscribe, so the embedded SPA could never send
// a chat or receive an event.
//
// WebHandler is the missing server half. It authenticates exactly the
// way the client dials — WebSocket subprotocol ["r1.bearer", <token>]
// where <token> is either the daemon bearer or a short-lived ticket
// from POST /auth/ws-ticket — then translates:
//
//	{type:"chat"}        → DaemonSessionSend
//	{type:"interrupt"}   → DaemonSessionInterrupt (drop-partial)
//	{type:"subscribe"}   → SubscribeSessionWithSink (journal replay +
//	                       live fanout); events surface as flat
//	                       lane.created / lane.status / lane.delta /
//	                       lane.killed envelopes
//	{type:"unsubscribe"} → subscription cancel
//	{type:"ping"}        → {type:"pong"}
//
// Envelope `seq` for lane events is the per-subscription monotonic
// seq (the client's Last-Event-ID replay cursor); control envelopes
// (pong/error) use a per-connection counter.
package ws

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/RelayOne/r1/internal/hub"
	"github.com/RelayOne/r1/internal/server/jsonrpc"
	"github.com/RelayOne/r1/internal/stokerr"
)

// WebDaemonAPI is the slice of the daemon the web bridge drives.
// *jsonrpc.HubHandler satisfies it.
type WebDaemonAPI interface {
	DaemonSessionSend(ctx context.Context, req jsonrpc.SessionSendRequest) (jsonrpc.SessionSendResponse, error)
	DaemonSessionInterrupt(ctx context.Context, req jsonrpc.SessionInterruptRequest) (jsonrpc.SessionInterruptResponse, error)
	SubscribeSessionWithSink(ctx context.Context, sessionID string, sinceSeq uint64, filter []string, sink jsonrpc.EventSink) (func(), error)
}

// WebIdleTimeout is the read deadline for the web bridge. The client
// heartbeats {type:"ping"} after 30 s of silence, so 3 missed
// heartbeats mark the peer dead.
const WebIdleTimeout = 90 * time.Second

// WebHandler serves the typed-frame endpoint (mounted at /ws by the
// serve glue, matching the client default wsUrl).
type WebHandler struct {
	// API is the daemon surface. Required.
	API WebDaemonAPI

	// ValidateToken authorizes the subprotocol token slot. It should
	// accept the daemon bearer AND live ws-tickets. nil = no auth
	// (development mode), mirroring Handler.Token == "".
	ValidateToken func(token string) bool

	// IdleTimeout overrides WebIdleTimeout (testing only).
	IdleTimeout time.Duration
}

// webClientFrame is the union of every inbound typed frame
// (WsClientFrameSchema in web/src/lib/api/types.ts).
type webClientFrame struct {
	Type        string  `json:"type"`
	SessionID   string  `json:"sessionId"`
	Content     string  `json:"content"`
	LastEventID *uint64 `json:"lastEventId"`
}

// ServeHTTP upgrades and runs the typed-frame loop.
func (h *WebHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.API == nil {
		http.Error(w, "ws: web bridge not configured", http.StatusInternalServerError)
		return
	}
	subprotos := parseSubprotocols(r.Header.Get("Sec-WebSocket-Protocol"))
	if len(subprotos) < 1 || !strings.EqualFold(subprotos[0], Subprotocol) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="r1"`)
		http.Error(w, "ws: missing r1.bearer subprotocol", http.StatusUnauthorized)
		return
	}
	token := ""
	if len(subprotos) >= 2 {
		token = subprotos[1]
	}
	if hdr := r.Header.Get("Authorization"); strings.HasPrefix(hdr, "Bearer ") {
		token = strings.TrimSpace(strings.TrimPrefix(hdr, "Bearer "))
	}
	if h.ValidateToken != nil && !h.ValidateToken(token) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="r1"`)
		http.Error(w, "ws: invalid bearer token", http.StatusUnauthorized)
		return
	}

	wsConn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:       []string{Subprotocol},
		InsecureSkipVerify: true, // origin enforced by middleware (TASK-20)
	})
	if err != nil {
		return
	}
	defer func() { _ = wsConn.Close(websocket.StatusInternalError, "handler exit") }()

	conn := &Conn{WS: wsConn}
	h.serveWebLoop(r.Context(), conn)
}

// webConnState is the per-connection bridge state.
type webConnState struct {
	h    *WebHandler
	conn *Conn

	// ctrlSeq numbers control envelopes (pong/error).
	ctrlSeq atomic.Uint64

	// cancels maps sessionID → subscription cancel func. Accessed
	// only from the read goroutine.
	cancels map[string]func()
}

func (h *WebHandler) serveWebLoop(parent context.Context, conn *Conn) {
	idle := h.IdleTimeout
	if idle == 0 {
		idle = WebIdleTimeout
	}
	connCtx, cancel := context.WithCancel(parent)
	defer cancel()

	st := &webConnState{h: h, conn: conn, cancels: map[string]func(){}}
	defer func() {
		for _, c := range st.cancels {
			c()
		}
	}()

	for {
		readCtx, rcancel := context.WithTimeout(connCtx, idle)
		typ, data, err := conn.WS.Read(readCtx)
		rcancel()
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			_ = conn.WS.Close(4400, "binary frame not allowed")
			return
		}
		st.handleFrame(connCtx, data)
	}
}

// handleFrame routes one inbound typed frame.
func (st *webConnState) handleFrame(ctx context.Context, data []byte) {
	var frame webClientFrame
	if err := json.Unmarshal(data, &frame); err != nil {
		st.writeError(ctx, "", "INVALID_INPUT", "malformed frame: "+err.Error(), false)
		return
	}
	switch frame.Type {
	case "ping":
		st.writeEnvelope(ctx, map[string]any{
			"type": "pong",
			"seq":  st.ctrlSeq.Add(1),
			"ts":   time.Now().UTC().Format(time.RFC3339Nano),
		})
	case "chat":
		_, err := st.h.API.DaemonSessionSend(ctx, jsonrpc.SessionSendRequest{
			SessionID: frame.SessionID,
			Text:      frame.Content,
		})
		if err != nil {
			code, retryable := webErrorCode(err)
			st.writeError(ctx, frame.SessionID, code, err.Error(), retryable)
		}
	case "interrupt":
		_, err := st.h.API.DaemonSessionInterrupt(ctx, jsonrpc.SessionInterruptRequest{
			SessionID:   frame.SessionID,
			DropPartial: true,
		})
		if err != nil {
			code, retryable := webErrorCode(err)
			st.writeError(ctx, frame.SessionID, code, err.Error(), retryable)
		}
	case "subscribe":
		st.handleSubscribe(ctx, frame)
	case "unsubscribe":
		if c, ok := st.cancels[frame.SessionID]; ok {
			c()
			delete(st.cancels, frame.SessionID)
		}
	default:
		st.writeError(ctx, frame.SessionID, "INVALID_INPUT",
			"unknown frame type: "+frame.Type, false)
	}
}

// handleSubscribe (re)binds this connection to a session's event
// stream: journal replay from lastEventId, then live fanout. A repeat
// subscribe for the same session (the client's reconnect path)
// replaces the prior subscription.
func (st *webConnState) handleSubscribe(ctx context.Context, frame webClientFrame) {
	if frame.SessionID == "" {
		st.writeError(ctx, "", "INVALID_INPUT", "subscribe: sessionId is required", false)
		return
	}
	if prior, ok := st.cancels[frame.SessionID]; ok {
		prior()
		delete(st.cancels, frame.SessionID)
	}
	sinceSeq := uint64(0)
	if frame.LastEventID != nil {
		sinceSeq = *frame.LastEventID
	}
	sessionID := frame.SessionID
	conn := st.conn
	sink := func(sctx context.Context, ev *jsonrpc.SubscriptionEvent) error {
		env, ok := webEnvelopeFor(sessionID, ev)
		if !ok {
			// Event type the web contract does not model (cost ticks,
			// tool events, ...). Skipping keeps the closed zod union
			// on the client valid; the seq gap is harmless.
			return nil
		}
		b, err := json.Marshal(env)
		if err != nil {
			return nil
		}
		return conn.WriteRaw(sctx, b)
	}
	// No jsonrpc-level filter: filtering at the sink keeps the
	// per-subscription seq aligned with the journal cursor so
	// lastEventId resume replays exactly the missed records.
	cancelSub, err := st.h.API.SubscribeSessionWithSink(ctx, sessionID, sinceSeq, nil, sink)
	if err != nil {
		code, retryable := webErrorCode(err)
		st.writeError(ctx, sessionID, code, err.Error(), retryable)
		return
	}
	st.cancels[sessionID] = cancelSub
}

// writeEnvelope marshals and writes one flat server envelope.
func (st *webConnState) writeEnvelope(ctx context.Context, env map[string]any) {
	b, err := json.Marshal(env)
	if err != nil {
		return
	}
	_ = st.conn.WriteRaw(ctx, b)
}

// writeError emits an {type:"error"} envelope per ErrorEnvelopeSchema.
func (st *webConnState) writeError(ctx context.Context, sessionID, code, message string, retryable bool) {
	env := map[string]any{
		"type":      "error",
		"seq":       st.ctrlSeq.Add(1),
		"ts":        time.Now().UTC().Format(time.RFC3339Nano),
		"code":      code,
		"message":   message,
		"retryable": retryable,
	}
	if sessionID != "" {
		env["sessionId"] = sessionID
	}
	st.writeEnvelope(ctx, env)
}

// webErrorCode maps the stokerr taxonomy onto the client's
// R1dErrorCode enum (web/src/lib/api/types.ts).
func webErrorCode(err error) (code string, retryable bool) {
	var se *stokerr.Error
	if errors.As(err, &se) {
		switch se.Code {
		case stokerr.ErrValidation:
			return "INVALID_INPUT", false
		case stokerr.ErrNotFound:
			return "NOT_FOUND", false
		case stokerr.ErrConflict:
			// Conflict on send = inbox back-pressure; retry shortly.
			return "CONFLICT", true
		case stokerr.ErrPermission:
			return "FORBIDDEN", false
		case stokerr.ErrTimeout:
			return "TIMEOUT", true
		}
	}
	return "INTERNAL", false
}

// webEnvelopeFor translates one SubscriptionEvent (journal replay or
// live fanout; payload = hub.Event) into the flat envelope shape the
// web client validates. Returns ok=false for event types outside the
// web contract.
func webEnvelopeFor(sessionID string, ev *jsonrpc.SubscriptionEvent) (map[string]any, bool) {
	he, ok := coerceHubEvent(ev.Data)
	if !ok || he.Lane == nil {
		return nil, false
	}
	ts := he.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	base := map[string]any{
		"seq":       ev.Seq,
		"ts":        ts.UTC().Format(time.RFC3339Nano),
		"sessionId": sessionID,
	}
	switch hub.EventType(ev.Type) {
	case hub.EventLaneDelta:
		base["type"] = "lane.delta"
		base["laneId"] = he.Lane.LaneID
		base["data"] = laneDeltaText(he.Lane)
		return base, true
	case hub.EventLaneStatus:
		state, known := webLaneState(he.Lane.Status)
		if !known {
			return nil, false
		}
		base["type"] = "lane.status"
		base["laneId"] = he.Lane.LaneID
		base["state"] = state
		return base, true
	case hub.EventLaneCreated:
		state, known := webLaneState(he.Lane.Status)
		if !known {
			state = "queued"
		}
		created := ts
		if he.Lane.StartedAt != nil && !he.Lane.StartedAt.IsZero() {
			created = *he.Lane.StartedAt
		}
		label := he.Lane.Label
		if label == "" {
			label = he.Lane.LobeName
		}
		if label == "" {
			label = he.Lane.LaneID
		}
		base["type"] = "lane.created"
		base["lane"] = map[string]any{
			"id":         he.Lane.LaneID,
			"sessionId":  sessionID,
			"label":      label,
			"state":      state,
			"createdAt":  created.UTC().Format(time.RFC3339Nano),
			"updatedAt":  ts.UTC().Format(time.RFC3339Nano),
			"progress":   nil,
			"lastRender": nil,
			"lastSeq":    he.Lane.Seq,
		}
		return base, true
	case hub.EventLaneKilled:
		base["type"] = "lane.killed"
		base["laneId"] = he.Lane.LaneID
		if he.Lane.Reason != "" {
			base["reason"] = he.Lane.Reason
		} else {
			base["reason"] = nil
		}
		return base, true
	default:
		return nil, false
	}
}

// laneDeltaText flattens the streamed content block for the client's
// string-typed lane.delta data field.
func laneDeltaText(l *hub.LaneEvent) string {
	if l.Block == nil {
		return ""
	}
	switch {
	case l.Block.Text != "":
		return l.Block.Text
	case l.Block.Content != "":
		return l.Block.Content
	case l.Block.Thinking != "":
		return l.Block.Thinking
	default:
		return ""
	}
}

// webLaneState maps the lanes-protocol six-state FSM onto the web
// contract's LaneStateSchema enum.
func webLaneState(s hub.LaneStatus) (string, bool) {
	switch s {
	case hub.LaneStatusPending:
		return "queued", true
	case hub.LaneStatusRunning:
		return "running", true
	case hub.LaneStatusBlocked:
		return "waiting-tool", true
	case hub.LaneStatusDone:
		return "completed", true
	case hub.LaneStatusErrored:
		return "failed", true
	case hub.LaneStatusCancelled:
		return "killed", true
	default:
		return "", false
	}
}

// coerceHubEvent normalizes the SubscriptionEvent payload — *hub.Event
// on the live path, json.RawMessage on the journal-replay path — into
// a decoded *hub.Event.
func coerceHubEvent(data any) (*hub.Event, bool) {
	switch v := data.(type) {
	case *hub.Event:
		return v, v != nil
	case hub.Event:
		return &v, true
	case json.RawMessage:
		var ev hub.Event
		if err := json.Unmarshal(v, &ev); err != nil {
			return nil, false
		}
		return &ev, true
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, false
		}
		var ev hub.Event
		if err := json.Unmarshal(b, &ev); err != nil {
			return nil, false
		}
		return &ev, true
	}
}
