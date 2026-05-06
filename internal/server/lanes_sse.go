// Package server — lanes-protocol HTTP+SSE handler (TASK-13).
//
// Implements specs/lanes-protocol.md §6.1 (Last-Event-ID over HTTP+SSE).
// The handler:
//
//   - reads ?session_id= (required) and rejects empty / mismatched values
//     with 400 Bad Request;
//   - reads Last-Event-ID header (preferred) or ?since_seq= query param
//     (URL-friendly fallback) as the replay cursor;
//   - sets Content-Type: text/event-stream and X-R1-Lanes-Version: 1;
//   - subscribes to the session's hub.Bus for live lane events filtered
//     by session_id;
//   - emits the synthetic session.bound event (seq=0) FIRST per spec §5.5
//     and §6.2 (TASK-17);
//   - replays from the WAL since since_seq+1 when wired (TASK-16);
//   - formats each event as `id: <seq>\nevent: <type>\ndata: <json>\n\n`
//     per WHATWG SSE spec.
//
// Wire dependencies are abstracted behind LanesHub and LanesWAL interfaces
// so server tests can drive the handler with a fake hub and the production
// wiring can plug in *hub.Bus and *bus.Bus respectively.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"

	"github.com/RelayOne/r1/internal/hub"
)

// LanesHub is the minimal subscription surface the server needs from the
// in-process event hub. *hub.Bus implements this directly via Register /
// Unregister; tests pass an in-memory shim so they don't have to spin up
// the full bus.
type LanesHub interface {
	// Register subscribes to lane events. The handler is invoked
	// in-process; the implementation is responsible for goroutine /
	// circuit-breaker policy. The returned id is opaque and may be
	// passed to Unregister.
	Register(sub hub.Subscriber)
	Unregister(id string)
}

// LanesWAL is the minimal replay surface the server needs from the
// durable bus. The production wiring passes *bus.Bus (which exposes
// Replay(pattern, fromSeq, handler)). Tests may pass an in-memory shim.
//
// Per spec §6.1: replay from since_seq+1; if the requested seq is older
// than the WAL retention window, ReplayLane returns ErrWALTruncated.
type LanesWAL interface {
	// ReplayLane invokes handler for every persisted lane event with
	// the given session_id and seq >= fromSeq, in seq order.
	//
	// fromSeq=0 means "no replay; only future live events". The
	// handler runs synchronously on the calling goroutine; if it
	// returns an error replay aborts.
	//
	// Returns ErrWALTruncated when fromSeq > 0 and the WAL has
	// already pruned past fromSeq (the client must re-subscribe with
	// since_seq=0 per spec §6.3).
	ReplayLane(ctx context.Context, sessionID string, fromSeq uint64, handler func(*hub.Event) error) error
}

// ErrWALTruncated is returned by LanesWAL.ReplayLane when the requested
// fromSeq predates the retained WAL window. Surfaced over HTTP+SSE as
// 404 Not Found (spec §6.1) and over JSON-RPC as -32004 (TASK-16).
type ErrWALTruncatedError struct{ FromSeq uint64 }

func (e *ErrWALTruncatedError) Error() string {
	return fmt.Sprintf("lanes: WAL truncated past seq=%d", e.FromSeq)
}

// LanesWiring carries the optional dependencies needed by the lanes-protocol
// endpoints. Either field may be nil:
//
//   - Hub == nil disables the live subscription (clients only see replay).
//   - WAL == nil disables replay (clients only see live + session.bound).
type LanesWiring struct {
	Hub LanesHub
	WAL LanesWAL
}

// LanesProtocolVersion is the X-R1-Lanes-Version header value (spec §5.6).
const LanesProtocolVersion = "1"

// laneSSESubID is the prefix for hub subscriber IDs registered by the SSE
// handler. The full ID is "<prefix>:<session_id>:<request_uuid>" so each
// connected client gets a unique entry in the bus and Unregister works in
// O(1) on disconnect.
const laneSSESubID = "server.lanes.sse"

// handleLaneEvents serves the HTTP+SSE lane stream per spec §6.1.
//
// HTTP request:
//
//	GET /v1/lanes/events?session_id=sess_01... HTTP/1.1
//	Authorization: Bearer <token>
//	Last-Event-ID: 142
//	Accept: text/event-stream
//
// HTTP response:
//
//	HTTP/1.1 200 OK
//	Content-Type: text/event-stream
//	X-R1-Lanes-Version: 1
//	(stream of `id: <seq>\nevent: <type>\ndata: <json>\n\n` records)
//
// On WAL-truncated replay request: 404 with structured error body.
// On missing session_id: 400. On streaming-unsupported writer: 500.
func (s *Server) handleLaneEvents(w http.ResponseWriter, r *http.Request) {
	if s.Lanes == nil {
		http.Error(w, "lanes endpoint not configured", http.StatusServiceUnavailable)
		return
	}

	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		http.Error(w, "missing session_id query parameter", http.StatusBadRequest)
		return
	}

	// Last-Event-ID header is preferred (WHATWG SSE convention). Falls
	// back to ?since_seq= so URL-only clients (curl scripts) can replay.
	sinceSeq := uint64(0)
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			sinceSeq = n
		}
	} else if v := r.URL.Query().Get("since_seq"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			sinceSeq = n
		}
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Replay from WAL (if wired) BEFORE setting SSE headers so we can
	// still report 404 wal_truncated as a normal HTTP response.
	if sinceSeq > 0 && s.Lanes.WAL != nil {
		// Probe replay: collect into a slice so we can short-circuit on
		// truncation before committing the SSE response. The collected
		// events are then re-emitted via the SSE writer in order.
		// Probe is cheap because the WAL is local-disk NDJSON; for
		// large windows callers should use the WS path instead.
		var collected []*hub.Event
		err := s.Lanes.WAL.ReplayLane(r.Context(), sessionID, sinceSeq+1, func(ev *hub.Event) error {
			collected = append(collected, ev)
			return nil
		})
		if err != nil {
			if _, isTrunc := err.(*ErrWALTruncatedError); isTrunc {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-R1-Lanes-Version", LanesProtocolVersion)
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0",
					"error": map[string]any{
						"code":    -32004,
						"message": "wal_truncated",
						"data": map[string]any{
							"stoke_code": "not_found",
							"detail":     "wal_truncated",
							"since_seq":  sinceSeq,
						},
					},
				})
				return
			}
			http.Error(w, "replay failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		// Commit SSE headers, emit replay batch, then proceed live.
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-R1-Lanes-Version", LanesProtocolVersion)
		w.WriteHeader(http.StatusOK)

		// session.bound synthetic always fires first (TASK-17).
		writeSSEBound(w, sessionID)
		flusher.Flush()

		for _, ev := range collected {
			writeSSEEvent(w, ev)
			flusher.Flush()
		}
		s.streamLiveLaneEvents(w, flusher, r.Context(), sessionID)
		return
	}

	// No replay (no WAL or since_seq=0). Stream live only after the
	// session.bound synthetic.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-R1-Lanes-Version", LanesProtocolVersion)
	w.WriteHeader(http.StatusOK)

	writeSSEBound(w, sessionID)
	flusher.Flush()
	s.streamLiveLaneEvents(w, flusher, r.Context(), sessionID)
}

// streamLiveLaneEvents subscribes to the lanes hub and forwards events
// for sessionID until the request context is cancelled. Each event is
// formatted per writeSSEEvent. The subscription is removed on return so
// no goroutine leaks across reconnects.
func (s *Server) streamLiveLaneEvents(w http.ResponseWriter, flusher http.Flusher, ctx context.Context, sessionID string) {
	if s.Lanes == nil || s.Lanes.Hub == nil {
		<-ctx.Done()
		return
	}

	// Buffered channel + dedicated subscriber: hub.Bus invokes the
	// handler synchronously (ModeObserve dispatches a goroutine per
	// event), so the channel hop decouples our writer from the bus.
	// Buffer 256 matches the streamjson convention.
	ch := make(chan *hub.Event, 256)
	subID := fmt.Sprintf("%s:%s:%p", laneSSESubID, sessionID, ch)
	var once sync.Once
	closeCh := func() { once.Do(func() { close(ch) }) }

	s.Lanes.Hub.Register(hub.Subscriber{
		ID: subID,
		Events: []hub.EventType{
			hub.EventLaneCreated,
			hub.EventLaneStatus,
			hub.EventLaneDelta,
			hub.EventLaneCost,
			hub.EventLaneNote,
			hub.EventLaneKilled,
		},
		Mode:     hub.ModeObserve,
		Priority: 9200,
		Handler: func(_ context.Context, ev *hub.Event) *hub.HookResponse {
			if ev == nil || ev.Lane == nil {
				return &hub.HookResponse{Decision: hub.Allow}
			}
			if ev.Lane.SessionID != sessionID {
				return &hub.HookResponse{Decision: hub.Allow}
			}
			// Non-blocking: if the writer is wedged we drop rather
			// than back-pressure the bus.
			select {
			case ch <- ev:
			default:
			}
			return &hub.HookResponse{Decision: hub.Allow}
		},
	})
	defer s.Lanes.Hub.Unregister(subID)
	defer closeCh()

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			writeSSEEvent(w, ev)
			flusher.Flush()
		}
	}
}

// writeSSEBound emits the synthetic session.bound record at seq=0
// (TASK-17, spec §5.5). This is the first thing every subscriber sees
// so it can detect mismatches against its locally-tracked session_id.
func writeSSEBound(w http.ResponseWriter, sessionID string) {
	body, _ := json.Marshal(map[string]any{
		"event":      "session.bound",
		"seq":        0,
		"session_id": sessionID,
	})
	fmt.Fprintf(w, "id: 0\nevent: session.bound\ndata: %s\n\n", body)
}

// writeSSEEvent formats one hub.Event carrying a LaneEvent payload as a
// single SSE record. Per spec §6.1 the `id:` field carries the seq so
// reconnecting clients can resume via Last-Event-ID.
func writeSSEEvent(w http.ResponseWriter, ev *hub.Event) {
	if ev == nil || ev.Lane == nil {
		return
	}
	// data payload mirrors the §5.2 / §5.3 wire body: {event,
	// session_id, lane_id, event_id, seq, at, data}. The SSE `event:`
	// line carries the same kind so clients can dispatch on
	// EventSource.addEventListener.
	at := ""
	if !ev.Timestamp.IsZero() {
		at = ev.Timestamp.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
	}
	payload := map[string]any{
		"event":      string(ev.Type),
		"event_id":   ev.ID,
		"session_id": ev.Lane.SessionID,
		"lane_id":    ev.Lane.LaneID,
		"seq":        ev.Lane.Seq,
		"at":         at,
		"data":       buildLaneRPCData(ev),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.Lane.Seq, ev.Type, body)
}

// buildLaneRPCData mirrors the per-event-type subobject documented in
// spec §4 / §5.2. Co-located with the SSE handler (rather than in
// streamjson/lane.go) so the server package does not depend on
// streamjson and so the SSE/WS shapes evolve together. The streamjson
// NDJSON shape is intentionally a sibling, not a parent.
func buildLaneRPCData(ev *hub.Event) map[string]any {
	if ev == nil || ev.Lane == nil {
		return map[string]any{}
	}
	data := map[string]any{}
	switch ev.Type {
	case hub.EventLaneCreated:
		if ev.Lane.Kind != "" {
			data["kind"] = string(ev.Lane.Kind)
		}
		if ev.Lane.LobeName != "" {
			data["lobe_name"] = ev.Lane.LobeName
		}
		if ev.Lane.ParentID != "" {
			data["parent_lane_id"] = ev.Lane.ParentID
		}
		if ev.Lane.Label != "" {
			data["label"] = ev.Lane.Label
		}
		if ev.Lane.StartedAt != nil {
			data["started_at"] = ev.Lane.StartedAt.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
		}
		if ev.Lane.Labels != nil {
			data["labels"] = ev.Lane.Labels
		}
	case hub.EventLaneStatus:
		if ev.Lane.Status != "" {
			data["status"] = string(ev.Lane.Status)
		}
		if ev.Lane.PrevStatus != "" {
			data["prev_status"] = string(ev.Lane.PrevStatus)
		}
		if ev.Lane.Reason != "" {
			data["reason"] = ev.Lane.Reason
		}
		if ev.Lane.ReasonCode != "" {
			data["reason_code"] = ev.Lane.ReasonCode
		}
		if ev.Lane.EndedAt != nil {
			data["ended_at"] = ev.Lane.EndedAt.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
		}
	case hub.EventLaneDelta:
		if ev.Lane.DeltaSeq != 0 {
			data["delta_seq"] = ev.Lane.DeltaSeq
		}
		if ev.Lane.Block != nil {
			data["content_block"] = ev.Lane.Block
		}
	case hub.EventLaneCost:
		data["tokens_in"] = ev.Lane.TokensIn
		data["tokens_out"] = ev.Lane.TokensOut
		if ev.Lane.CachedTokens != 0 {
			data["cached_tokens"] = ev.Lane.CachedTokens
		}
		data["usd"] = ev.Lane.USD
		if ev.Lane.CumulativeUSD != 0 {
			data["cumulative_usd"] = ev.Lane.CumulativeUSD
		}
	case hub.EventLaneNote:
		if ev.Lane.NoteID != "" {
			data["note_id"] = ev.Lane.NoteID
		}
		if ev.Lane.NoteSeverity != "" {
			data["severity"] = ev.Lane.NoteSeverity
		}
		if ev.Lane.NoteKind != "" {
			data["kind"] = ev.Lane.NoteKind
		}
		if ev.Lane.NoteSummary != "" {
			data["summary"] = ev.Lane.NoteSummary
		}
	case hub.EventLaneKilled:
		if ev.Lane.Reason != "" {
			data["reason"] = ev.Lane.Reason
		}
		if ev.Lane.Actor != "" {
			data["actor"] = ev.Lane.Actor
		}
		if ev.Lane.ActorID != "" {
			data["actor_id"] = ev.Lane.ActorID
		}
	}
	return data
}
