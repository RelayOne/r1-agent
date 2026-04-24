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
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/google/uuid"
)

// StokeProtocolVersion is the version string populated onto every
// EmitStoke event under the `stoke_version` envelope field. r1-server
// and any future STOKE-aware consumer use this to gate feature flags.
const StokeProtocolVersion = "1.0"

// Emitter writes Claude-Code-shape NDJSON events to w. Thread-safe —
// multiple goroutines can call Emit / EmitSystem / etc. concurrently.
type Emitter struct {
	w         io.Writer
	sessionID string
	mu        sync.Mutex
	startTime time.Time
	enabled   bool

	// STOKE envelope (RS-6). Populated at Emitter construction time
	// via SetStokeMeta; used by EmitStoke to stamp every Stoke-family
	// event with protocol version + instance + W3C trace context. The
	// envelope is additive: when the fields are empty, EmitStoke
	// omits them and CloudSwarm / pre-STOKE consumers see the
	// original shape unchanged.
	stokeMeta stokeMeta
}

// stokeMeta groups the STOKE envelope fields tied to an Emitter.
// Guarded by Emitter.mu alongside writer state.
type stokeMeta struct {
	version     string // protocol version, e.g. "1.0"
	instanceID  string // r1-<8hex> from session.Signature
	traceParent string // W3C Trace Context string
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

// SetStokeMeta configures the STOKE envelope fields (RS-6) populated
// on every EmitStoke call. Callers typically invoke this once at
// Emitter construction in main.go after the session signature's
// instance_id + fresh traceparent are available. Empty strings
// disable the corresponding envelope field.
func (e *Emitter) SetStokeMeta(version, instanceID, traceParent string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.stokeMeta = stokeMeta{
		version:     version,
		instanceID:  instanceID,
		traceParent: traceParent,
	}
}

// NewTraceParent generates a W3C Trace Context `traceparent` string
// per https://www.w3.org/TR/trace-context-1/#traceparent-header:
//
//	version "-" trace-id "-" parent-id "-" trace-flags
//	00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01
//
// Returns a freshly randomized trace+span pair with the "sampled"
// flag bit set. crypto/rand.Read never fails on Unix/Windows in
// practice — if it somehow does, we fall back to a deterministic
// zero trace so the format stays valid for downstream parsers.
func NewTraceParent() string {
	var trace [16]byte
	var span [8]byte
	// Ignore errors: crypto/rand.Read on Linux/Darwin reads from
	// /dev/urandom which never fails in practice; on an exceptional
	// platform we'd fall back to the zero trace which is still
	// RFC-W3C-valid. Explicit `_ =` satisfies staticcheck SA9003
	// without the empty-branch anti-pattern.
	_, _ = rand.Read(trace[:])
	_, _ = rand.Read(span[:])
	return fmt.Sprintf("00-%s-%s-01",
		hex.EncodeToString(trace[:]),
		hex.EncodeToString(span[:]))
}

// AddWriter routes subsequent events to both the existing writer and
// the additional w. This lets callers attach a file tee (e.g.
// .stoke/stream.jsonl) for the r1-server scanner after the emitter
// has already been constructed bound to os.Stdout. Safe for
// concurrent use — the internal mutex guards writer swaps.
func (e *Emitter) AddWriter(w io.Writer) {
	if e == nil || w == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.w == nil {
		e.w = w
		return
	}
	e.w = io.MultiWriter(e.w, w)
}

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

// EmitPolicyCheck records a policy gate pass-through for audit. Emitted
// on every Allow verdict (per-tool-call grain). Called by the sow_native
// policy hook (POL-7) after Client.Check returns Decision=Allow.
//
// Shape: type=system, subtype=policy.check, with the four audit keys
// under the `_stoke.dev/policy.*` namespace so Claude-Code-only
// consumers see a pure system event and STOKE-aware consumers can
// pick up backend + latency + reason count for triage.
func (e *Emitter) EmitPolicyCheck(decision string, latencyMs int, reasonsCount int, backend string) {
	if !e.Enabled() {
		return
	}
	e.EmitSystem("policy.check", map[string]any{
		"_stoke.dev/policy.decision":      decision,
		"_stoke.dev/policy.latency_ms":    latencyMs,
		"_stoke.dev/policy.reasons_count": reasonsCount,
		"_stoke.dev/policy.backend":       backend,
	})
}

// EmitPolicyDenied records a policy denial for audit + operator triage.
// Emitted on Deny verdicts only; includes the PARC identifiers so
// operators can correlate with the rule that fired.
//
// Shape: type=system, subtype=policy.denied, with reasons / principal
// / action / resource under the `_stoke.dev/policy.*` namespace.
func (e *Emitter) EmitPolicyDenied(reasons []string, principal, action, resource string) {
	if !e.Enabled() {
		return
	}
	e.EmitSystem("policy.denied", map[string]any{
		"_stoke.dev/policy.reasons":   reasons,
		"_stoke.dev/policy.principal": principal,
		"_stoke.dev/policy.action":    action,
		"_stoke.dev/policy.resource":  resource,
	})
}

// EmitOperator emits a Claude-Code-shape "system" event with subtype
// "stoke.operator.<verb>" for every operator action taken through the
// sessionctl handlers (approve / override / pause / resume / budget_add
// / inject). The durable eventID from the local event bus is threaded
// onto the event under "_stoke.dev/operator.event_id" so downstream
// consumers (r1-server dashboard, audit log tail) can cross-reference
// the streamjson record with the bus's authoritative event.
//
// verb is the short suffix ("approve", "pause", ...). Callers that
// hold the bus-style "operator.<verb>" kind should strip the
// "operator." prefix before invoking; the sessionctl mirror does this
// automatically.
//
// payload is marshaled as JSON and embedded under
// "_stoke.dev/operator.payload" as a RawMessage so it survives
// re-marshaling without field-order churn. When payload is nil the
// key is omitted.
func (e *Emitter) EmitOperator(verb string, payload any, eventID string) {
	if !e.Enabled() {
		return
	}
	extra := map[string]any{
		"_stoke.dev/operator.verb": verb,
	}
	if eventID != "" {
		extra["_stoke.dev/operator.event_id"] = eventID
	}
	if payload != nil {
		if raw, err := json.Marshal(payload); err == nil {
			extra["_stoke.dev/operator.payload"] = json.RawMessage(raw)
		}
	}
	e.EmitSystem("stoke.operator."+verb, extra)
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

// EmitStoke writes a Stoke-family NDJSON event carrying the STOKE
// envelope (RS-6): stoke_version, instance_id, trace_parent, plus
// the familiar type/uuid/session_id/ts fields. Call sites pass
// eventType strings in the "stoke.*" namespace (e.g. stoke.session.start,
// stoke.descent.tier) and a data map that becomes the event body.
//
// The optional key "ledger_node_id" in data is lifted into the
// envelope and kept at top level so r1-server can join against
// the ledger without parsing the event body.
//
// Envelope fields only appear when SetStokeMeta has populated them
// (the default Emitter leaves them empty, keeping backward-compat
// for non-STOKE-aware consumers like the first CloudSwarm release).
func (e *Emitter) EmitStoke(eventType string, data map[string]any) {
	if !e.Enabled() {
		return
	}
	e.mu.Lock()
	meta := e.stokeMeta
	e.mu.Unlock()

	evt := map[string]any{
		"type":       eventType,
		"uuid":       uuid.NewString(),
		"session_id": e.sessionID,
		"ts":         time.Now().UTC().Format(time.RFC3339Nano),
	}
	if meta.version != "" {
		// S3-3 dual-emit: every EmitStoke event carries both the
		// legacy `stoke_version` key and the canonical `r1_version`
		// key with identical values during the 30-day rename window
		// (work-r1-rename.md §S3-3). Consumers can read either.
		evt["stoke_version"] = meta.version
		evt["r1_version"] = meta.version
	}
	if meta.instanceID != "" {
		evt["instance_id"] = meta.instanceID
	}
	if meta.traceParent != "" {
		evt["trace_parent"] = meta.traceParent
	}
	for k, v := range data {
		evt[k] = v
	}
	e.writeEvent(evt)
}

// SharedAuditEvent is the canonical 9-field portfolio-aligned audit
// event shape (AL-3 / SEAM-20 of work-stoke-alignment). Multica,
// CloudSwarm, and OpenACP consumers agree on these nine keys as the
// cross-product audit payload so a single collector can join stoke,
// multica, and cloudswarm streams without per-product parsers.
//
// Field semantics:
//   - ID:          event identifier (unique per event). Auto-minted
//     via crypto/rand when empty.
//   - Timestamp:   RFC3339Nano UTC string. Defaults to time.Now().UTC()
//     when empty.
//   - Type:        event kind in the "stoke.*" namespace
//     (e.g. "stoke.request.start", "stoke.descent.tier").
//   - SessionID:   stoke session identifier; callers typically pass
//     Emitter.SessionID() when omitted.
//   - AgentID:     identifier for the worker/agent emitting the event.
//   - TaskID:      identifier for the task/mission the event belongs to.
//   - Payload:     event-specific body as raw JSON so it survives
//     re-marshaling without field-order churn.
//   - Severity:    "info" | "warn" | "error". Defaults to "info"
//     when empty.
//   - TraceParent: W3C trace context string (optional). Omitted from
//     JSON when empty.
//
// Migration note: existing `stoke.request.*` and `stoke.descent.*`
// emit sites that currently use EmitSystem or EmitStoke should
// gradually adopt EmitSharedAudit so downstream collectors get a
// single consistent shape. This rollout is additive — no existing
// caller is migrated in this commit.
type SharedAuditEvent struct {
	ID          string          `json:"id"`
	Timestamp   string          `json:"ts"`
	Type        string          `json:"type"`
	SessionID   string          `json:"session_id"`
	AgentID     string          `json:"agent_id"`
	TaskID      string          `json:"task_id"`
	Payload     json.RawMessage `json:"payload"`
	Severity    string          `json:"severity"`
	TraceParent string          `json:"trace_parent,omitempty"`
}

// EmitSharedAudit writes a SharedAuditEvent (AL-3 / SEAM-20) as a
// single NDJSON record. Empty ID / Timestamp / Severity fields are
// auto-defaulted (random 16-byte hex ID, RFC3339Nano now, "info").
// The event is emitted as a flat JSON object — none of the fields are
// wrapped under `_stoke.dev/` because the 9-field shape IS the public
// contract with Multica + CloudSwarm.
//
// S1-6 dual-key emission: alongside the canonical `session_id` field,
// every emitted event also carries `stoke_session_id` (legacy) AND
// `r1_session_id` (canonical) with the identical value. This mirrors
// the RelayGate audit-event shape through the 60-day rename window
// ending 2026-06-22, so cross-product collectors (CloudSwarm,
// RelayGate, Multica) can read either key during the transition.
// Both keys are always emitted for the duration of the S1-6 dual-emit
// window — dropping the legacy key is scheduled for phase S6-2
// (2026-06-22). Per work-r1-rename.md §S1-6.
//
// Callers emitting `stoke.request.*` / `stoke.descent.*` events
// should gradually migrate to this helper; EmitStoke / EmitSystem
// remain the right choice for non-audit events.
func (e *Emitter) EmitSharedAudit(ev SharedAuditEvent) {
	if !e.Enabled() {
		return
	}
	if ev.ID == "" {
		var buf [16]byte
		if _, err := rand.Read(buf[:]); err == nil {
			ev.ID = hex.EncodeToString(buf[:])
		} else {
			// crypto/rand virtually never fails; fall back to
			// a uuid so the ID field is still populated.
			ev.ID = uuid.NewString()
		}
	}
	if ev.Timestamp == "" {
		ev.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if ev.Severity == "" {
		ev.Severity = "info"
	}

	// S1-6 dual-key: marshal the struct first, then rewrite the JSON
	// object to include `stoke_session_id` + `r1_session_id` as
	// companion fields alongside the canonical `session_id`. Using a
	// map[string]any round-trip keeps the struct definition stable and
	// the dual-key contract self-contained in the emitter — callers do
	// not need to think about the legacy/canonical split.
	raw, err := json.Marshal(ev)
	if err != nil {
		e.mu.Lock()
		defer e.mu.Unlock()
		fallback, _ := json.Marshal(map[string]any{
			"type":             "error",
			"uuid":             uuid.NewString(),
			"session_id":       e.sessionID,
			"stoke_session_id": e.sessionID,
			"r1_session_id":    e.sessionID,
			"message":          fmt.Sprintf("streamjson shared-audit marshal failed: %v", err),
		})
		fmt.Fprintln(e.w, string(fallback))
		return
	}

	var flat map[string]any
	if err := json.Unmarshal(raw, &flat); err != nil {
		// Marshal of a struct whose JSON decode back into a map fails
		// is pathological — emit the original bytes verbatim so the
		// event still lands, just without the dual-key mirror.
		e.mu.Lock()
		defer e.mu.Unlock()
		fmt.Fprintln(e.w, string(raw))
		return
	}
	flat["stoke_session_id"] = ev.SessionID
	flat["r1_session_id"] = ev.SessionID

	buf, err := json.Marshal(flat)
	if err != nil {
		e.mu.Lock()
		defer e.mu.Unlock()
		fallback, _ := json.Marshal(map[string]any{
			"type":             "error",
			"uuid":             uuid.NewString(),
			"session_id":       e.sessionID,
			"stoke_session_id": e.sessionID,
			"r1_session_id":    e.sessionID,
			"message":          fmt.Sprintf("streamjson shared-audit re-marshal failed: %v", err),
		})
		fmt.Fprintln(e.w, string(fallback))
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	fmt.Fprintln(e.w, string(buf))
}

// EmitTopLevel writes a top-level event (type != "system") — used
// by the cloudswarm-protocol spec for hitl_required, error, complete,
// mission.aborted. Kept on Emitter alongside the existing 5 helpers
// so single-lane callers can emit CloudSwarm-visible events without
// taking the TwoLane dependency.
//
// Note: callers expecting critical-lane guarantees should use
// TwoLane.EmitTopLevel; this variant is synchronous only.
func (e *Emitter) EmitTopLevel(eventType string, extra map[string]any) {
	if !e.Enabled() {
		return
	}
	evt := map[string]any{
		"type":       eventType,
		"uuid":       uuid.NewString(),
		"session_id": e.sessionID,
	}
	for k, v := range extra {
		evt[k] = v
	}
	e.writeEvent(evt)
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
