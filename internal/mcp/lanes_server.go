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
// This file is the scaffold (TASK-18). Tool definitions carry the §7
// JSON Schema draft 2020-12 documents verbatim as json.RawMessage in
// ToolDefinition.InputSchema / OutputSchema. Per-tool handler bodies
// land in subsequent TASKs (TASK-19..23).
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

	// mu guards subscriptions; not currently used during the scaffold
	// but reserved for the streaming subscribe handler (TASK-20) so
	// adding it later is a non-breaking change.
	mu sync.Mutex
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
// tools (r1.lanes.subscribe) are handled by HandleStreamingToolCall.
//
// During the TASK-18 scaffold all handlers return a "not implemented"
// envelope so the caller sees a structured error rather than an empty
// response. Subsequent TASKs (19..23) replace the bodies with real
// implementations.
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
		return s.envelopeError("invalid_request", "r1.lanes.subscribe is a streaming tool; use HandleStreamingToolCall"), nil
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

// handleGet is implemented in TASK-21.
func (s *LanesServer) handleGet(_ context.Context, _ map[string]interface{}) (string, error) {
	return s.envelopeError("not_implemented", "r1.lanes.get scaffold; implementation in TASK-21"), nil
}

// handleKill is implemented in TASK-22.
func (s *LanesServer) handleKill(_ context.Context, _ map[string]interface{}) (string, error) {
	return s.envelopeError("not_implemented", "r1.lanes.kill scaffold; implementation in TASK-22"), nil
}

// handlePin is implemented in TASK-23.
func (s *LanesServer) handlePin(_ context.Context, _ map[string]interface{}) (string, error) {
	return s.envelopeError("not_implemented", "r1.lanes.pin scaffold; implementation in TASK-23"), nil
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
