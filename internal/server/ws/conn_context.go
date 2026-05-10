// conn_context.go — pass the live *Conn down to JSON-RPC handlers.
//
// JSON-RPC method handlers receive (ctx, params) only. For verbs that
// must register per-connection state (notably session.subscribe, which
// mints a Subscription wired to write notifications back to the same
// connection), the Conn pointer needs to be reachable from the
// handler. We thread it via context.WithValue using an unexported key
// type so callers can only access it through the typed
// ConnFromContext helper.

package ws

import "context"

// connKeyT is an unexported type used as the context key. Using a
// dedicated type avoids collision with any string-keyed value a
// caller might stash on the same ctx.
type connKeyT struct{}

// ContextWithConn returns a child context carrying the supplied Conn.
// dispatchFrame calls this before invoking the dispatcher so handlers
// can pull the Conn out via ConnFromContext.
func ContextWithConn(ctx context.Context, c *Conn) context.Context {
	return context.WithValue(ctx, connKeyT{}, c)
}

// ConnFromContext returns the Conn stashed by ContextWithConn, or nil
// when the ctx was not produced by the WS dispatch path. Handlers
// should treat a nil return as "this RPC is being invoked outside a
// WS connection" and fall back to a connection-less behavior.
func ConnFromContext(ctx context.Context) *Conn {
	if ctx == nil {
		return nil
	}
	c, _ := ctx.Value(connKeyT{}).(*Conn)
	return c
}
