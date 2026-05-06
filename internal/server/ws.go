// Package server — lanes-protocol WebSocket handler (TASK-14 + TASK-15).
//
// Implements specs/lanes-protocol.md §5.4 (WebSocket subprotocol):
//
//   - subprotocol token "r1.lanes.v1" is required on Sec-WebSocket-Protocol;
//     absence closes with code 4401 ("unauthorized");
//   - the token-bearing variant "r1.lanes.v1+token.<token>" carries the
//     bearer token via the subprotocol slot (CSWSH defense per D-S6);
//   - Origin must match a configured allowlist or be loopback;
//   - server emits $/ping every 15 s; idle-without-traffic for 30 s closes
//     with code 4408.
//
// Implemented over net/http.Hijacker (not coder/websocket). The codebase
// already ships two RFC 6455 implementations (internal/server/dashboard.go
// handleWebSocket and internal/beacon/transport/transport.go ServeWS) so
// taking on a third-party dependency is unnecessary for the small subset
// of the protocol we need (text frames, ping/pong control frames, server
// close codes). Behaviour is verified by the unit tests in ws_test.go.
//
// JSON-RPC 2.0 framing for session.subscribe (TASK-15) is implemented in
// the same file because the verb is intrinsic to the WS transport per
// spec §5.4 / §6.2 — separating them would split a single response
// pipeline across files for no benefit.
package server

import (
	"bufio"
	"context"
	"crypto/sha1" // #nosec G505 -- mandated by RFC 6455 Sec-WebSocket-Accept; not crypto
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/RelayOne/r1/internal/hub"
)

// LanesSubprotocol is the required Sec-WebSocket-Protocol token (spec §5.4).
const LanesSubprotocol = "r1.lanes.v1"

// LanesSubprotocolTokenPrefix is the bearer-carrying variant. The full
// offered subprotocol value is `r1.lanes.v1+token.<token>`.
const LanesSubprotocolTokenPrefix = "r1.lanes.v1+token."

// WS close codes per spec §5.4.
const (
	wsCloseUnauthorized = 4401 // missing/wrong subprotocol or token
	wsCloseProtoErr     = 4400 // binary frame, malformed JSON, etc.
	wsCloseIdleTimeout  = 4408 // 30s without inbound traffic
	wsCloseWALTruncated = 4404 // since_seq predates retention window
)

// WS heartbeat / idle policy per spec §5.4.
const (
	wsPingInterval = 15 * time.Second
	wsIdleTimeout  = 30 * time.Second
)

// handleLaneWS is the WebSocket entry point for the lanes protocol. The
// upgrade is performed inline (RFC 6455) and the loop runs until the
// client disconnects, the idle timer fires, or the request context is
// cancelled.
//
// Auth model:
//
//   - if s.Token == "" no auth is enforced (development mode);
//   - otherwise the client MUST advertise the subprotocol
//     "r1.lanes.v1+token.<s.Token>" or send Authorization: Bearer <token>
//     on the upgrade request.
//
// The chosen subprotocol echoed back to the client is always the bare
// "r1.lanes.v1" (the token slot is consumed during the handshake and
// never re-emitted).
func (s *Server) handleLaneWS(w http.ResponseWriter, r *http.Request) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "expected websocket upgrade", http.StatusBadRequest)
		return
	}

	// Subprotocol negotiation. Sec-WebSocket-Protocol may carry a
	// comma-separated list; we accept either the bare token or the
	// token-bearing variant.
	offered := parseSubprotocols(r.Header.Get("Sec-WebSocket-Protocol"))
	clientToken, hasBare := selectLanesSubprotocol(offered)
	if !hasBare {
		http.Error(w, "missing r1.lanes.v1 subprotocol", http.StatusUnauthorized)
		return
	}

	// Token validation. Bearer header takes precedence over the
	// subprotocol-embedded token because Authorization is the standard
	// auth header and the subprotocol token only exists for browser
	// clients that cannot set custom headers on the upgrade.
	if s.Token != "" {
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if bearer != s.Token && clientToken != s.Token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	// Origin pinning. Loopback is always allowed; otherwise the Origin
	// must match s.AllowedOrigins exactly. Empty Origin (non-browser
	// client like Go's http.Client) is allowed because it cannot mount
	// CSWSH attacks.
	if origin := r.Header.Get("Origin"); origin != "" {
		if !isOriginAllowed(origin, s.AllowedOrigins) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "websocket unsupported", http.StatusInternalServerError)
		return
	}
	conn, bufrw, err := hj.Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer conn.Close()

	acceptKey := computeWSAcceptKey(r.Header.Get("Sec-WebSocket-Key"))
	bufrw.WriteString("HTTP/1.1 101 Switching Protocols\r\n")
	bufrw.WriteString("Upgrade: websocket\r\n")
	bufrw.WriteString("Connection: Upgrade\r\n")
	bufrw.WriteString("Sec-WebSocket-Accept: " + acceptKey + "\r\n")
	bufrw.WriteString("Sec-WebSocket-Protocol: " + LanesSubprotocol + "\r\n")
	bufrw.WriteString("X-R1-Lanes-Version: " + LanesProtocolVersion + "\r\n")
	bufrw.WriteString("\r\n")
	if err := bufrw.Flush(); err != nil {
		return
	}

	// Run the per-connection loop. All further IO goes through the
	// laneWSConn wrapper so the read/write paths share one mutex.
	c := newLaneWSConn(conn, bufrw, s)
	c.run(r.Context())
}

// parseSubprotocols splits the Sec-WebSocket-Protocol header into a
// trimmed list of offered values. Empty input returns nil.
func parseSubprotocols(h string) []string {
	if h == "" {
		return nil
	}
	parts := strings.Split(h, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// selectLanesSubprotocol scans offered for either the bare lanes token
// or the token-bearing variant. Returns (token, true) if either form
// was present; token is "" when only the bare token was offered.
func selectLanesSubprotocol(offered []string) (string, bool) {
	var token string
	found := false
	for _, p := range offered {
		switch {
		case p == LanesSubprotocol:
			found = true
		case strings.HasPrefix(p, LanesSubprotocolTokenPrefix):
			found = true
			token = strings.TrimPrefix(p, LanesSubprotocolTokenPrefix)
		}
	}
	return token, found
}

// isOriginAllowed pins the WS upgrade Origin to loopback or one of the
// configured allowlist entries. Returns true on match. Loopback is always
// trusted because a CSWSH-style attack from a remote origin cannot reach
// 127.0.0.1 / localhost in a same-origin browser without explicit user
// configuration.
func isOriginAllowed(origin string, allow []string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	host := u.Hostname()
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}
	for _, a := range allow {
		if origin == a {
			return true
		}
	}
	return false
}

// computeWSAcceptKey computes the Sec-WebSocket-Accept value per RFC 6455.
func computeWSAcceptKey(clientKey string) string {
	const magic = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	h := sha1.New() // #nosec G401 -- RFC 6455 mandates SHA1 for handshake; not used as crypto.
	io.WriteString(h, clientKey+magic)
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// laneWSConn carries one WebSocket connection's state. Frame reads run on
// one goroutine and frame writes are serialised through writeMu so the
// ping ticker and the live-event publisher can both write safely.
type laneWSConn struct {
	conn   net.Conn
	bufrw  *bufio.ReadWriter
	srv    *Server
	writeMu sync.Mutex

	// subscriptions[subID] -> sessionID. Created by session.subscribe
	// JSON-RPC requests (TASK-15); destroyed when the connection closes
	// or session.unsubscribe is received. Accessed only on the read
	// goroutine so no mutex is needed.
	subscriptions map[int64]*laneWSSubscription
	nextSubID     int64

	// lastRead is updated on every inbound frame for the idle-timeout
	// check. Written under conn deadlines so no atomic is needed.
	lastRead time.Time
}

// laneWSSubscription is one active session.subscribe attached to this
// connection. Holds the hub subscriber id (so we can Unregister on close)
// and the goroutine cancellation handle.
type laneWSSubscription struct {
	sessionID  string
	hubSubID   string
	cancel     context.CancelFunc
}

func newLaneWSConn(conn net.Conn, bufrw *bufio.ReadWriter, srv *Server) *laneWSConn {
	return &laneWSConn{
		conn:          conn,
		bufrw:         bufrw,
		srv:           srv,
		subscriptions: make(map[int64]*laneWSSubscription),
		lastRead:      time.Now(),
	}
}

// run is the per-connection main loop. It launches a ping ticker, a
// reader, and forwards any subscriptions to the hub. Returns when the
// connection closes, the idle timer fires, or the request context is
// cancelled.
func (c *laneWSConn) run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer c.cleanupSubscriptions()

	// Ping ticker. Per spec §5.4 the heartbeat is a JSON-RPC $/ping
	// notification (not a low-level WS ping frame); the client SHOULD
	// reply with $/pong but we do not require it. The idle timeout is
	// independently enforced via SetReadDeadline.
	pingTicker := time.NewTicker(wsPingInterval)
	defer pingTicker.Stop()

	pingDone := make(chan struct{})
	go func() {
		defer close(pingDone)
		for {
			select {
			case <-ctx.Done():
				return
			case <-pingTicker.C:
				now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
				body, _ := json.Marshal(map[string]any{
					"jsonrpc": "2.0",
					"method":  "$/ping",
					"params":  map[string]any{"at": now},
				})
				if err := c.writeText(body); err != nil {
					return
				}
			}
		}
	}()

	// Reader loop. We re-arm the read deadline on every frame so
	// 30 s of silence (no $/pong, no client request) closes the
	// connection per spec §5.4.
	for {
		_ = c.conn.SetReadDeadline(time.Now().Add(wsIdleTimeout))
		opcode, payload, err := readWSFrameStrict(c.bufrw.Reader)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				c.writeClose(wsCloseIdleTimeout, "idle")
			}
			return
		}
		c.lastRead = time.Now()
		switch opcode {
		case wsOpcodeText:
			c.handleClientFrame(ctx, payload)
		case wsOpcodeBinary:
			// Spec §5.4: binary frames are reserved; close 4400.
			c.writeClose(wsCloseProtoErr, "binary frame")
			return
		case wsOpcodeClose:
			return
		case wsOpcodePing:
			// Echo low-level ping as pong (RFC 6455 §5.5.3).
			_ = c.writeFrame(wsOpcodePong, payload)
		case wsOpcodePong:
			// no-op; we use $/ping JSON-RPC for liveness.
		}
	}
}

// handleClientFrame parses one inbound text frame as a JSON-RPC request
// or notification and dispatches to the matching method. Unknown methods
// produce a JSON-RPC -32601 error reply.
func (c *laneWSConn) handleClientFrame(ctx context.Context, payload []byte) {
	var req struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		c.writeRPCError(nil, -32700, "parse error", nil)
		return
	}
	if req.JSONRPC != "2.0" {
		c.writeRPCError(req.ID, -32600, "invalid request", nil)
		return
	}
	switch req.Method {
	case "$/pong":
		// Client liveness reply; no response.
		return
	case "$/ping":
		// Client-initiated ping: reply with $/pong.
		now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
		_ = c.writeJSON(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  map[string]any{"at": now},
		})
	case "session.subscribe":
		c.handleSessionSubscribe(ctx, req.ID, req.Params)
	case "session.unsubscribe":
		c.handleSessionUnsubscribe(req.ID, req.Params)
	default:
		c.writeRPCError(req.ID, -32601, "method not found", nil)
	}
}

// handleSessionSubscribe implements TASK-15 / TASK-16 / TASK-17:
//
//   - registers a hub subscriber for the requested session_id;
//   - replies with {sub, snapshot_seq};
//   - emits the synthetic session.bound notification at seq=0 (TASK-17);
//   - replays from WAL since since_seq+1 (TASK-16) — on out-of-window
//     replies with JSON-RPC error -32004 carrying data.code="wal_truncated"
//     and closes the subscription;
//   - subsequently pushes $/event notifications for every live lane
//     event matching the subscription.
func (c *laneWSConn) handleSessionSubscribe(ctx context.Context, id, paramsRaw json.RawMessage) {
	var params struct {
		SessionID string `json:"session_id"`
		SinceSeq  uint64 `json:"since_seq"`
	}
	if len(paramsRaw) > 0 {
		if err := json.Unmarshal(paramsRaw, &params); err != nil {
			c.writeRPCError(id, -32602, "invalid params", nil)
			return
		}
	}
	if params.SessionID == "" {
		c.writeRPCError(id, -32602, "missing session_id", nil)
		return
	}
	if c.srv.Lanes == nil {
		c.writeRPCError(id, -32601, "lanes endpoint not configured", nil)
		return
	}

	// WAL replay first. We collect into a slice so we can short-circuit
	// on truncation before sending the success result — simplifies the
	// client-side error handling (no half-replayed stream).
	var replayed []*hub.Event
	if params.SinceSeq > 0 && c.srv.Lanes.WAL != nil {
		err := c.srv.Lanes.WAL.ReplayLane(ctx, params.SessionID, params.SinceSeq+1, func(ev *hub.Event) error {
			replayed = append(replayed, ev)
			return nil
		})
		if err != nil {
			if _, isTrunc := err.(*ErrWALTruncatedError); isTrunc {
				c.writeRPCError(id, -32004, "wal_truncated", map[string]any{
					"code":      "wal_truncated",
					"since_seq": params.SinceSeq,
				})
				c.writeClose(wsCloseWALTruncated, "wal_truncated")
				return
			}
			c.writeRPCError(id, -32603, "replay failed: "+err.Error(), nil)
			return
		}
	}

	// Allocate subscription id and register the hub subscriber. The
	// hub fan-out runs on its own goroutines; we hop through a buffered
	// channel so a wedged writer cannot back-pressure the bus.
	c.nextSubID++
	subID := c.nextSubID
	subCtx, subCancel := context.WithCancel(ctx)
	hubID := fmt.Sprintf("server.lanes.ws:%s:%d:%p", params.SessionID, subID, c)
	ch := make(chan *hub.Event, 256)
	if c.srv.Lanes.Hub != nil {
		c.srv.Lanes.Hub.Register(hub.Subscriber{
			ID: hubID,
			Events: []hub.EventType{
				hub.EventLaneCreated,
				hub.EventLaneStatus,
				hub.EventLaneDelta,
				hub.EventLaneCost,
				hub.EventLaneNote,
				hub.EventLaneKilled,
			},
			Mode:     hub.ModeObserve,
			Priority: 9300,
			Handler: func(_ context.Context, ev *hub.Event) *hub.HookResponse {
				if ev == nil || ev.Lane == nil || ev.Lane.SessionID != params.SessionID {
					return &hub.HookResponse{Decision: hub.Allow}
				}
				select {
				case ch <- ev:
				default:
				}
				return &hub.HookResponse{Decision: hub.Allow}
			},
		})
	}
	c.subscriptions[subID] = &laneWSSubscription{
		sessionID: params.SessionID,
		hubSubID:  hubID,
		cancel:    subCancel,
	}

	// Reply with subscription confirmation. snapshot_seq is the highest
	// seq we know of for this session at this moment; the client uses
	// it to detect gaps after reconnect. When no WAL is wired we report
	// 0 (the client then sees seq numbers monotonically increasing from
	// the first $/event).
	var snapshotSeq uint64
	for _, ev := range replayed {
		if ev.Lane != nil && ev.Lane.Seq > snapshotSeq {
			snapshotSeq = ev.Lane.Seq
		}
	}
	_ = c.writeJSON(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result": map[string]any{
			"sub":          subID,
			"snapshot_seq": snapshotSeq,
		},
	})

	// session.bound synthetic FIRST (TASK-17). The client uses this to
	// detect session-id mismatches before any lane events arrive.
	_ = c.writeJSON(map[string]any{
		"jsonrpc": "2.0",
		"method":  "$/event",
		"params": map[string]any{
			"sub":        subID,
			"seq":        0,
			"event":      "session.bound",
			"session_id": params.SessionID,
			"at":         time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00"),
			"data":       map[string]any{},
		},
	})

	// Replay batch (TASK-16).
	for _, ev := range replayed {
		_ = c.writeJSON(buildLaneRPCNotification(subID, ev))
	}

	// Live forwarder. One goroutine per subscription; cancelled when
	// the subscription is removed or the connection closes.
	go func() {
		defer close(ch)
		for {
			select {
			case <-subCtx.Done():
				return
			case ev, ok := <-ch:
				if !ok {
					return
				}
				_ = c.writeJSON(buildLaneRPCNotification(subID, ev))
			}
		}
	}()
}

// handleSessionUnsubscribe tears down one subscription.
func (c *laneWSConn) handleSessionUnsubscribe(id, paramsRaw json.RawMessage) {
	var params struct {
		Sub int64 `json:"sub"`
	}
	if len(paramsRaw) > 0 {
		_ = json.Unmarshal(paramsRaw, &params)
	}
	sub, ok := c.subscriptions[params.Sub]
	if !ok {
		c.writeRPCError(id, -32602, "unknown subscription", nil)
		return
	}
	if c.srv.Lanes != nil && c.srv.Lanes.Hub != nil {
		c.srv.Lanes.Hub.Unregister(sub.hubSubID)
	}
	sub.cancel()
	delete(c.subscriptions, params.Sub)
	_ = c.writeJSON(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  map[string]any{"ok": true},
	})
}

// cleanupSubscriptions tears down every subscription on connection close.
func (c *laneWSConn) cleanupSubscriptions() {
	for _, sub := range c.subscriptions {
		if c.srv.Lanes != nil && c.srv.Lanes.Hub != nil {
			c.srv.Lanes.Hub.Unregister(sub.hubSubID)
		}
		sub.cancel()
	}
	c.subscriptions = nil
}

// buildLaneRPCNotification formats one lane event as a JSON-RPC $/event
// notification per spec §5.2.
func buildLaneRPCNotification(subID int64, ev *hub.Event) map[string]any {
	if ev == nil || ev.Lane == nil {
		return nil
	}
	at := ""
	if !ev.Timestamp.IsZero() {
		at = ev.Timestamp.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
	}
	return map[string]any{
		"jsonrpc": "2.0",
		"method":  "$/event",
		"params": map[string]any{
			"sub":        subID,
			"seq":        ev.Lane.Seq,
			"event":      string(ev.Type),
			"event_id":   ev.ID,
			"session_id": ev.Lane.SessionID,
			"lane_id":    ev.Lane.LaneID,
			"at":         at,
			"data":       buildLaneRPCData(ev),
		},
	}
}

// writeRPCError writes a JSON-RPC error reply.
func (c *laneWSConn) writeRPCError(id json.RawMessage, code int, message string, data any) {
	errObj := map[string]any{
		"code":    code,
		"message": message,
	}
	if data != nil {
		errObj["data"] = data
	}
	_ = c.writeJSON(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   errObj,
	})
}

// writeJSON serialises v and writes it as a single text frame.
func (c *laneWSConn) writeJSON(v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.writeText(body)
}

// writeText writes one RFC 6455 text frame (opcode 0x1).
func (c *laneWSConn) writeText(payload []byte) error {
	return c.writeFrame(wsOpcodeText, payload)
}

// writeClose sends an RFC 6455 close frame with the given code and reason.
func (c *laneWSConn) writeClose(code uint16, reason string) {
	body := make([]byte, 2+len(reason))
	binary.BigEndian.PutUint16(body[:2], code)
	copy(body[2:], reason)
	_ = c.writeFrame(wsOpcodeClose, body)
}

// writeFrame writes one RFC 6455 frame with the given opcode.
const (
	wsOpcodeText   = 0x1
	wsOpcodeBinary = 0x2
	wsOpcodeClose  = 0x8
	wsOpcodePing   = 0x9
	wsOpcodePong   = 0xA
)

func (c *laneWSConn) writeFrame(opcode byte, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	header := []byte{0x80 | opcode}
	length := len(payload)
	switch {
	case length <= 125:
		header = append(header, byte(length))
	case length <= 65535:
		var buf [3]byte
		buf[0] = 126
		binary.BigEndian.PutUint16(buf[1:], uint16(length))
		header = append(header, buf[:]...)
	default:
		var buf [9]byte
		buf[0] = 127
		binary.BigEndian.PutUint64(buf[1:], uint64(length))
		header = append(header, buf[:]...)
	}
	if _, err := c.bufrw.Write(header); err != nil {
		return err
	}
	if _, err := c.bufrw.Write(payload); err != nil {
		return err
	}
	return c.bufrw.Flush()
}

// readWSFrameStrict reads one RFC 6455 frame and returns its opcode and
// (possibly unmasked) payload. Multi-frame fragmented messages are not
// supported (TASK-14 only carries small single-frame JSON-RPC messages);
// a fragmented frame returns an error.
func readWSFrameStrict(r *bufio.Reader) (byte, []byte, error) {
	head, err := r.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	fin := head&0x80 != 0
	opcode := head & 0x0f
	if !fin {
		return 0, nil, errors.New("ws: fragmented frames not supported")
	}
	lenByte, err := r.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	masked := lenByte&0x80 != 0
	length := uint64(lenByte & 0x7f)
	switch length {
	case 126:
		var buf [2]byte
		if _, err := io.ReadFull(r, buf[:]); err != nil {
			return 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(buf[:]))
	case 127:
		var buf [8]byte
		if _, err := io.ReadFull(r, buf[:]); err != nil {
			return 0, nil, err
		}
		length = binary.BigEndian.Uint64(buf[:])
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return opcode, payload, nil
}
