// Package sse implements the read-only Server-Sent Events bridge for
// the r1d-server multi-session daemon (specs/r1d-server.md Phase E
// item 33).
//
// # Why an SSE bridge alongside WebSocket
//
// The TASK-29 WebSocket transport handles the bidirectional case: a
// browser or CLI client speaks JSON-RPC requests AND receives
// `$/event` notifications on the same connection. SSE is a strictly
// uplink (server -> client) channel — but it has two practical
// advantages over WS for read-only consumers:
//
//   - works through every reverse proxy and corporate firewall that
//     blocks WebSocket upgrades (curl, nginx, k8s ingress);
//   - native browser support via `EventSource` with automatic
//     reconnect AND `Last-Event-ID` resume — no JS shim needed.
//
// The bridge subscribes to the same per-session event stream as a WS
// subscriber and writes each event as one SSE record. Resume works
// the same way: the client passes its last-seen seq either via
// `Last-Event-ID` (the WHATWG-spec channel) or `?since_seq=N` (a
// query-string fallback some HTTP libraries make easier to set).
//
// # Token query param
//
// Browser EventSource cannot set the `Authorization` header. The
// daemon therefore accepts `?token=<t>` as an alternative: a query
// param that the bearer middleware (TASK-18) recognises after the SSE
// handler stamps it into the Authorization header. We do this in
// AttachAuthFromQuery — see below — rather than in the bearer
// middleware itself, because token-in-query is intentionally restricted
// to the SSE path (it leaks into nginx access logs; not a fit for
// general API use).
//
// # Nginx-friendly buffering
//
// `X-Accel-Buffering: no` is the canonical nginx directive that
// disables response buffering — without it, a default nginx config
// holds the SSE stream until the upstream closes, defeating the whole
// point. We set it on every response unconditionally; non-nginx
// clients ignore the header.
package sse

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/RelayOne/r1/internal/server/jsonrpc"
)

// HeaderXAccelBuffering is the response header we set so nginx does
// NOT buffer the stream. Spec §11.33 calls this out explicitly.
const HeaderXAccelBuffering = "X-Accel-Buffering"

// HeaderLastEventID is the WHATWG SSE resume header. Browsers set
// this automatically on EventSource reconnect; CLI clients can also
// set it explicitly.
const HeaderLastEventID = "Last-Event-ID"

// QueryToken is the query param used to pass a bearer token from a
// browser EventSource (which cannot set Authorization headers).
const QueryToken = "token"

// QuerySinceSeq is the query-string fallback for resume cursor when
// the client cannot set Last-Event-ID.
const QuerySinceSeq = "since_seq"

// SubscribeFunc is the bridge's binding to the daemon's session
// subscribe path. Implementations spin up a *jsonrpc.Subscription
// that publishes via the supplied EventSink.
//
// The returned cancel func MUST tear down the subscription (close the
// underlying *Subscription via Close); the bridge calls it on client
// disconnect.
type SubscribeFunc func(
	ctx context.Context,
	sessionID string,
	sinceSeq uint64,
	filter []string,
	sink jsonrpc.EventSink,
) (cancel func(), err error)

// Handler is the HTTP handler for `/v1/sessions/:id/sse`. The bridge
// expects the session ID to be extracted by the router and passed
// either via path-mux pattern or by SessionID being filled in via a
// thin wrapper. This keeps the package router-agnostic — net/http,
// chi, gorilla all wire into it the same way (see ServeHTTP).
type Handler struct {
	// Subscribe is the daemon's per-session subscribe function. Required.
	Subscribe SubscribeFunc

	// SessionIDFromRequest extracts the session id from the request
	// (path param, query param, etc). Required. Wired by the daemon
	// (different routers expose path params differently).
	SessionIDFromRequest func(r *http.Request) string

	// FilterFromRequest extracts an optional filter list (event types).
	// Empty / nil means "all events". Optional.
	FilterFromRequest func(r *http.Request) []string
}

// ServeHTTP runs the SSE response. Connection lifecycle:
//
//  1. Resolve session id; 400 if missing.
//  2. Resolve since_seq from Last-Event-ID header or ?since_seq query.
//  3. Set SSE headers including X-Accel-Buffering: no.
//  4. Bind a synchronous EventSink that writes one SSE record per
//     event and flushes immediately.
//  5. Call Subscribe; the daemon drives Replay-then-live ordering.
//  6. Block on the request context until the client disconnects;
//     cancel teardown.
//
// The SSE record format is the standard:
//
//	id: <seq>
//	event: <type>
//	data: <json>
//	(blank line)
//
// The `id` field is the per-subscription monotonic seq; clients use
// it to resume via `Last-Event-ID`. The `event` field is the JSON-RPC
// event type ("session.delta", "lane.delta", ...).
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.Subscribe == nil || h.SessionIDFromRequest == nil {
		http.Error(w, "sse: bridge not configured", http.StatusInternalServerError)
		return
	}
	sessionID := h.SessionIDFromRequest(r)
	if sessionID == "" {
		http.Error(w, "sse: missing session id", http.StatusBadRequest)
		return
	}

	sinceSeq := resolveSinceSeq(r)

	// SSE headers. Order matters only for X-Accel-Buffering, which MUST
	// land on the wire before the first byte of body — but Go's
	// http.ResponseWriter buffers headers until the first Write, so
	// any order works.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set(HeaderXAccelBuffering, "no")
	w.WriteHeader(http.StatusOK)

	flusher, ok := w.(http.Flusher)
	if !ok {
		// The test ResponseRecorder doesn't implement Flusher; in
		// production every real ResponseWriter does. Fail-fast here
		// because we can't honour SSE semantics without a Flusher.
		http.Error(w, "sse: streaming not supported by ResponseWriter", http.StatusInternalServerError)
		return
	}

	// Wrap writes in a per-handler mutex so the daemon's bus
	// subscriber (which fires from a hub goroutine) and our
	// handler-side write don't interleave a record.
	var writeMu sync.Mutex
	writeRecord := func(ev *jsonrpc.SubscriptionEvent) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return writeSSEEvent(w, flusher, ev)
	}

	var filter []string
	if h.FilterFromRequest != nil {
		filter = h.FilterFromRequest(r)
	}

	cancel, err := h.Subscribe(r.Context(), sessionID, sinceSeq, filter, func(ctx context.Context, ev *jsonrpc.SubscriptionEvent) error {
		return writeRecord(ev)
	})
	if err != nil {
		// Best-effort: emit an `event: error` record so an
		// EventSource consumer sees the problem instead of a silent
		// disconnect. Connection still closes.
		_ = writeRecord(&jsonrpc.SubscriptionEvent{
			SubID: "",
			Seq:   0,
			Type:  "error",
			Data:  map[string]string{"message": err.Error()},
		})
		return
	}
	defer cancel()

	// Block until the client disconnects. The Subscribe-supplied
	// cancel tears down the subscription; the request context's Done
	// channel is the canonical disconnect signal in net/http.
	<-r.Context().Done()
}

// resolveSinceSeq pulls the resume cursor from Last-Event-ID, falling
// back to the ?since_seq query param. Both are uint64-decoded; a
// malformed value silently degrades to 0 (start from "now") rather
// than 400, so a confused client gets live events instead of nothing.
func resolveSinceSeq(r *http.Request) uint64 {
	if hdr := r.Header.Get(HeaderLastEventID); hdr != "" {
		if v, err := strconv.ParseUint(strings.TrimSpace(hdr), 10, 64); err == nil {
			return v
		}
	}
	if q := r.URL.Query().Get(QuerySinceSeq); q != "" {
		if v, err := strconv.ParseUint(q, 10, 64); err == nil {
			return v
		}
	}
	return 0
}

// writeSSEEvent emits one SSE record. The data field is the
// JSON-encoded event; multi-line values get one `data: ` prefix per
// line per WHATWG SSE.
func writeSSEEvent(w http.ResponseWriter, flusher http.Flusher, ev *jsonrpc.SubscriptionEvent) error {
	dataBytes, err := json.Marshal(ev.Data)
	if err != nil {
		// Encode the error rather than the original payload — a
		// downstream JSON-decode failure is informative.
		dataBytes, _ = json.Marshal(map[string]string{"error": "marshal: " + err.Error()})
	}

	var b strings.Builder
	if ev.Seq > 0 {
		fmt.Fprintf(&b, "id: %d\n", ev.Seq)
	}
	if ev.Type != "" {
		fmt.Fprintf(&b, "event: %s\n", ev.Type)
	}
	for _, line := range strings.Split(string(dataBytes), "\n") {
		fmt.Fprintf(&b, "data: %s\n", line)
	}
	b.WriteString("\n")

	if _, err := w.Write([]byte(b.String())); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

// AttachAuthFromQuery is HTTP middleware that promotes a `?token=<t>`
// query param to an Authorization: Bearer header BEFORE the bearer
// middleware sees the request. Apply to the SSE route only — never
// to general APIs — because tokens in query strings appear in nginx
// access logs by default.
//
// The middleware is no-op when:
//
//   - no `?token=` is present (the request flows through unchanged), or
//   - an Authorization header is already set (we don't overwrite an
//     explicit bearer header — query param is the FALLBACK path).
func AttachAuthFromQuery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			if tok := r.URL.Query().Get(QueryToken); tok != "" {
				r.Header.Set("Authorization", "Bearer "+tok)
			}
		}
		next.ServeHTTP(w, r)
	})
}

// PathSessionID is a helper SessionIDFromRequest implementation for
// routers that put the id in the path (e.g. the Go 1.22+ ServeMux
// pattern `/v1/sessions/{id}/sse`). Callers can wire this directly,
// or replace it with a router-specific shim.
//
// The default implementation expects PathValue("id") to be set; if
// not, falls back to the query string `?session_id=`.
func PathSessionID(r *http.Request) string {
	if v := r.PathValue("id"); v != "" {
		return v
	}
	return r.URL.Query().Get("session_id")
}

// QueryFilter is a helper FilterFromRequest implementation that reads
// a comma-separated `?filter=` query param. Empty -> nil filter.
func QueryFilter(r *http.Request) []string {
	q := r.URL.Query().Get("filter")
	if q == "" {
		return nil
	}
	parts := strings.Split(q, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		v := strings.TrimSpace(p)
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}
