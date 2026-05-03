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

// handleList is implemented in TASK-19.
func (s *LanesServer) handleList(_ context.Context, _ map[string]interface{}) (string, error) {
	return s.envelopeError("not_implemented", "r1.lanes.list scaffold; implementation in TASK-19"), nil
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
