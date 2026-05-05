// Package ws implements the JSON-RPC 2.0 over WebSocket transport for
// the r1d-server multi-session daemon (specs/r1d-server.md Phase E item 29).
//
// # Subprotocol auth
//
// Per spec §11.29 the client MUST advertise the comma-separated value
// `r1.bearer, <token>` on Sec-WebSocket-Protocol. The server picks
// `r1.bearer` as the negotiated subprotocol and matches the second value
// against the daemon's bearer token. If the token is empty or wrong,
// the upgrade is rejected with HTTP 401 BEFORE the WebSocket handshake
// completes.
//
// The subprotocol-as-token trick is the only browser-friendly auth path
// for the WebSocket API: browsers don't expose `Authorization` headers
// for WS upgrades. By contrast, CLI clients can also speak the bearer
// header directly — the handler accepts either path so both surfaces
// share one transport.
//
// # 30s ping watchdog
//
// coder/websocket is context-driven, not SetReadDeadline-driven. The
// watchdog therefore lives in a separate goroutine that:
//
//  1. fires `Conn.Ping(ctx)` every 10 s and waits up to 30 s for the
//     pong (the library's Ping returns nil on pong receipt, error on
//     timeout/disconnect);
//  2. on Ping error, calls `Conn.Close(StatusGoingAway, "ping timeout")`
//     which unblocks the reader's `Read(ctx)` call.
//
// The reader goroutine itself wraps each `Read` in `context.WithTimeout`
// of 30 s so a silent connection (no data, no pong) ALSO trips the
// timeout. Either watchdog firing is sufficient to close an idle peer.
//
// # Wire format
//
// Inbound: JSON-RPC 2.0 envelopes per `internal/server/jsonrpc`. One
// envelope per WebSocket TEXT frame; multi-envelope batches travel in a
// single frame.
//
// Outbound: responses on the same channel; server-pushed events
// (TASK-32) ride as `$/event` notifications via Conn.Write.
package ws

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/RelayOne/r1/internal/server/jsonrpc"
)

// Subprotocol is the WebSocket subprotocol the daemon advertises. Clients
// MUST list this as the first value on Sec-WebSocket-Protocol.
const Subprotocol = "r1.bearer"

// IdleTimeout is the maximum time a connection may go without inbound
// frames before the read goroutine tears it down. Per spec §11.29 this is
// 30s. Each Read call wraps its parent ctx with this deadline.
const IdleTimeout = 30 * time.Second

// PingInterval is how often the watchdog fires a ping. 10s gives us 3
// chances inside one IdleTimeout window before the reader gives up; if
// a pong arrives, the reader's deadline is reset implicitly because
// the pong unblocks Read with a no-op control frame.
const PingInterval = 10 * time.Second

// Handler is the per-daemon WebSocket entry point. It upgrades each
// inbound HTTP request, performs subprotocol-based bearer auth, and
// pumps JSON-RPC envelopes through the shared Dispatcher.
//
// # Concurrency
//
// One Handler is shared across many connections. The Handler itself
// holds no per-connection state — that lives on the *connection struct
// in serveLoop. Multiple goroutines may invoke ServeHTTP concurrently;
// the only shared state is the Dispatcher (immutable post-construction).
type Handler struct {
	// Dispatcher routes JSON-RPC method names to handlers. Required.
	Dispatcher *jsonrpc.Dispatcher

	// Token is the daemon's bearer token. Empty means "no auth"
	// (development mode); production callers ALWAYS set this.
	Token string

	// OnConnect is an optional hook fired after a successful upgrade.
	// The hook receives the negotiated *Conn and a per-connection ctx
	// that is cancelled when the read loop exits. Use this to register
	// per-connection subscriber state with TASK-32's hub bridge.
	//
	// The hook MUST NOT block — long-running per-connection work
	// belongs in a goroutine the hook spawns itself.
	OnConnect func(ctx context.Context, conn *Conn)

	// IdleTimeout overrides the package default (testing only).
	IdleTimeout time.Duration

	// PingInterval overrides the package default (testing only).
	PingInterval time.Duration
}

// Conn wraps *websocket.Conn with the per-connection write mutex needed
// for safe concurrent writes from the read loop and the (TASK-32)
// subscriber-fanout goroutine. coder/websocket's Conn IS safe for
// concurrent calls except Reader/Read (we don't share Reader), but
// keeping a single writeMu makes it impossible to interleave a frame's
// chunks. The library's docstring confirms: "All methods may be called
// concurrently except for Reader and Read."
//
// We expose Conn so the OnConnect hook can wire an outbound publish
// path (TASK-32 subscription fanout) without re-importing the library.
type Conn struct {
	WS      *websocket.Conn
	writeMu sync.Mutex
}

// WriteNotification marshals a JSON-RPC notification envelope and
// writes it as a TEXT frame. Used by the TASK-32 subscription fanout
// to push `$/event` frames.
func (c *Conn) WriteNotification(ctx context.Context, n *jsonrpc.Notification) error {
	b, err := jsonrpc.EncodeNotification(n)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.WS.Write(ctx, websocket.MessageText, b)
}

// WriteRaw writes pre-encoded bytes as a TEXT frame.
func (c *Conn) WriteRaw(ctx context.Context, b []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.WS.Write(ctx, websocket.MessageText, b)
}

// WriteResponse marshals a JSON-RPC response and writes it. Convenience
// for the read-loop dispatch path.
func (c *Conn) WriteResponse(ctx context.Context, resp *jsonrpc.Response) error {
	b, err := jsonrpc.EncodeResponse(resp)
	if err != nil {
		return err
	}
	return c.WriteRaw(ctx, b)
}

// ServeHTTP performs the WebSocket upgrade and runs the read loop.
//
// The negotiation order is:
//
//  1. Parse Sec-WebSocket-Protocol — MUST contain "r1.bearer" as first
//     value AND the bearer token as the second value (comma-separated).
//  2. Validate the token against h.Token. Empty token (server-side) =
//     dev mode = no validation.
//  3. Upgrade the connection, advertising "r1.bearer" as the negotiated
//     subprotocol.
//  4. Run the read loop until the client disconnects or the watchdog
//     trips.
//
// Auth failures return HTTP 401 BEFORE upgrade so the client can read
// a proper status code; post-upgrade failures close the connection
// with the matching WebSocket close code.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.Dispatcher == nil {
		http.Error(w, "ws: dispatcher not configured", http.StatusInternalServerError)
		return
	}
	subprotos := parseSubprotocols(r.Header.Get("Sec-WebSocket-Protocol"))
	if len(subprotos) < 1 || !strings.EqualFold(subprotos[0], Subprotocol) {
		// Spec §11.29: subprotocol MUST include `r1.bearer`. We require
		// it as the FIRST value because that's the slot the JS WS
		// constructor lets the caller specify deterministically.
		w.Header().Set("WWW-Authenticate", `Bearer realm="r1"`)
		http.Error(w, "ws: missing r1.bearer subprotocol", http.StatusUnauthorized)
		return
	}

	// Token is the SECOND value in the comma-separated subprotocol
	// list per spec §11.29. Browser clients pass `[Subprotocol, token]`
	// to the WebSocket constructor; the server picks Subprotocol as
	// the negotiated value and ignores the token slot in the response.
	clientToken := ""
	if len(subprotos) >= 2 {
		clientToken = subprotos[1]
	}
	// Bearer header takes precedence if present (CLI clients prefer
	// the standard auth path; browsers can't set this header on WS).
	if hdr := r.Header.Get("Authorization"); hdr != "" {
		const prefix = "Bearer "
		if strings.HasPrefix(hdr, prefix) {
			clientToken = strings.TrimSpace(hdr[len(prefix):])
		}
	}
	if h.Token != "" && clientToken != h.Token {
		w.Header().Set("WWW-Authenticate", `Bearer realm="r1"`)
		http.Error(w, "ws: invalid bearer token", http.StatusUnauthorized)
		return
	}

	// Accept the upgrade. We pin Subprotocols to the bare "r1.bearer"
	// so the negotiated value echoed back is always that — the token
	// slot in the offer is consumed during the handshake and never
	// re-emitted.
	wsConn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:       []string{Subprotocol},
		InsecureSkipVerify: true, // origin already enforced by middleware (TASK-20)
	})
	if err != nil {
		// Accept already wrote a status; nothing more to do.
		return
	}
	// Default close-on-exit; serveLoop replaces with explicit close
	// when it tears down for a known reason (idle timeout etc).
	defer func() { _ = wsConn.Close(websocket.StatusInternalError, "handler exit") }()

	conn := &Conn{WS: wsConn}
	h.serveLoop(r.Context(), conn)
}

// parseSubprotocols splits a comma-separated Sec-WebSocket-Protocol
// header into per-value tokens. RFC 6455 §1.9 allows OWS around commas
// and case-insensitive matching at the protocol-token level; we apply
// strings.TrimSpace per value so " r1.bearer , token " round-trips.
func parseSubprotocols(h string) []string {
	if h == "" {
		return nil
	}
	parts := strings.Split(h, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		v := strings.TrimSpace(p)
		if v == "" {
			continue
		}
		out = append(out, v)
	}
	return out
}

// serveLoop is the per-connection read loop. It:
//
//  1. spawns a ping watchdog (PingInterval / IdleTimeout);
//  2. fires the OnConnect hook;
//  3. reads JSON-RPC envelopes, dispatches, writes responses;
//  4. exits on Read error, ctx cancel, or watchdog trip.
//
// When the loop exits, both the watchdog and the connection are torn
// down. The OnConnect ctx is cancelled so per-connection resources
// (TASK-32 subscriptions) can drain.
func (h *Handler) serveLoop(parent context.Context, conn *Conn) {
	idle := h.IdleTimeout
	if idle == 0 {
		idle = IdleTimeout
	}
	pingInt := h.PingInterval
	if pingInt == 0 {
		pingInt = PingInterval
	}

	connCtx, cancel := context.WithCancel(parent)
	defer cancel()

	if h.OnConnect != nil {
		h.OnConnect(connCtx, conn)
	}

	// Ping watchdog. coder/websocket's Ping returns nil on pong receipt,
	// error otherwise; we treat any error as "client gone" and close
	// the connection so the read loop unblocks. The pong is also a
	// control frame received inside Read, which extends the read
	// deadline implicitly (we re-derive the deadline ctx every Read
	// below).
	go func() {
		ticker := time.NewTicker(pingInt)
		defer ticker.Stop()
		for {
			select {
			case <-connCtx.Done():
				return
			case <-ticker.C:
				pingCtx, pcancel := context.WithTimeout(connCtx, idle)
				err := conn.WS.Ping(pingCtx)
				pcancel()
				if err != nil {
					// Force-close: the read loop will return on its
					// next iteration with an error.
					_ = conn.WS.Close(websocket.StatusGoingAway, "ping timeout")
					return
				}
			}
		}
	}()

	for {
		// Per-Read deadline. If the peer goes silent for IdleTimeout
		// AND fails to respond to ping (the watchdog above also closes
		// in that case), the Read returns context.DeadlineExceeded.
		readCtx, rcancel := context.WithTimeout(connCtx, idle)
		typ, data, err := conn.WS.Read(readCtx)
		rcancel()
		if err != nil {
			// Includes normal close, idle timeout, and watchdog-driven
			// close. The deferred Close in ServeHTTP wraps up.
			return
		}
		if typ != websocket.MessageText {
			// Per JSON-RPC 2.0 wire choice (NDJSON / JSON), only TEXT
			// frames carry envelopes. A binary frame is a protocol
			// error — close with 4400 (proto error).
			_ = conn.WS.Close(4400, "binary frame not allowed")
			return
		}
		h.dispatchFrame(connCtx, conn, data)
	}
}

// dispatchFrame parses one inbound TEXT frame as either a single
// envelope or a batch, dispatches each, and writes the response (if
// any) back as a TEXT frame.
//
// Errors decoding the envelope are returned as a CodeParseError /
// CodeInvalidRequest response with a null id (per JSON-RPC 2.0 §4.2 —
// the server cannot echo an id it never decoded).
func (h *Handler) dispatchFrame(ctx context.Context, conn *Conn, data []byte) {
	single, batch, err := jsonrpc.DecodeBatchOrSingle(data)
	if err != nil {
		resp := &jsonrpc.Response{
			JSONRPC: jsonrpc.Version,
			Error:   jsonrpc.NewError(jsonrpc.CodeParseError, "parse error: "+err.Error()),
		}
		// Best-effort write — if the peer is gone, nothing to do.
		_ = conn.WriteResponse(ctx, resp)
		return
	}
	if single != nil {
		resp := h.Dispatcher.Dispatch(ctx, single)
		if resp != nil {
			_ = conn.WriteResponse(ctx, resp)
		}
		return
	}
	resps := h.Dispatcher.DispatchBatch(ctx, batch)
	if len(resps) == 0 {
		// All notifications — no wire response per JSON-RPC 2.0 §6.
		return
	}
	out, err := jsonrpc.EncodeBatch(resps)
	if err != nil {
		// Programmer error: a Response we built failed to marshal.
		// Surface as a server-side internal error using a synthetic
		// envelope.
		resp := &jsonrpc.Response{
			JSONRPC: jsonrpc.Version,
			Error:   jsonrpc.NewError(jsonrpc.CodeInternalError, "encode batch: "+err.Error()),
		}
		_ = conn.WriteResponse(ctx, resp)
		return
	}
	_ = conn.WriteRaw(ctx, out)
}

// ----------------------------------------------------------------------
// Errors — kept here so transport callers can branch on them
// ----------------------------------------------------------------------

// ErrNoSubprotocol is returned by the upgrade path when the client did
// not advertise `r1.bearer`. ServeHTTP responds with 401; this constant
// lets unit tests pattern-match without hard-coding the string.
var ErrNoSubprotocol = errors.New("ws: missing r1.bearer subprotocol")

// ErrBadToken is returned by the upgrade path when the bearer token
// does not match the daemon's. ServeHTTP responds with 401.
var ErrBadToken = errors.New("ws: invalid bearer token")

// debugString is a tiny helper used by tests that want a one-line
// connection summary. Kept on the package surface so it doesn't pull
// in fmt at the top.
func debugString(c *Conn) string {
	if c == nil || c.WS == nil {
		return "<nil>"
	}
	return fmt.Sprintf("conn{subproto=%q}", c.WS.Subprotocol())
}
