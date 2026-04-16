// Package main — stoke-acp
//
// Agent Client Protocol (ACP) adapter binary (S-U-002). Wraps
// Stoke's mission surface in an async JSON-RPC stdio server per
// the ACP spec so editors that speak ACP (Zed, JetBrains,
// Neovim via CodeCompanion/avante.nvim, and any future
// ACP-capable editor) can drive Stoke directly without a
// per-editor bespoke integration.
//
// Transport: JSON-RPC 2.0 over stdio. Stdout is reserved for
// RPC frames; logs go to stderr (via stoke's internal/logging
// conventions). Each line on stdin is one JSON-RPC request;
// responses and notifications write a single JSON object +
// newline to stdout.
//
// Protocol surface shipped in this initial binary:
//
//   initialize       — capability negotiation
//   session/new      — create a session, bind cwd, allocate ID
//   session/prompt   — accept a prompt, emit tool_call /
//                       agent_message_chunk notifications, finish
//                       with a tool_call_update result. PHASE 1
//                       SHIPS A DIRECT-ECHO STUB so editors can
//                       wire the transport end-to-end; PHASE 2
//                       will delegate to Stoke's mission runner
//                       via a separate PR that can be reviewed
//                       independently of the transport shape.
//   session/cancel   — mark a session for cancellation
//   session/load     — reload a previously-created session by ID
//
// Stoke-specific extensions live under the `_stoke.dev/`
// namespace in event payloads so consumers parsing stock ACP
// ignore them cleanly.
//
// This binary is strictly additive: Stoke's existing `stoke`
// binary, TUI, and mission API are unchanged. Operators who
// don't invoke `stoke-acp` are unaffected.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
)

// jsonRPCRequest and jsonRPCResponse are the 2.0 wire shapes.
// Kept inline in this binary (rather than extracted to
// internal/jsonrpc/) to avoid a refactor of the MCP server's
// existing types — the two speak similar framing but different
// method vocabularies, so an extraction can land as a separate
// PR without blocking this adapter.
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"` // string or number or absent for notifications
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

// Well-known JSON-RPC error codes per the spec.
const (
	rpcParseError     = -32700
	rpcInvalidRequest = -32600
	rpcMethodNotFound = -32601
	rpcInvalidParams  = -32602
	rpcInternalError  = -32603
)

// acpProtocolVersion is the ACP protocol version this adapter
// targets. Matches the published agentclientprotocol.com reference
// implementations as of early 2026.
const acpProtocolVersion = "0.1.0"

// Server holds the adapter's mutable state: sessions + a writer
// mutex so concurrent notifications don't interleave on stdout.
type Server struct {
	mu       sync.Mutex // guards sessions and out
	sessions map[string]*Session
	out      io.Writer // stdout — single writer, single-goroutine serialization via mu
}

// Session is one ACP session. Bound to an editor cwd; optionally
// allocates a worktree for isolation (future; Phase 1 uses cwd
// directly so editors see changes in place).
type Session struct {
	ID        string
	Cwd       string
	CreatedAt time.Time
	Canceled  bool
}

func main() {
	srv := &Server{
		sessions: map[string]*Session{},
		out:      os.Stdout,
	}
	// Log to stderr per ACP convention; stdout reserved for RPC.
	if err := srv.serve(os.Stdin); err != nil {
		fmt.Fprintln(os.Stderr, "stoke-acp:", err)
		os.Exit(1)
	}
}

// serve is the main RPC loop. One request per line; responses
// written inline. Notifications (no ID field) get no reply.
func (s *Server) serve(in io.Reader) error {
	scanner := bufio.NewScanner(in)
	// 16 MiB line limit — ACP prompts + repo context blobs can be
	// large; the MCP server uses the same bound.
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)

	ctx := context.Background()
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req jsonRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			s.respondError(nil, rpcParseError, "parse error: "+err.Error())
			continue
		}
		if req.JSONRPC != "2.0" {
			s.respondError(req.ID, rpcInvalidRequest, "jsonrpc must be \"2.0\"")
			continue
		}
		s.dispatch(ctx, req)
	}
	return scanner.Err()
}

// dispatch routes one request to its handler.
func (s *Server) dispatch(ctx context.Context, req jsonRPCRequest) {
	switch req.Method {
	case "initialize":
		s.handleInitialize(req)
	case "session/new":
		s.handleSessionNew(req)
	case "session/prompt":
		s.handleSessionPrompt(ctx, req)
	case "session/cancel":
		s.handleSessionCancel(req)
	case "session/load":
		s.handleSessionLoad(req)
	case "notifications/initialized", "initialized":
		// No response for notifications.
	default:
		s.respondError(req.ID, rpcMethodNotFound, "method not found: "+req.Method)
	}
}

// --- Handlers ---

func (s *Server) handleInitialize(req jsonRPCRequest) {
	// Capabilities negotiation. Phase 1 declares the minimal
	// surface the transport supports; future phases extend this
	// as features land (image prompts, streaming diffs, etc.).
	s.respondResult(req.ID, map[string]any{
		"protocolVersion": acpProtocolVersion,
		"capabilities": map[string]any{
			// loadSession: client can reload a previously-created
			// session by ID via session/load.
			"loadSession": true,
			// Client prompt capabilities — what the editor can
			// send us. Phase 1 accepts text only; image/audio
			// support lands with a later capability bump.
			"promptCapabilities": map[string]any{
				"text":  true,
				"image": false,
				"audio": false,
			},
			// Auth handled out-of-band via environment variables
			// stoke already honors (ANTHROPIC_API_KEY, etc.). No
			// ACP-level auth negotiation in this version.
			"authentication": map[string]any{
				"required": false,
			},
		},
		"serverInfo": map[string]any{
			"name":    "stoke-acp",
			"version": "0.1.0",
			"_stoke.dev/phase": "1",
			"_stoke.dev/note":  "Phase 1 transport + session lifecycle. session/prompt currently echoes; Phase 2 delegates to Stoke's mission runner.",
		},
	})
}

func (s *Server) handleSessionNew(req jsonRPCRequest) {
	var params struct {
		Cwd string `json:"cwd"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		s.respondError(req.ID, rpcInvalidParams, "session/new params: "+err.Error())
		return
	}
	cwd := params.Cwd
	if cwd == "" {
		// Default to current working directory of the adapter
		// process — editors that don't specify a cwd get the
		// invocation directory, which is typically what they want.
		if wd, err := os.Getwd(); err == nil {
			cwd = wd
		}
	} else {
		// Resolve to absolute so later operations that compare
		// paths don't drift based on the adapter's cwd.
		if abs, err := filepath.Abs(cwd); err == nil {
			cwd = abs
		}
	}
	id := uuid.NewString()
	s.mu.Lock()
	s.sessions[id] = &Session{
		ID:        id,
		Cwd:       cwd,
		CreatedAt: time.Now().UTC(),
	}
	s.mu.Unlock()
	s.respondResult(req.ID, map[string]any{
		"sessionId": id,
		"_stoke.dev/cwd": cwd,
	})
}

func (s *Server) handleSessionLoad(req jsonRPCRequest) {
	var params struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		s.respondError(req.ID, rpcInvalidParams, "session/load params: "+err.Error())
		return
	}
	s.mu.Lock()
	sess, ok := s.sessions[params.SessionID]
	s.mu.Unlock()
	if !ok {
		s.respondError(req.ID, rpcInvalidParams, "session not found: "+params.SessionID)
		return
	}
	s.respondResult(req.ID, map[string]any{
		"sessionId":     sess.ID,
		"_stoke.dev/cwd": sess.Cwd,
		"_stoke.dev/createdAt": sess.CreatedAt.Format(time.RFC3339Nano),
		"_stoke.dev/canceled":  sess.Canceled,
	})
}

func (s *Server) handleSessionPrompt(ctx context.Context, req jsonRPCRequest) {
	var params struct {
		SessionID string `json:"sessionId"`
		Prompt    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"prompt"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		s.respondError(req.ID, rpcInvalidParams, "session/prompt params: "+err.Error())
		return
	}
	s.mu.Lock()
	sess, ok := s.sessions[params.SessionID]
	s.mu.Unlock()
	if !ok {
		s.respondError(req.ID, rpcInvalidParams, "session not found: "+params.SessionID)
		return
	}
	if sess.Canceled {
		s.respondError(req.ID, rpcInvalidParams, "session canceled: "+params.SessionID)
		return
	}

	// Concatenate text blocks for the Phase 1 echo. Image/audio
	// are rejected by the capability negotiation above, so
	// non-text blocks reaching us indicate a client protocol bug
	// we surface explicitly.
	var fullPrompt string
	for i, block := range params.Prompt {
		if block.Type != "text" {
			s.respondError(req.ID, rpcInvalidParams, fmt.Sprintf("prompt[%d] type=%q not supported in phase 1 (text only)", i, block.Type))
			return
		}
		if fullPrompt != "" {
			fullPrompt += "\n\n"
		}
		fullPrompt += block.Text
	}

	// Emit an agent_message_chunk notification so editors see
	// streaming progress. Then emit a terminal result. This is
	// the ACP event vocabulary editors already parse for Claude
	// Code / Hermes / Goose.
	s.sendNotification("session/update", map[string]any{
		"sessionId": sess.ID,
		"update": map[string]any{
			"sessionUpdate": "agent_message_chunk",
			"content": map[string]any{
				"type": "text",
				"text": fmt.Sprintf("stoke-acp Phase 1 echo: received %d chars. Phase 2 will delegate to Stoke's mission runner; this build returns without executing tools.", len(fullPrompt)),
			},
		},
	})
	s.respondResult(req.ID, map[string]any{
		"stopReason": "end_turn",
		"_stoke.dev/phase": "1",
		"_stoke.dev/mode":  "echo",
	})
}

func (s *Server) handleSessionCancel(req jsonRPCRequest) {
	var params struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		s.respondError(req.ID, rpcInvalidParams, "session/cancel params: "+err.Error())
		return
	}
	s.mu.Lock()
	sess, ok := s.sessions[params.SessionID]
	if ok {
		sess.Canceled = true
	}
	s.mu.Unlock()
	if !ok {
		s.respondError(req.ID, rpcInvalidParams, "session not found: "+params.SessionID)
		return
	}
	s.respondResult(req.ID, map[string]any{"canceled": true})
}

// --- Transport helpers ---

// respondResult writes a successful JSON-RPC response. id may be
// nil for methods that never have an id (shouldn't happen for
// result responses — caller is responsible for passing the
// request's id).
func (s *Server) respondResult(id json.RawMessage, result any) {
	s.write(jsonRPCResponse{JSONRPC: "2.0", ID: id, Result: result})
}

// respondError writes an error response. id may be nil when the
// request itself failed to parse; in that case the response has
// no id field per JSON-RPC spec.
func (s *Server) respondError(id json.RawMessage, code int, msg string) {
	s.write(jsonRPCResponse{JSONRPC: "2.0", ID: id, Error: &jsonRPCError{Code: code, Message: msg}})
}

// sendNotification writes a JSON-RPC notification (no id, no
// response expected). Used for session/update events during a
// session/prompt turn.
func (s *Server) sendNotification(method string, params any) {
	msg := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	}
	s.writeRaw(msg)
}

// write emits a single response object + newline to stdout under
// the mutex so concurrent notifications / responses can't
// interleave.
func (s *Server) write(resp jsonRPCResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	enc := json.NewEncoder(s.out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(resp); err != nil {
		fmt.Fprintln(os.Stderr, "stoke-acp: write response:", err)
	}
}

// writeRaw is the notification-side counterpart that emits any
// map-valued object (not constrained to jsonRPCResponse shape).
func (s *Server) writeRaw(obj any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	enc := json.NewEncoder(s.out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(obj); err != nil {
		fmt.Fprintln(os.Stderr, "stoke-acp: write notification:", err)
	}
}
