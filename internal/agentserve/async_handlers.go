package agentserve

// Async-capable HTTP surface for spec 19 (agent-serve-async) tail:
// the cancel endpoint and the Server-Sent Events stream. Lives beside
// server.go so the MVP sync handlers stay compact and the async
// primitives can grow independently (worker pool already lives in
// pool.go; webhooks / eventlog recovery are follow-up work).
//
// Both endpoints are wired through the existing withAuth middleware
// in Handler(); no separate auth gate is added here.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ericmacdougall/stoke/internal/bus"
	"github.com/ericmacdougall/stoke/internal/eventlog"
)

// sseBufferSize is the buffered-channel capacity for each SSE
// subscriber. 64 is generous given we emit at most a handful of
// lifecycle events per task (queued / started / completed-or-failed-
// or-cancelled) — drops would indicate a pathologically slow client.
const sseBufferSize = 64

// Task status string constants. These are on the wire (JSON status
// field) and persisted to disk, so changes here would break consumers.
const (
	taskStatusCompleted = "completed"
	taskStatusFailed    = "failed"
	taskStatusCancelled = "cancelled"
)

// terminalStates is the set of task states that cause the SSE stream
// to terminate after the final frame is flushed.
var terminalStates = map[string]struct{}{
	taskStatusCompleted: {},
	taskStatusFailed:    {},
	taskStatusCancelled: {},
}

// handleCancelTask implements POST /api/task/{id}/cancel. Invokes the
// task's CancelFunc so Execute observes ctx.Done(), transitions the
// state to "cancelled", emits the cancellation event, and returns the
// current TaskState snapshot.
//
// - 404 if the task ID is unknown.
// - 409 if the task is already in a terminal state (completed,
//   failed, cancelled) — the client has nothing useful to cancel and
//   we do not want to overwrite a real outcome with "cancelled".
// - 200 + TaskState on success.
func (s *Server) handleCancelTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	s.mu.Lock()
	state, ok := s.tasks[id]
	if !ok {
		s.mu.Unlock()
		writeErr(w, http.StatusNotFound, "task %q not found", id)
		return
	}
	if _, terminal := terminalStates[state.Status]; terminal {
		snapshot := *state
		s.mu.Unlock()
		writeJSON(w, http.StatusConflict, snapshot)
		return
	}
	cancel := s.cancels[id]
	// Flip state before releasing mu so runTask's terminal-phase
	// check observes "cancelled" even if Execute has already returned.
	state.Status = taskStatusCancelled
	now := time.Now().UTC()
	state.CompletedAt = &now
	snapshot := *state
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	s.emitTaskEvent(state, taskStatusCancelled, true)
	writeJSON(w, http.StatusOK, snapshot)
}

// handleTaskEvents implements GET /api/task/{id}/events. Opens a
// Server-Sent Events stream, replays the task's current state as a
// priming frame (so late subscribers always see at least one event),
// then forwards every subsequent lifecycle event until the task
// reaches a terminal state. Honors client disconnects via the
// request context.
//
// Content-Type: text/event-stream
// Cache-Control: no-cache
// Each frame:  data: <json>\n\n
// Closing frame: event: end\ndata: {}\n\n
func (s *Server) handleTaskEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	s.mu.Lock()
	state, exists := s.tasks[id]
	if !exists {
		s.mu.Unlock()
		writeErr(w, http.StatusNotFound, "task %q not found", id)
		return
	}
	// Prime with the current snapshot so clients that connect after
	// "queued" still receive the task's latest observable state.
	primer := taskEvent{Kind: state.Status, State: *state}
	if _, terminal := terminalStates[state.Status]; terminal {
		primer.Terminal = true
	}
	// Subscribe before releasing mu so we cannot miss events that
	// would otherwise race between the snapshot and subscription.
	ch := make(chan taskEvent, sseBufferSize)
	if !primer.Terminal {
		s.subs[id] = append(s.subs[id], ch)
	}
	s.mu.Unlock()

	// SSE headers must go out before the first flush.
	hdr := w.Header()
	hdr.Set("Content-Type", "text/event-stream")
	hdr.Set("Cache-Control", "no-cache")
	hdr.Set("Connection", "keep-alive")
	hdr.Set("X-Accel-Buffering", "no") // disable nginx buffering if fronted
	w.WriteHeader(http.StatusOK)

	writeSSE := func(ev taskEvent) bool {
		body, err := json.Marshal(ev)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", body); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	closeStream := func() {
		_, _ = w.Write([]byte("event: end\ndata: {}\n\n"))
		flusher.Flush()
	}

	// Prime frame.
	if !writeSSE(primer) {
		s.unsubscribe(id, ch)
		return
	}
	if primer.Terminal {
		closeStream()
		return
	}

	// Live tail.
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			s.unsubscribe(id, ch)
			return
		case ev, ok := <-ch:
			if !ok {
				// Channel closed by emitTaskEvent after a terminal
				// transition. No further events will arrive.
				closeStream()
				return
			}
			if !writeSSE(ev) {
				s.unsubscribe(id, ch)
				return
			}
			if ev.Terminal {
				// emitTaskEvent closes the channel after a terminal
				// event; drain it so we exit cleanly on the next
				// iteration without blocking.
				closeStream()
				s.unsubscribe(id, ch)
				return
			}
		}
	}
}

// unsubscribe removes ch from the subscriber list for id. Idempotent;
// safe to call after the channel has already been closed by
// emitTaskEvent.
func (s *Server) unsubscribe(id string, ch chan taskEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.subs[id]
	for i, c := range list {
		if c == ch {
			s.subs[id] = append(list[:i], list[i+1:]...)
			if len(s.subs[id]) == 0 {
				delete(s.subs, id)
			}
			break
		}
	}
}

// emitTaskEvent fans a lifecycle event out to every live SSE
// subscriber for this task, and (when configured) mirrors the event
// to the durable eventlog + bus so external observers and crash
// recovery can reconstruct history.
//
// terminal == true closes each subscriber's channel after the event
// is delivered so the SSE writer returns without further polling.
// The subscriber list itself is cleared.
func (s *Server) emitTaskEvent(state *TaskState, kind string, terminal bool) {
	s.mu.Lock()
	snapshot := *state
	ev := taskEvent{Kind: kind, State: snapshot, Terminal: terminal}
	subs := s.subs[state.ID]
	if terminal {
		delete(s.subs, state.ID)
	}
	s.mu.Unlock()

	for _, ch := range subs {
		// Non-blocking send: a full buffer means a pathologically slow
		// client. Drop rather than block the lifecycle transition;
		// spec calls for rate limits upstream of this layer anyway.
		select {
		case ch <- ev:
		default:
		}
		if terminal {
			// Safe: we removed subs[state.ID] under the lock so no
			// concurrent send can race with this close.
			close(ch)
		}
	}

	// Optional mirror to durable eventlog.
	if s.cfg.EventLog != nil && s.cfg.Bus != nil {
		payload, err := json.Marshal(struct {
			State TaskState `json:"state"`
		}{State: snapshot})
		if err == nil {
			_ = eventlog.EmitBus(s.cfg.Bus, s.cfg.EventLog, bus.Event{
				Type:  bus.EventType("agentserve.task." + kind),
				Scope: bus.Scope{TaskID: state.ID},
				Payload: payload,
			})
		}
	}
}

