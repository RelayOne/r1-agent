// Package streamjson implements S-U-020: Claude Code-compatible
// NDJSON output mode for stoke. Emitting this schema lets Multica,
// OpenACP, and other orchestrators consume stoke output without
// custom parsers — they already speak Claude Code's event vocabulary.
//
// The five top-level event types Claude Code ships are:
//   - system (init / api_retry / compact_boundary subtypes)
//   - assistant (message.content[] carrying text / tool_use / thinking)
//   - user (tool_result content blocks)
//   - result (subtype + duration_ms + total_cost_usd + num_turns)
//   - stream_event (streaming deltas)
//
// Every event carries type, uuid, session_id per Claude Code
// convention. Stoke-specific extensions live under the
// "_stoke.dev/" namespace on relevant events so consumers that
// only parse the Claude Code fields see a superset, not a
// breaking change.
package streamjson

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Emitter writes Claude-Code-shape NDJSON events to w. Thread-safe —
// multiple goroutines can call Emit / EmitSystem / etc. concurrently.
type Emitter struct {
	w         io.Writer
	sessionID string
	mu        sync.Mutex
	startTime time.Time
	enabled   bool
}

// New constructs an Emitter bound to w with a fresh session ID.
// When enabled is false, every method is a no-op so callers can
// cheaply integrate without a branch at every call site.
func New(w io.Writer, enabled bool) *Emitter {
	return &Emitter{
		w:         w,
		sessionID: uuid.NewString(),
		startTime: time.Now(),
		enabled:   enabled,
	}
}

// SessionID returns the emitter's session identifier. External
// callers may embed this in logs or correlate with ledger nodes.
func (e *Emitter) SessionID() string { return e.sessionID }

// Enabled reports whether the emitter is active. When false,
// every Emit* call is a no-op.
func (e *Emitter) Enabled() bool { return e != nil && e.enabled }

// EmitSystem writes an event of type "system" with the given subtype
// ("init" | "api_retry" | "compact_boundary"). Extra fields are
// merged into the event's top-level JSON.
func (e *Emitter) EmitSystem(subtype string, extra map[string]any) {
	if !e.Enabled() {
		return
	}
	evt := map[string]any{
		"type":       "system",
		"subtype":    subtype,
		"uuid":       uuid.NewString(),
		"session_id": e.sessionID,
	}
	for k, v := range extra {
		evt[k] = v
	}
	e.writeEvent(evt)
}

// EmitAssistant writes an "assistant" event. content is the
// message.content[] array (caller constructs blocks with type
// "text" | "tool_use" | "thinking"). stokeFields, when non-nil, are
// attached to the event under "_stoke.dev/<key>" entries.
func (e *Emitter) EmitAssistant(content []any, stokeFields map[string]any) {
	if !e.Enabled() {
		return
	}
	evt := map[string]any{
		"type":       "assistant",
		"uuid":       uuid.NewString(),
		"session_id": e.sessionID,
		"message": map[string]any{
			"content": content,
		},
	}
	for k, v := range stokeFields {
		evt["_stoke.dev/"+k] = v
	}
	e.writeEvent(evt)
}

// EmitUser writes a "user" event with tool_result blocks.
func (e *Emitter) EmitUser(toolResults []any, stokeFields map[string]any) {
	if !e.Enabled() {
		return
	}
	evt := map[string]any{
		"type":       "user",
		"uuid":       uuid.NewString(),
		"session_id": e.sessionID,
		"message": map[string]any{
			"content": toolResults,
		},
	}
	for k, v := range stokeFields {
		evt["_stoke.dev/"+k] = v
	}
	e.writeEvent(evt)
}

// EmitResult writes the terminal "result" event and returns the
// event's uuid (useful for logging). subtype values match Claude
// Code's: "success" | "error_max_turns" | "error_during_execution"
// | "error_max_budget_usd". Stoke-specific error subtypes should
// go into _stoke.dev/error_subtype on the event body, not as the
// subtype value, to preserve consumer parsability.
func (e *Emitter) EmitResult(subtype string, totalCostUSD float64, numTurns int, resultText string, stokeFields map[string]any) string {
	if !e.Enabled() {
		return ""
	}
	id := uuid.NewString()
	evt := map[string]any{
		"type":           "result",
		"subtype":        subtype,
		"uuid":           id,
		"session_id":     e.sessionID,
		"duration_ms":    time.Since(e.startTime).Milliseconds(),
		"total_cost_usd": totalCostUSD,
		"num_turns":      numTurns,
		"result":         resultText,
	}
	for k, v := range stokeFields {
		evt["_stoke.dev/"+k] = v
	}
	e.writeEvent(evt)
	return id
}

// EmitStreamEvent writes a low-level streaming "stream_event". Used
// for delta text streams that come off the model before the full
// assistant message is available.
func (e *Emitter) EmitStreamEvent(deltaType, deltaText string) {
	if !e.Enabled() {
		return
	}
	evt := map[string]any{
		"type":       "stream_event",
		"uuid":       uuid.NewString(),
		"session_id": e.sessionID,
		"delta": map[string]any{
			"type": deltaType,
			"text": deltaText,
		},
	}
	e.writeEvent(evt)
}

// writeEvent marshals and writes one newline-terminated JSON object
// to the underlying writer under the mutex.
func (e *Emitter) writeEvent(evt map[string]any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	buf, err := json.Marshal(evt)
	if err != nil {
		// Marshaling a map[string]any should never fail with
		// well-formed input; surface via a minimal error event
		// so consumers see something went wrong on our side.
		fallback, _ := json.Marshal(map[string]any{
			"type":       "error",
			"uuid":       uuid.NewString(),
			"session_id": e.sessionID,
			"message":    fmt.Sprintf("streamjson marshal failed: %v", err),
		})
		fmt.Fprintln(e.w, string(fallback))
		return
	}
	fmt.Fprintln(e.w, string(buf))
}
