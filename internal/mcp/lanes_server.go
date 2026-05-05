// Package mcp — lanes-protocol MCP server (specs/lanes-protocol.md §7).
//
// LanesServer exposes the five lane tools over stdio JSON-RPC:
//
//   - r1.lanes.list      (read-only)
//   - r1.lanes.subscribe (streaming)
//   - r1.lanes.get       (read-only)
//   - r1.lanes.kill      (mutation, idempotent, cascades)
//   - r1.lanes.pin       (mutation, idempotent)
//
// Tool definitions carry the §7 JSON Schema draft 2020-12 documents
// verbatim as json.RawMessage in ToolDefinition.InputSchema and
// ToolDefinition.OutputSchema. Each handler implements the matching
// §7 contract: list (§7.1), subscribe (§7.2), get (§7.3), kill (§7.4),
// pin (§7.5).
//
// SECURITY (outbound sanitization policy): same as stoke_server.go.
// Tool responses are NOT pre-sanitized for prompt-injection because
// non-LLM clients consume them programmatically and different LLM
// clients apply different sanitization conventions. Downstream
// consumers that feed lane-tool output into an LLM prompt must apply
// their own injection defenses.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/RelayOne/r1/internal/cortex"
	"github.com/RelayOne/r1/internal/hub"
)

// LanesBackend is the minimal surface the LanesServer needs from the
// rest of the runtime. *cortex.Workspace satisfies this directly via
// the Lanes / GetLane / GetLaneByID accessors added in TASK-19, and
// tests pass an in-memory shim. Decoupling lets the lanes server be
// constructed and unit-tested without spinning up a full cortex round.
//
// All methods are read-only-or-idempotent so the LanesServer can run
// in parallel with the agent loop without contention.
type LanesBackend interface {
	// Lanes returns a snapshot of every lane in the session, current
	// status included. Caller may mutate the returned slice header;
	// the *Lane elements are pointers into the canonical workspace
	// store so consumers MUST NOT mutate fields directly.
	Lanes() []*cortex.Lane

	// GetLane returns the canonical *Lane for laneID, or (nil, false).
	GetLane(laneID string) (*cortex.Lane, bool)

	// SessionID returns the session identifier the workspace is bound
	// to. Empty when the workspace has not yet been bound.
	SessionID() string

	// Bus returns the in-process event hub. r1.lanes.subscribe
	// registers a subscriber against this bus. May be nil; callers
	// must guard.
	Bus() *hub.Bus
}

// LanesWAL is the optional replay surface the r1.lanes.get tool tail
// parameter consumes. Same shape as internal/server.LanesWAL but
// duplicated here so the mcp package does not depend on server.
//
// fromSeq=0 means "no replay; only future live events" and is treated
// as "return nothing" by r1.lanes.get since the tool is read-only.
type LanesWAL interface {
	ReplayLane(ctx context.Context, sessionID string, fromSeq uint64, handler func(*hub.Event) error) error
}

// LanesServer is an MCP tool server exposing the five lane tools.
type LanesServer struct {
	backend LanesBackend
	wal     LanesWAL

	// mu guards nextSubID; the live subscriber map lives on the
	// hub.Bus, not here. Each Subscribe call returns a cancel func
	// so callers can tear down without server-side bookkeeping.
	mu        sync.Mutex
	nextSubID uint64
}

// NewLanesServer constructs a LanesServer bound to the given backend.
// wal may be nil (r1.lanes.get tail returns empty in that case).
func NewLanesServer(backend LanesBackend, wal LanesWAL) *LanesServer {
	return &LanesServer{backend: backend, wal: wal}
}

// ToolDefinitions returns the five §7 lane tool definitions.
// Schemas are embedded verbatim from specs/lanes-protocol.md §7 so
// MCP clients see exactly the contract documented in the spec.
func (s *LanesServer) ToolDefinitions() []ToolDefinition {
	return []ToolDefinition{
		{
			Name:         lanesListToolName,
			Description:  "List all lanes in a session, current state, parent links, and last seq emitted. Read-only.",
			InputSchema:  json.RawMessage(lanesListInputSchema),
			OutputSchema: json.RawMessage(lanesListOutputSchema),
		},
		{
			Name:         lanesSubscribeToolName,
			Description:  "Subscribe to live lane events for one session. Streaming. Replays from since_seq if provided; else starts from a snapshot of currently-active lanes.",
			InputSchema:  json.RawMessage(lanesSubscribeInputSchema),
			OutputSchema: json.RawMessage(lanesSubscribeOutputSchema),
		},
		{
			Name:         lanesGetToolName,
			Description:  "Fetch a single lane's current state and an optional bounded tail of its recent events. Read-only.",
			InputSchema:  json.RawMessage(lanesGetInputSchema),
			OutputSchema: json.RawMessage(lanesGetOutputSchema),
		},
		{
			Name:         lanesKillToolName,
			Description:  "Cancel a running or pending lane. Cascades to descendant lanes. Idempotent.",
			InputSchema:  json.RawMessage(lanesKillInputSchema),
			OutputSchema: json.RawMessage(lanesKillOutputSchema),
		},
		{
			Name:         lanesPinToolName,
			Description:  "Set or clear the pinned flag on a lane. Pinned lanes render above unpinned ones across all surfaces. Idempotent.",
			InputSchema:  json.RawMessage(lanesPinInputSchema),
			OutputSchema: json.RawMessage(lanesPinOutputSchema),
		},
	}
}

// Canonical tool names per spec §7. Pinned as constants so the
// HandleToolCall switch and the ToolDefinitions table never drift.
const (
	lanesListToolName      = "r1.lanes.list"
	lanesSubscribeToolName = "r1.lanes.subscribe"
	lanesGetToolName       = "r1.lanes.get"
	lanesKillToolName      = "r1.lanes.kill"
	lanesPinToolName       = "r1.lanes.pin"
)

// HandleToolCall dispatches a non-streaming tool invocation. Streaming
// (r1.lanes.subscribe) flows through Subscribe; calling HandleToolCall
// with that name returns an invalid_request envelope pointing the
// caller at Subscribe so the streaming guarantee is not silently
// downgraded.
func (s *LanesServer) HandleToolCall(ctx context.Context, toolName string, args map[string]interface{}) (string, error) {
	switch toolName {
	case lanesListToolName:
		return s.handleList(ctx, args)
	case lanesGetToolName:
		return s.handleGet(ctx, args)
	case lanesKillToolName:
		return s.handleKill(ctx, args)
	case lanesPinToolName:
		return s.handlePin(ctx, args)
	case lanesSubscribeToolName:
		// Streaming tool; non-streaming dispatch returns a structured
		// not-supported envelope so callers know to use the streaming
		// path. The tool does NOT degrade to a one-shot snapshot here
		// because that would silently drop the streaming guarantee.
		return s.envelopeError("invalid_request", "r1.lanes.subscribe is a streaming tool; use Subscribe"), nil
	default:
		return "", fmt.Errorf("unknown tool: %s", toolName)
	}
}

// handleList implements r1.lanes.list per spec §7.1 (TASK-19). Reads
// from the cortex Workspace via LanesBackend.Lanes() and projects each
// Lane into the §7.1 output_schema shape.
//
// Filtering:
//   - session_id is required; if it does not match the workspace's
//     bound session id, the result is an empty lanes array (NOT an
//     error — surfaces poll many sessions and should silently skip
//     non-matching ones).
//   - include_terminal=false drops done/errored/cancelled lanes.
//   - kinds filters by lane kind (main|lobe|tool|mission_task|router).
//   - limit caps the array length (default 100, max 500).
//
// The returned lanes are sorted by StartedAt ascending so consumers
// see the call-graph in creation order regardless of map iteration.
func (s *LanesServer) handleList(_ context.Context, args map[string]interface{}) (string, error) {
	if s.backend == nil {
		return s.envelopeError("internal", "lanes backend not configured"), nil
	}

	sessionID, _ := args["session_id"].(string)
	if sessionID == "" {
		return s.envelopeError("invalid_request", "session_id is required"), nil
	}

	includeTerminal := true
	if v, ok := args["include_terminal"].(bool); ok {
		includeTerminal = v
	}

	var kindsFilter map[string]bool
	if v, ok := args["kinds"].([]interface{}); ok && len(v) > 0 {
		kindsFilter = make(map[string]bool, len(v))
		for _, kv := range v {
			if s, ok := kv.(string); ok {
				kindsFilter[s] = true
			}
		}
	}

	limit := 100
	if v, ok := args["limit"].(float64); ok {
		limit = int(v)
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 500 {
		limit = 500
	}

	// Session-id filter: surfaces typically poll a single session, so
	// returning an empty list (rather than an error) for a session_id
	// that doesn't match the backend keeps the caller's polling loop
	// simple. The MCP server is constructed per-session per spec §8.2,
	// so this is a defensive check, not a routing one.
	if backendSession := s.backend.SessionID(); backendSession != "" && backendSession != sessionID {
		return s.envelopeOK(map[string]any{"lanes": []any{}}), nil
	}

	lanes := s.backend.Lanes()

	out := make([]map[string]any, 0, len(lanes))
	for _, l := range lanes {
		if l == nil {
			continue
		}
		if !includeTerminal && l.IsTerminal() {
			continue
		}
		if kindsFilter != nil && !kindsFilter[string(l.Kind)] {
			continue
		}
		entry := map[string]any{
			"lane_id":    l.ID,
			"kind":       string(l.Kind),
			"status":     string(l.Status),
			"started_at": l.StartedAt.UTC().Format("2006-01-02T15:04:05.000000000Z07:00"),
			"pinned":     l.Pinned,
			"last_seq":   l.LastSeq,
		}
		if l.Label != "" {
			entry["label"] = l.Label
		}
		if l.Kind == hub.LaneKindLobe && l.Label != "" {
			entry["lobe_name"] = l.Label
		}
		if l.ParentID != "" {
			entry["parent_lane_id"] = l.ParentID
		}
		if !l.EndedAt.IsZero() {
			entry["ended_at"] = l.EndedAt.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
		}
		out = append(out, entry)
	}

	// Sort by started_at ascending. Stable on equal timestamps so the
	// canonical workspace ordering breaks ties deterministically when
	// two lanes share a millisecond.
	sortLanesByStartedAt(out)

	if len(out) > limit {
		out = out[:limit]
	}

	return s.envelopeOK(map[string]any{"lanes": out}), nil
}

// sortLanesByStartedAt sorts in-place by the started_at field. Lanes
// without a started_at sort first (defensive; spec §7.1 marks the
// field required so this branch should never fire on real data).
func sortLanesByStartedAt(lanes []map[string]any) {
	// Insertion sort: tiny lists (lanes per session typically <50) and
	// avoiding the sort.Slice closure allocation matters in the hot
	// path of r1.lanes.list polling.
	for i := 1; i < len(lanes); i++ {
		j := i
		for j > 0 {
			a, _ := lanes[j-1]["started_at"].(string)
			b, _ := lanes[j]["started_at"].(string)
			if a <= b {
				break
			}
			lanes[j-1], lanes[j] = lanes[j], lanes[j-1]
			j--
		}
	}
}

// handleGet implements r1.lanes.get per spec §7.3 (TASK-21). Returns
// the current snapshot of one lane plus an optional bounded tail of
// recent events read from the WAL.
//
// tail=0 (or no WAL configured) returns the snapshot only. tail>0 with
// a configured WAL replays the lane's events backwards from now and
// returns the most recent N. Events are returned in chronological
// (oldest-first) order so consumers can render them as a transcript
// without reversing.
func (s *LanesServer) handleGet(ctx context.Context, args map[string]interface{}) (string, error) {
	if s.backend == nil {
		return s.envelopeError("internal", "lanes backend not configured"), nil
	}
	sessionID, _ := args["session_id"].(string)
	if sessionID == "" {
		return s.envelopeError("invalid_request", "session_id is required"), nil
	}
	laneID, _ := args["lane_id"].(string)
	if laneID == "" {
		return s.envelopeError("invalid_request", "lane_id is required"), nil
	}
	tail := 0
	if v, ok := args["tail"].(float64); ok {
		tail = int(v)
	}
	if tail < 0 {
		tail = 0
	}
	if tail > 500 {
		tail = 500
	}

	// Session-id mismatch returns not_found rather than empty so the
	// caller distinguishes "you asked the wrong server" from "lane
	// genuinely doesn't exist". Spec §7.3 lists no enum constraint on
	// error_code for this tool, so the bare not_found is fine.
	if backendSession := s.backend.SessionID(); backendSession != "" && backendSession != sessionID {
		return s.envelopeError("not_found", fmt.Sprintf("session %q not bound to this lanes server", sessionID)), nil
	}

	l, ok := s.backend.GetLane(laneID)
	if !ok || l == nil {
		return s.envelopeError("not_found", fmt.Sprintf("lane %q not found", laneID)), nil
	}

	laneSnapshot := map[string]any{
		"lane_id":    l.ID,
		"kind":       string(l.Kind),
		"status":     string(l.Status),
		"started_at": l.StartedAt.UTC().Format("2006-01-02T15:04:05.000000000Z07:00"),
		"pinned":     l.Pinned,
		"last_seq":   l.LastSeq,
	}
	if l.Label != "" {
		laneSnapshot["label"] = l.Label
	}
	if l.Kind == hub.LaneKindLobe && l.Label != "" {
		laneSnapshot["lobe_name"] = l.Label
	}
	if l.ParentID != "" {
		laneSnapshot["parent_lane_id"] = l.ParentID
	}
	if !l.EndedAt.IsZero() {
		laneSnapshot["ended_at"] = l.EndedAt.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
	}

	data := map[string]any{"lane": laneSnapshot}

	if tail > 0 && s.wal != nil {
		tailEvents, err := s.fetchTail(ctx, sessionID, laneID, tail)
		if err != nil {
			return s.envelopeError("internal", "tail fetch failed: "+err.Error()), nil
		}
		data["tail"] = tailEvents
	} else {
		data["tail"] = []map[string]any{}
	}

	return s.envelopeOK(data), nil
}

// fetchTail walks the WAL for the given lane in seq order, keeping the
// most recent `limit` matching events, then returns them in oldest-
// first order. Implements spec §7.3 "optional bounded tail of its
// recent events".
//
// We replay from seq=1 (the floor immediately after the synthetic
// session.bound). For lanes with very long histories this could be
// expensive; production callers should keep `tail` <= 500 (enforced
// by the spec). A future optimisation could index the WAL by lane_id;
// not in scope for TASK-21.
func (s *LanesServer) fetchTail(ctx context.Context, sessionID, laneID string, limit int) ([]map[string]any, error) {
	// Ring buffer to retain only the most-recent `limit` matches.
	ring := make([]map[string]any, 0, limit)
	err := s.wal.ReplayLane(ctx, sessionID, 1, func(ev *hub.Event) error {
		if ev == nil || ev.Lane == nil || ev.Lane.LaneID != laneID {
			return nil
		}
		entry := map[string]any{
			"event":      string(ev.Type),
			"event_id":   ev.ID,
			"session_id": ev.Lane.SessionID,
			"lane_id":    ev.Lane.LaneID,
			"seq":        ev.Lane.Seq,
			"at":         laneEventAt(ev),
			"data":       buildLaneEventData(ev),
		}
		if len(ring) < limit {
			ring = append(ring, entry)
		} else {
			// Slide the window: drop the oldest, append the newest.
			copy(ring, ring[1:])
			ring[len(ring)-1] = entry
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ring, nil
}

// handleKill implements r1.lanes.kill per spec §7.4 (TASK-22). Cancels
// a running or pending lane; cascades to descendants by default.
//
// Idempotent: invoking on a terminal lane returns
// {ok:true, data:{killed_lane_ids:[], already_terminal:true}}. The
// MCP server is the operator-facing path so the killed_lane_ids list
// is empty in the idempotent case (no events emitted) — surfaces use
// the already_terminal flag to suppress kill animations.
//
// Cascade walks the parent_id graph BFS-style. Each cancelled lane
// emits lane.killed (actor=operator) followed by the terminal
// lane.status(cancelled_by_operator). The reason argument is surfaced
// in lane.killed.data.reason verbatim.
func (s *LanesServer) handleKill(_ context.Context, args map[string]interface{}) (string, error) {
	if s.backend == nil {
		return s.envelopeError("internal", "lanes backend not configured"), nil
	}
	sessionID, _ := args["session_id"].(string)
	if sessionID == "" {
		// Spec §7.4 error_code enum: [not_found, permission_denied,
		// internal]. Validation errors map to "internal" so we stay
		// inside the enum.
		return s.envelopeError("internal", "session_id is required"), nil
	}
	laneID, _ := args["lane_id"].(string)
	if laneID == "" {
		return s.envelopeError("internal", "lane_id is required"), nil
	}
	reason, _ := args["reason"].(string)
	cascade := true
	if v, ok := args["cascade"].(bool); ok {
		cascade = v
	}

	if backendSession := s.backend.SessionID(); backendSession != "" && backendSession != sessionID {
		return s.envelopeError("not_found", fmt.Sprintf("session %q not bound to this lanes server", sessionID)), nil
	}

	root, ok := s.backend.GetLane(laneID)
	if !ok || root == nil {
		return s.envelopeError("not_found", fmt.Sprintf("lane %q not found", laneID)), nil
	}

	// Idempotent terminal short-circuit. Spec §7.4 mandates the exact
	// payload shape: {ok:true, data:{already_terminal:true}}.
	if root.IsTerminal() {
		return s.envelopeOK(map[string]any{
			"already_terminal": true,
			"killed_lane_ids":  []string{},
		}), nil
	}

	// Walk descendants when cascading. Children look up by ParentID;
	// we collect the targets before killing so a child whose parent is
	// already-cancelled-by-cascade doesn't see its own kill error.
	targets := []*cortex.Lane{root}
	if cascade {
		targets = append(targets, s.descendantsOf(root.ID)...)
	}

	killed := make([]string, 0, len(targets))
	for _, l := range targets {
		if l == nil || l.IsTerminal() {
			continue
		}
		if err := l.Kill(reason); err != nil {
			// One failed kill should not stop the cascade — partial
			// success is documented behaviour (the operator can re-
			// invoke to clean up; the second invocation hits the
			// idempotent path for already-killed children).
			continue
		}
		killed = append(killed, l.ID)
	}

	return s.envelopeOK(map[string]any{
		"killed_lane_ids":  killed,
		"already_terminal": false,
	}), nil
}

// descendantsOf returns every lane whose parent chain leads back to
// rootID, in BFS order. Used by handleKill to collect cascade targets
// before mutating any of them so a transient ParentID race does not
// cause the cascade to miss a child.
//
// Walk is safe under the workspace mutex because Lanes() takes the
// read lock and copies the pointer list; subsequent reads of l.ID and
// l.ParentID are fields on the canonical workspace records (no struct
// copying), but those fields are immutable after lane creation
// (ParentID is set once at NewLane and never re-written) so the read
// is safe.
func (s *LanesServer) descendantsOf(rootID string) []*cortex.Lane {
	if s.backend == nil {
		return nil
	}
	all := s.backend.Lanes()

	// Build child index: parent_id -> []*Lane.
	children := make(map[string][]*cortex.Lane, len(all))
	for _, l := range all {
		if l == nil || l.ParentID == "" {
			continue
		}
		children[l.ParentID] = append(children[l.ParentID], l)
	}

	// BFS from rootID.
	out := []*cortex.Lane{}
	queue := []string{rootID}
	seen := map[string]bool{rootID: true}
	for len(queue) > 0 {
		head := queue[0]
		queue = queue[1:]
		for _, child := range children[head] {
			if child == nil || seen[child.ID] {
				continue
			}
			seen[child.ID] = true
			out = append(out, child)
			queue = append(queue, child.ID)
		}
	}
	return out
}

// handlePin implements r1.lanes.pin per spec §7.5 (TASK-23). Sets or
// clears the pinned flag on a lane. Idempotent — setting pinned to
// its current value still returns ok:true.
//
// Per spec: this MUST NOT emit an event. Surfaces re-fetch via
// r1.lanes.list (cheap) when their pin command returns ok. The
// rationale is that pinning is a UI affordance, not a workflow event;
// adding it to the event stream would inflate the WAL with state
// noise that every consumer would discard.
func (s *LanesServer) handlePin(_ context.Context, args map[string]interface{}) (string, error) {
	if s.backend == nil {
		return s.envelopeError("internal", "lanes backend not configured"), nil
	}
	sessionID, _ := args["session_id"].(string)
	if sessionID == "" {
		return s.envelopeError("internal", "session_id is required"), nil
	}
	laneID, _ := args["lane_id"].(string)
	if laneID == "" {
		return s.envelopeError("internal", "lane_id is required"), nil
	}
	pinnedRaw, ok := args["pinned"]
	if !ok {
		return s.envelopeError("internal", "pinned is required"), nil
	}
	pinned, ok := pinnedRaw.(bool)
	if !ok {
		return s.envelopeError("internal", "pinned must be a boolean"), nil
	}

	if backendSession := s.backend.SessionID(); backendSession != "" && backendSession != sessionID {
		return s.envelopeError("not_found", fmt.Sprintf("session %q not bound to this lanes server", sessionID)), nil
	}

	l, ok := s.backend.GetLane(laneID)
	if !ok || l == nil {
		return s.envelopeError("not_found", fmt.Sprintf("lane %q not found", laneID)), nil
	}

	// LaneBackend gives us a *cortex.Lane; SetPinned is exported on
	// that type and goroutine-safe via the workspace mutex. The
	// fakeLanesBackend used in tests holds a *cortex.Lane directly so
	// the mutation flows through too.
	l.SetPinned(pinned)

	return s.envelopeOK(map[string]any{
		"lane_id": l.ID,
		"pinned":  pinned,
	}), nil
}

// SubscribeArgs is the typed shape of the r1.lanes.subscribe input
// arguments. ServeStdio decodes the json.RawMessage of `params` into
// this struct before invoking Subscribe so the streaming path stays
// type-safe. External callers (in-process integrations) populate it
// directly.
type SubscribeArgs struct {
	SessionID string   `json:"session_id"`
	SinceSeq  uint64   `json:"since_seq,omitempty"`
	LaneIDs   []string `json:"lane_ids,omitempty"`
	Kinds     []string `json:"kinds,omitempty"`
	Events    []string `json:"events,omitempty"`
}

// LaneStreamChunk is one item produced by Subscribe. It is either:
//
//   - a per-event chunk carrying the full §4 wire body (Event != nil
//     and Final == false); or
//   - the synthetic floor marker emitted FIRST per spec §5.5 (Event
//     != nil with Type=="session.bound" at seq=0); or
//   - the final envelope per §7.2 ({ok, data:{ended:true,reason}})
//     when Final == true.
//
// Consumers iterate until Final is observed or the channel is closed.
type LaneStreamChunk struct {
	// Event carries the §4 lane event body when Final == false.
	// Encoded by Marshal as the §5.2 envelope sub-object.
	Event *hub.Event `json:"-"`

	// Final is true on the terminating envelope. Reason carries the
	// shutdown cause: "context_cancelled", "client_unsubscribed", or
	// any other free-form reason set by the implementation.
	Final  bool   `json:"final,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Marshal serialises the chunk to its on-the-wire JSON-RPC notification
// body per spec §5.2 (event chunks) and §7.2 (final envelope).
func (c *LaneStreamChunk) Marshal() []byte {
	if c.Final {
		body, _ := json.Marshal(map[string]any{
			"ok": true,
			"data": map[string]any{
				"ended":  true,
				"reason": c.Reason,
			},
		})
		return body
	}
	if c.Event == nil {
		return []byte("{}")
	}
	// session.bound synthetic first-chunk has no Lane payload; emit
	// the minimum envelope so consumers can detect the floor seq.
	if c.Event.Type == "session.bound" {
		body, _ := json.Marshal(map[string]any{
			"event":      "session.bound",
			"event_id":   c.Event.ID,
			"session_id": laneEventSessionID(c.Event),
			"seq":        uint64(0),
			"at":         laneEventAt(c.Event),
			"data":       map[string]any{},
		})
		return body
	}
	if c.Event.Lane == nil {
		return []byte("{}")
	}
	body, _ := json.Marshal(map[string]any{
		"event":      string(c.Event.Type),
		"event_id":   c.Event.ID,
		"session_id": c.Event.Lane.SessionID,
		"lane_id":    c.Event.Lane.LaneID,
		"seq":        c.Event.Lane.Seq,
		"at":         laneEventAt(c.Event),
		"data":       buildLaneEventData(c.Event),
	})
	return body
}

// laneEventSessionID returns the session id from the lane payload or
// from envelope custom fields (session.bound has no Lane payload).
func laneEventSessionID(ev *hub.Event) string {
	if ev == nil {
		return ""
	}
	if ev.Lane != nil && ev.Lane.SessionID != "" {
		return ev.Lane.SessionID
	}
	if v, ok := ev.Custom["session_id"].(string); ok {
		return v
	}
	return ""
}

// laneEventAt formats the event timestamp per spec §4 (RFC 3339 nano).
func laneEventAt(ev *hub.Event) string {
	if ev == nil || ev.Timestamp.IsZero() {
		return ""
	}
	return ev.Timestamp.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
}

// Subscribe implements r1.lanes.subscribe per spec §7.2 (TASK-20). It
// registers a hub subscriber against the same bus that streamjson and
// internal/server use, and pushes matching events into a buffered
// channel. The returned cancel func unregisters the subscriber and
// closes the channel.
//
// Snapshot semantics (since_seq=0):
//
//   - emits a synthetic session.bound chunk first (seq=0) per spec
//     §5.5;
//   - emits a lane.created chunk for every currently-active lane
//     (one-shot snapshot) per spec §6.2;
//   - then forwards live events.
//
// Replay semantics (since_seq>0):
//
//   - WAL replay is owned by the server-side handler (internal/server
//     drives this end). The MCP server only forwards live events
//     beyond since_seq; clients that need WAL replay should use the
//     WS transport (spec §6.2). This keeps the MCP server thin —
//     the bus subscription IS the live pipe; the WAL adapter wraps
//     it elsewhere.
//
// Filtering (lane_ids / kinds / events) is applied per-event before
// delivery so the channel doesn't carry chaff the consumer would
// drop anyway.
//
// When ctx is cancelled the subscriber is unregistered and the
// channel is closed; consumers MUST observe channel close or the
// final chunk to avoid leaking the goroutine that feeds it.
func (s *LanesServer) Subscribe(ctx context.Context, args SubscribeArgs) (<-chan LaneStreamChunk, func(), error) {
	if s.backend == nil {
		return nil, nil, fmt.Errorf("lanes backend not configured")
	}
	if args.SessionID == "" {
		return nil, nil, fmt.Errorf("session_id is required")
	}
	bus := s.backend.Bus()
	if bus == nil {
		return nil, nil, fmt.Errorf("lanes backend has no event bus")
	}

	// Build allow-sets once so the per-event filter is O(1).
	var laneIDFilter map[string]bool
	if len(args.LaneIDs) > 0 {
		laneIDFilter = make(map[string]bool, len(args.LaneIDs))
		for _, id := range args.LaneIDs {
			laneIDFilter[id] = true
		}
	}
	var kindsFilter map[string]bool
	if len(args.Kinds) > 0 {
		kindsFilter = make(map[string]bool, len(args.Kinds))
		for _, k := range args.Kinds {
			kindsFilter[k] = true
		}
	}
	var eventsFilter map[string]bool
	if len(args.Events) > 0 {
		eventsFilter = make(map[string]bool, len(args.Events))
		for _, e := range args.Events {
			eventsFilter[e] = true
		}
	}

	// Allocate a unique subscriber id so concurrent Subscribe calls
	// don't collide on the bus's dedup-by-id check.
	s.mu.Lock()
	s.nextSubID++
	subID := fmt.Sprintf("mcp.lanes.subscribe:%s:%d", args.SessionID, s.nextSubID)
	s.mu.Unlock()

	out := make(chan LaneStreamChunk, 256)
	var (
		closeOnce sync.Once
		closeCh   = func() { closeOnce.Do(func() { close(out) }) }
	)

	// Register the hub subscriber. ModeObserve so it never blocks the
	// publisher; on overflow we drop the event and emit a protocol
	// violation in logs (caller can detect via gap in seq).
	bus.Register(hub.Subscriber{
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
		Priority: 9400,
		Handler: func(_ context.Context, ev *hub.Event) *hub.HookResponse {
			if ev == nil || ev.Lane == nil {
				return &hub.HookResponse{Decision: hub.Allow}
			}
			if ev.Lane.SessionID != args.SessionID {
				return &hub.HookResponse{Decision: hub.Allow}
			}
			if args.SinceSeq > 0 && ev.Lane.Seq <= args.SinceSeq {
				return &hub.HookResponse{Decision: hub.Allow}
			}
			if eventsFilter != nil && !eventsFilter[string(ev.Type)] {
				return &hub.HookResponse{Decision: hub.Allow}
			}
			if laneIDFilter != nil && !laneIDFilter[ev.Lane.LaneID] {
				return &hub.HookResponse{Decision: hub.Allow}
			}
			if kindsFilter != nil && ev.Lane.Kind != "" && !kindsFilter[string(ev.Lane.Kind)] {
				return &hub.HookResponse{Decision: hub.Allow}
			}
			select {
			case out <- LaneStreamChunk{Event: ev}:
			default:
				// Drop on overflow; per spec §6.3 the client detects
				// gaps via missing seq and re-fetches via r1.bus.tail.
			}
			return &hub.HookResponse{Decision: hub.Allow}
		},
	})

	// Snapshot semantics: for since_seq=0 emit session.bound (seq=0)
	// then a lane.created chunk for every active lane currently in
	// the workspace per spec §6.2. We do this BEFORE returning so the
	// caller's first read sees the floor marker.
	if args.SinceSeq == 0 {
		bound := LaneStreamChunk{
			Event: &hub.Event{
				ID:        "session.bound:" + args.SessionID,
				Type:      "session.bound",
				Timestamp: time.Now().UTC(),
				Custom:    map[string]any{"session_id": args.SessionID},
			},
		}
		select {
		case out <- bound:
		default:
		}

		for _, l := range s.backend.Lanes() {
			if l == nil {
				continue
			}
			if laneIDFilter != nil && !laneIDFilter[l.ID] {
				continue
			}
			if kindsFilter != nil && !kindsFilter[string(l.Kind)] {
				continue
			}
			if eventsFilter != nil && !eventsFilter["lane.created"] {
				continue
			}
			started := l.StartedAt
			synthetic := &hub.Event{
				ID:        "snapshot:" + l.ID,
				Type:      hub.EventLaneCreated,
				Timestamp: l.StartedAt,
				Lane: &hub.LaneEvent{
					LaneID:    l.ID,
					SessionID: args.SessionID,
					Seq:       l.LastSeq,
					Kind:      l.Kind,
					ParentID:  l.ParentID,
					Label:     l.Label,
					Pinned:    l.Pinned,
					StartedAt: &started,
					LobeName:  laneLobeName(l),
				},
			}
			select {
			case out <- LaneStreamChunk{Event: synthetic}:
			default:
			}
		}
	}

	// Cancel goroutine: when ctx is done OR cancel() is called, drop
	// the hub subscription, push the final envelope, and close the
	// channel. Channel close is the primary signal; the final chunk
	// is a best-effort hint for clients that don't watch the channel.
	cancelOnce := sync.Once{}
	cancel := func() {
		cancelOnce.Do(func() {
			bus.Unregister(subID)
			select {
			case out <- LaneStreamChunk{Final: true, Reason: "client_unsubscribed"}:
			default:
			}
			closeCh()
		})
	}
	go func() {
		<-ctx.Done()
		cancelOnce.Do(func() {
			bus.Unregister(subID)
			select {
			case out <- LaneStreamChunk{Final: true, Reason: "context_cancelled"}:
			default:
			}
			closeCh()
		})
	}()

	return out, cancel, nil
}

// laneLobeName mirrors the cortex-side lobeNameFor: lobe lanes
// surface the label as the lobe_name; other kinds leave it empty.
func laneLobeName(l *cortex.Lane) string {
	if l == nil {
		return ""
	}
	if l.Kind == hub.LaneKindLobe {
		return l.Label
	}
	return ""
}

// buildLaneEventData mirrors the per-event-type sub-object shape from
// spec §4. Co-located with the MCP server (rather than imported from
// internal/server) so the lanes package has no upward dependencies.
func buildLaneEventData(ev *hub.Event) map[string]any {
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

// envelopeOK builds the §7 success result envelope as a JSON string.
//
//	{ "ok": true, "data": <data> }
//
// data is marshalled verbatim; passing nil omits the field.
func (s *LanesServer) envelopeOK(data any) string {
	out := map[string]any{"ok": true}
	if data != nil {
		out["data"] = data
	}
	body, _ := json.Marshal(out)
	return string(body)
}

// envelopeError builds the §7 error result envelope as a JSON string.
//
//	{ "ok": false, "error_code": "<code>", "error_message": "<msg>" }
//
// code is one of the per-tool §7 enum values (e.g. not_found,
// permission_denied, internal); message is human-readable. The result
// is wrapped at the JSON-RPC level by ServeStdio's tool-call response,
// not here.
func (s *LanesServer) envelopeError(code, message string) string {
	out := map[string]any{
		"ok":            false,
		"error_code":    code,
		"error_message": message,
	}
	body, _ := json.Marshal(out)
	return string(body)
}

// ServeStdio runs the MCP server over stdin/stdout (the MCP stdio
// transport). Speaks JSON-RPC 2.0; same protocol as CodebaseServer
// and StokeServer.
//
// Streaming tools (r1.lanes.subscribe) are invoked via a single
// tool-call request followed by a sequence of result chunks; each
// chunk is one JSON-RPC notification. The implementation lands in
// TASK-20.
func (s *LanesServer) ServeStdio() error {
	return s.serveJSONRPC(os.Stdin, os.Stdout)
}

// serveJSONRPC runs the MCP JSON-RPC loop on the given streams. Split
// out from ServeStdio so tests can drive it with bytes.Buffer.
func (s *LanesServer) serveJSONRPC(in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req jsonRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			writeJSONRPC(out, req.ID, nil, &jsonRPCError{Code: -32700, Message: "Parse error"})
			continue
		}

		switch req.Method {
		case "initialize":
			writeJSONRPC(out, req.ID, map[string]interface{}{
				"protocolVersion": "2025-11-25",
				"capabilities": map[string]interface{}{
					"tools": map[string]bool{"listChanged": false},
				},
				"serverInfo": map[string]string{
					"name":    "r1-lanes",
					"version": "1.0.0",
				},
			}, nil)
		case "notifications/initialized":
			// No response.
		case "tools/list":
			tools := s.ToolDefinitions()
			toolList := make([]map[string]interface{}, 0, len(tools))
			for _, t := range tools {
				entry := map[string]interface{}{
					"name":        t.Name,
					"description": t.Description,
					"inputSchema": t.InputSchema,
				}
				if len(t.OutputSchema) > 0 {
					entry["outputSchema"] = t.OutputSchema
				}
				toolList = append(toolList, entry)
			}
			writeJSONRPC(out, req.ID, map[string]interface{}{"tools": toolList}, nil)
		case "tools/call":
			var params struct {
				Name      string                 `json:"name"`
				Arguments map[string]interface{} `json:"arguments"`
			}
			paramsBytes, _ := json.Marshal(req.Params)
			if err := json.Unmarshal(paramsBytes, &params); err != nil {
				writeJSONRPC(out, req.ID, nil, &jsonRPCError{Code: -32602, Message: "Invalid params"})
				continue
			}
			result, err := s.HandleToolCall(context.Background(), params.Name, params.Arguments)
			if err != nil {
				writeJSONRPC(out, req.ID, map[string]interface{}{
					"content": []map[string]string{{"type": "text", "text": fmt.Sprintf("Error: %v", err)}},
					"isError": true,
				}, nil)
			} else {
				writeJSONRPC(out, req.ID, map[string]interface{}{
					"content": []map[string]string{{"type": "text", "text": result}},
				}, nil)
			}
		default:
			writeJSONRPC(out, req.ID, nil, &jsonRPCError{Code: -32601, Message: "Method not found"})
		}
	}
	return scanner.Err()
}

// LaneToolNames returns the canonical names of the 5 lane tools per spec 8
// §4.2. Used by the lint at tools/lint-view-without-api/ to verify the web
// LaneSidebar component references each one.
func LaneToolNames() []string {
	return []string{
		"r1.lanes.list",
		"r1.lanes.subscribe",
		"r1.lanes.get",
		"r1.lanes.kill",
		"r1.lanes.pin",
	}
}
