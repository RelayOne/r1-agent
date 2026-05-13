// cdp.go: minimal CDP-over-WS JSON-RPC layer for the Browserless
// (and, via reuse, the in-house) provider.
//
// CDP frames are JSON objects of the shape
//
//	{ "id": <int>, "method": "Module.method", "params": {...} }
//	{ "id": <int>, "result": {...} }
//	{ "id": <int>, "error":  {"code": -32602, "message": "..."} }
//	{          "method": "Module.event",  "params": {...} }  // server-initiated
//
// One read pump goroutine demultiplexes incoming frames by id and by
// event method. Callers either:
//
//   - Call (c).Send(ctx, method, params, out)              — blocks until response
//   - Call (c).Subscribe(method) <-chan json.RawMessage    — receives events until Close
//
// This package layer is deliberately stand-alone — it knows nothing
// about Browserless-vs-in-house auth, network policy, or sessions.
// Those concerns live in client.go.

package browserless

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// CDPConn wraps a single *websocket.Conn with JSON-RPC id/event
// demux. Safe for concurrent use; Send may be called from many
// goroutines. Close is idempotent.
type CDPConn struct {
	conn *websocket.Conn

	idCtr    atomic.Int64
	pending  sync.Map // int64 → chan rpcReply
	subsMu   sync.Mutex
	subs     map[string][]chan json.RawMessage // method → fanout
	closed   atomic.Bool
	closeErr error

	doneCh chan struct{}
}

// DialCDP opens a CDP-over-WS connection to the given URL with the
// supplied HTTP headers. The caller is responsible for assembling the
// URL (token / trackingId / etc.) and for any TLS configuration via
// the provided HTTP client.
func DialCDP(ctx context.Context, url string, header http.Header, httpClient *http.Client) (*CDPConn, error) {
	opts := &websocket.DialOptions{HTTPHeader: header, HTTPClient: httpClient}
	conn, resp, err := websocket.Dial(ctx, url, opts)
	if err != nil {
		// Browserless and in-house both surface 401/403 here; surface
		// the HTTP status so the caller can map to ErrAuthFailed.
		if resp != nil && (resp.StatusCode == 401 || resp.StatusCode == 403) {
			return nil, fmt.Errorf("dial cdp: auth rejected (status %d): %w", resp.StatusCode, err)
		}
		return nil, fmt.Errorf("dial cdp: %w", err)
	}
	// CDP messages can be large (screenshots in base64). Lift the
	// per-message read limit to 64 MiB so a single Page.captureScreenshot
	// reply doesn't trip the default 32 KiB ceiling.
	conn.SetReadLimit(64 << 20)

	c := &CDPConn{
		conn:   conn,
		subs:   make(map[string][]chan json.RawMessage),
		doneCh: make(chan struct{}),
	}
	go c.readPump()
	return c, nil
}

// rpcReply is the channel-delivered shape from readPump back to the
// goroutine that issued Send.
type rpcReply struct {
	Result json.RawMessage
	Err    *rpcError
}

// rpcError mirrors the JSON-RPC error object.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("cdp error %d: %s", e.Code, e.Message)
}

// inboundFrame is the union shape of every WS message we read.
type inboundFrame struct {
	ID     int64           `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

// Send issues an RPC and blocks until the matching reply or ctx
// cancellation. The reply Result is JSON-unmarshalled into out when
// out is non-nil.
func (c *CDPConn) Send(ctx context.Context, method string, params any, out any) error {
	if c.closed.Load() {
		return errors.New("cdp: connection closed")
	}
	id := c.idCtr.Add(1)
	replyCh := make(chan rpcReply, 1)
	c.pending.Store(id, replyCh)
	defer c.pending.Delete(id)

	frame := struct {
		ID     int64  `json:"id"`
		Method string `json:"method"`
		Params any    `json:"params,omitempty"`
	}{ID: id, Method: method, Params: params}
	b, err := json.Marshal(frame)
	if err != nil {
		return fmt.Errorf("cdp marshal %s: %w", method, err)
	}
	if err := c.conn.Write(ctx, websocket.MessageText, b); err != nil {
		return fmt.Errorf("cdp write %s: %w", method, err)
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.doneCh:
		return errors.New("cdp: connection closed during Send")
	case r := <-replyCh:
		if r.Err != nil {
			return r.Err
		}
		if out != nil && len(r.Result) > 0 {
			if err := json.Unmarshal(r.Result, out); err != nil {
				return fmt.Errorf("cdp decode %s result: %w", method, err)
			}
		}
		return nil
	}
}

// Subscribe registers a fanout channel for a CDP event method (e.g.
// "Page.loadEventFired"). The returned channel is buffered to absorb
// short bursts; slow consumers see drops, not back-pressure on the
// read pump (this is what protects the rest of CDP traffic from a
// noisy event subscriber).
//
// Caller must Unsubscribe to release the slot.
func (c *CDPConn) Subscribe(method string) chan json.RawMessage {
	ch := make(chan json.RawMessage, 64)
	c.subsMu.Lock()
	c.subs[method] = append(c.subs[method], ch)
	c.subsMu.Unlock()
	return ch
}

// Unsubscribe drops the channel from the fanout list. Closing the
// channel is the unsubscribe contract; readers see a closed channel
// on Close.
func (c *CDPConn) Unsubscribe(method string, ch chan json.RawMessage) {
	c.subsMu.Lock()
	defer c.subsMu.Unlock()
	subs := c.subs[method]
	for i, s := range subs {
		if s == ch {
			c.subs[method] = append(subs[:i], subs[i+1:]...)
			break
		}
	}
}

// readPump drains the WS connection, dispatching responses by id and
// events by method. Runs until the connection closes; on exit it
// closes every pending reply chan and every subscriber chan so
// blocked callers unblock cleanly.
func (c *CDPConn) readPump() {
	defer close(c.doneCh)
	for {
		_, data, err := c.conn.Read(context.Background())
		if err != nil {
			c.closed.Store(true)
			c.closeErr = err
			// Notify every pending caller.
			c.pending.Range(func(_, v any) bool {
				if ch, ok := v.(chan rpcReply); ok {
					select {
					case ch <- rpcReply{Err: &rpcError{Code: -1, Message: "connection closed: " + err.Error()}}:
					default:
					}
				}
				return true
			})
			c.subsMu.Lock()
			for _, list := range c.subs {
				for _, ch := range list {
					close(ch)
				}
			}
			c.subs = nil
			c.subsMu.Unlock()
			return
		}
		var f inboundFrame
		if err := json.Unmarshal(data, &f); err != nil {
			// Malformed frame — drop. CDP servers are well-behaved;
			// a parse failure here usually means an injection of
			// non-JSON-RPC traffic. Logging is up to the embedder.
			continue
		}
		if f.ID != 0 {
			// Response to one of our Send calls.
			if v, ok := c.pending.Load(f.ID); ok {
				if ch, ok := v.(chan rpcReply); ok {
					ch <- rpcReply{Result: f.Result, Err: f.Error}
				}
			}
			continue
		}
		if f.Method != "" {
			// Server-initiated event — fan out to subscribers.
			c.subsMu.Lock()
			subs := c.subs[f.Method]
			c.subsMu.Unlock()
			for _, ch := range subs {
				select {
				case ch <- f.Params:
				default:
					// Slow consumer; drop. The subscriber's contract
					// is to drain fast enough.
				}
			}
		}
	}
}

// Close shuts the underlying WS connection. Idempotent.
func (c *CDPConn) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	err := c.conn.Close(websocket.StatusNormalClosure, "cdp close")
	// Give readPump a moment to drain, but don't block forever.
	select {
	case <-c.doneCh:
	case <-time.After(2 * time.Second):
	}
	return err
}

// Closed reports whether the connection is shut.
func (c *CDPConn) Closed() bool { return c.closed.Load() }
