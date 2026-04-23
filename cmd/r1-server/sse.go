// Package main — sse.go
//
// RS-4 item 19: Server-Sent Events stream endpoint
// (GET /api/session/{id}/events/stream) that powers the live-tailing
// stream view. The browser's EventSource API consumes this; on each
// poll the handler drains any new rows from session_events with
// id > cursor and emits one SSE "message" per row.
//
// Design notes:
//
//   - Pull-driven, not push-driven. The scanner already stabbed every
//     NDJSON line into SQLite, so we reuse that as the single source of
//     truth. Polling the DB every 500ms feels live enough for humans,
//     keeps implementation stdlib-only, and avoids a broadcast fan-out
//     layer we'd need to bolt onto scanner.go.
//
//   - Cursor negotiation via the Last-Event-ID header (EventSource's
//     built-in reconnect mechanism) OR the ?after= query param. If both
//     are present Last-Event-ID wins — that matches RFC for EventSource
//     reconnects and keeps the browser's auto-resume correct.
//
//   - Writes are flushed after every message so intermediaries don't
//     hold frames. Write errors close the stream — the client will
//     reconnect and we'll resume from the last id it saw.
//
//   - Heartbeat comment frames keep proxies from closing idle
//     connections. SSE comments (lines starting with ":") are ignored
//     by EventSource so they're safe filler.
//
// The handler is wired into buildMux (main.go) alongside the existing
// /events endpoint so the tests in sse_test.go can exercise it without
// bringing up the full server.
package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

// sseTickInterval is how often the handler polls the DB for new events
// while a client is connected. Matches scanner.TailInterval defaults so
// an event lands in SQLite and in the client's EventSource within one
// tick of each other under nominal load.
const sseTickInterval = 500 * time.Millisecond

// sseHeartbeatInterval is the max gap between any two writes on an
// idle stream. EventSource itself times out after ~45s of silence on
// several proxies, so a 15s comment frame keeps the pipe warm with
// ample margin.
const sseHeartbeatInterval = 15 * time.Second

// sseInitialBatchLimit caps how many historical rows a single reconnect
// pulls in its first drain. Prevents a long-running session from
// flooding the wire when a client opens the stream fresh.
const sseInitialBatchLimit = 500

// handleEventsStream is the http.HandlerFunc for
// GET /api/session/{id}/events/stream. Factory form (returns the
// handler closed over db+logger) so buildMux can keep its wiring
// uniform and tests can instantiate a handler against a test DB.
func handleEventsStream(db *DB, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" {
			writeErr(w, http.StatusBadRequest, "missing session id")
			return
		}

		// ResponseWriter must support flushing — SSE is useless
		// otherwise. httptest.NewRecorder does support Flush via its
		// own path, and net/http's connection writer always does.
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeErr(w, http.StatusInternalServerError, "streaming unsupported")
			return
		}

		// Cursor negotiation: Last-Event-ID beats ?after= beats zero.
		var cursor int64
		if raw := r.Header.Get("Last-Event-ID"); raw != "" {
			if v, err := strconv.ParseInt(raw, 10, 64); err == nil && v >= 0 {
				cursor = v
			}
		} else if raw := r.URL.Query().Get("after"); raw != "" {
			if v, err := strconv.ParseInt(raw, 10, 64); err == nil && v >= 0 {
				cursor = v
			}
		}

		// Confirm the session exists; otherwise SSE clients would hang
		// on an empty stream waiting for events that will never come.
		if _, err := db.GetSession(id); err != nil {
			writeErr(w, http.StatusNotFound, "session not found: %v", err)
			return
		}

		// SSE headers. No-transform stops compression proxies from
		// buffering frames. Connection: keep-alive is technically
		// HTTP/1.1 default but some reverse proxies honor it more
		// reliably than the absence of an explicit header.
		h := w.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache, no-transform")
		h.Set("Connection", "keep-alive")
		h.Set("X-Accel-Buffering", "no") // nginx: disable response buffering
		w.WriteHeader(http.StatusOK)

		// Initial retry hint (ms) — browsers will use this when the
		// stream drops. 2s is a sane default; the reconnect itself is
		// cheap because session_events pagination is indexed.
		if _, err := fmt.Fprintf(w, "retry: 2000\n\n"); err != nil {
			return
		}
		flusher.Flush()

		ctx := r.Context()
		ticker := time.NewTicker(sseTickInterval)
		defer ticker.Stop()
		heartbeat := time.NewTicker(sseHeartbeatInterval)
		defer heartbeat.Stop()

		// Drain the initial backlog (bounded) so clients reconnecting
		// after a proxy blip don't re-read the whole session. New
		// connections with cursor=0 get up to sseInitialBatchLimit
		// warm-up rows, then the polling loop takes over.
		newCursor, err := flushNewEvents(w, flusher, db, id, cursor, sseInitialBatchLimit)
		if err != nil {
			if logger != nil {
				logger.Debug("sse initial drain", "instance_id", id, "err", err)
			}
			return
		}
		cursor = newCursor

		for {
			select {
			case <-ctx.Done():
				return
			case <-heartbeat.C:
				// SSE comment line: ignored by EventSource but keeps
				// the connection from being reaped by middleboxes.
				if _, err := fmt.Fprintf(w, ": keep-alive %d\n\n", time.Now().Unix()); err != nil {
					return
				}
				flusher.Flush()
			case <-ticker.C:
				newCursor, err := flushNewEvents(w, flusher, db, id, cursor, 0)
				if err != nil {
					if logger != nil && ctx.Err() == nil {
						// ctx cancelled = client hung up, not an error.
						logger.Debug("sse flush", "instance_id", id, "err", err)
					}
					return
				}
				cursor = newCursor
			}
		}
	}
}

// flushNewEvents reads rows past `cursor` from session_events and
// writes each one as one SSE message framed as:
//
//	id: <row-id>
//	event: <event_type>
//	data: <JSON data blob>
//
// The returned cursor is the id of the last row written, or the input
// cursor if nothing new was flushed. limit<=0 applies the DB default
// cap (1000 per pagination call).
//
// Used by the live loop AND the initial drain, which is why limit is a
// parameter and not a constant.
func flushNewEvents(w http.ResponseWriter, flusher http.Flusher, db *DB, instanceID string, cursor int64, limit int) (int64, error) {
	rows, err := db.ListEvents(instanceID, cursor, limit)
	if err != nil {
		return cursor, err
	}
	if len(rows) == 0 {
		return cursor, nil
	}
	for _, row := range rows {
		// event name (after "event: ") must not contain \n. Our types
		// are short alphanumeric strings like "task.start" so a plain
		// fallback of "message" when event_type is empty keeps the
		// wire format valid without pre-sanitizing.
		eventName := row.EventType
		if eventName == "" {
			eventName = "message"
		}
		// data lines must not contain raw newlines per the EventSource
		// spec. Our ListEvents returns the raw NDJSON line which is
		// single-line by construction, but we guard by writing on one
		// line and trusting the scanner's newline-stripping in
		// scanner.go (see extractEventMeta callers).
		data := row.Data
		if len(data) == 0 {
			data = []byte("{}")
		}
		if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", row.ID, eventName, data); err != nil {
			return cursor, err
		}
		cursor = row.ID
	}
	flusher.Flush()
	return cursor, nil
}

