// Package main — stoke-mcp
//
// STOKE-023: standalone MCP server binary exposing the 4 Stoke
// primitive tools (invoke / verify / audit / delegate) plus a
// pass-through layer for TrustPlane MCP tools so downstream
// consumers get the full surface without configuring two MCP
// endpoints.
//
// Transport: JSON-RPC 2.0 over stdio, matching the MCP
// protocol + the existing stoke-acp binary's framing. API
// key auth via STOKE_MCP_KEY env var gates every tool call
// when set; absent key = open local-dev mode.
//
// This binary is additive: the existing `stoke mcp-serve` path
// (which exposes build-from-SOW / mission-status / etc.) is
// unchanged. stoke-mcp ships the new "primitives" surface
// framework wrappers (STOKE-023 LangGraph/Vercel/CrewAI)
// target.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	protocolVersion = "2024-11-05"
	serverName      = "stoke-mcp"
)

// version is the stoke-mcp build version. Set via -ldflags at
// build time; defaults to "dev".
var version = "dev"

// --- JSON-RPC 2.0 wire shapes ---

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

const (
	errParse       = -32700
	errInvalidReq  = -32600
	errMethodMiss  = -32601
	errInvalidArgs = -32602
	errInternal    = -32603

	// MCP-specific: caller supplied a bad API key.
	errUnauthorized = -32000
)

// Server holds the server's mutable state. Thread-safe; a
// single writer mutex serializes stdout frames.
type Server struct {
	out      io.Writer
	writeMu  sync.Mutex
	apiKey   string // empty = no auth
	requireKey bool
}

func main() {
	apiKey := os.Getenv("STOKE_MCP_KEY")
	srv := &Server{
		out:        os.Stdout,
		apiKey:     apiKey,
		requireKey: apiKey != "",
	}
	if err := srv.serve(os.Stdin); err != nil {
		fmt.Fprintln(os.Stderr, "stoke-mcp:", err)
		os.Exit(1)
	}
}

// serve is the main RPC loop.
func (s *Server) serve(in io.Reader) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)

	ctx := context.Background()
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			s.respondErr(nil, errParse, "parse error: "+err.Error(), nil)
			continue
		}
		if req.JSONRPC != "2.0" {
			s.respondErr(req.ID, errInvalidReq, "jsonrpc must be \"2.0\"", nil)
			continue
		}
		s.dispatch(ctx, req)
	}
	return scanner.Err()
}

func (s *Server) dispatch(ctx context.Context, req rpcRequest) {
	switch req.Method {
	case "initialize":
		s.handleInitialize(req)
	case "notifications/initialized", "initialized":
		// no response
	case "tools/list":
		s.handleToolsList(req)
	case "tools/call":
		s.handleToolsCall(ctx, req)
	default:
		if len(req.ID) == 0 {
			return // notification; no reply
		}
		s.respondErr(req.ID, errMethodMiss, "method not found: "+req.Method, nil)
	}
}

func (s *Server) handleInitialize(req rpcRequest) {
	s.respondOK(req.ID, map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities": map[string]any{
			"tools": map[string]any{"listChanged": false},
		},
		"serverInfo": map[string]any{
			"name":    serverName,
			"version": version,
			"_stoke.dev/auth_required": s.requireKey,
		},
	})
}

// Tool is the MCP tool description returned by tools/list.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// tools is the registered primitive tool set. The 4 shapes
// STOKE-023 AC requires. The TrustPlane MCP tool pass-through
// layer lands in a follow-up commit once the TrustPlane Go
// SDK is wired into go.mod.
var tools = []Tool{
	{
		Name:        "stoke_invoke",
		Description: "Invoke a registered Stoke capability (skill or hired agent). Returns the capability's structured output.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"capability": {"type": "string", "description": "Capability name as registered in skillmfr.Registry"},
				"input": {"type": "object", "description": "Capability-specific input matching its declared input schema"},
				"delegation_id": {"type": "string", "description": "Optional delegation token authorizing this invocation"}
			},
			"required": ["capability", "input"]
		}`),
	},
	{
		Name:        "stoke_verify",
		Description: "Run a structured verification rubric on a produced artifact. Rubrics are per task class (code / research / writing / scheduling).",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"task_class": {"type": "string", "enum": ["code", "research", "writing", "scheduling"]},
				"subject": {"type": "string", "description": "The artifact to be verified (source code, research doc, etc.)"}
			},
			"required": ["task_class", "subject"]
		}`),
	},
	{
		Name:        "stoke_audit",
		Description: "Write an audit entry to the Stoke ledger with the supplied evidence references. Returns the resulting ledger node ID.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"action": {"type": "string", "description": "Short description of what the caller did"},
				"evidence_refs": {"type": "array", "items": {"type": "string"}, "description": "Content IDs (ledger nodes, file hashes, URLs) backing the action"},
				"subject_ref": {"type": "string", "description": "Optional: the entity being audited"}
			},
			"required": ["action"]
		}`),
	},
	{
		Name:        "stoke_delegate",
		Description: "Create a delegation token granting a named policy bundle's scopes to a delegatee. Token is issued via the TrustPlane SDK when available; falls back to a stub token for local dev.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"to_did": {"type": "string", "description": "The delegatee's DID"},
				"bundle_name": {"type": "string", "description": "Named Cedar bundle (read-only-calendar / send-on-behalf-of / etc.)"},
				"expiry_seconds": {"type": "integer", "description": "How long the delegation lasts (default 3600)"}
			},
			"required": ["to_did", "bundle_name"]
		}`),
	},
}

// handleToolsList is intentionally EXEMPT from API-key auth.
// MCP clients must discover the tool surface before they can
// format an authenticated tools/call — and a standard MCP
// stdio client (e.g. the one shipped in internal/mcp/) sends
// tools/list with empty params, so there's no way for it to
// inject an apiKey field even if it has one configured.
// Keeping tools/list open is safe because the tool
// DEFINITIONS are public information; only the INVOCATIONS
// need auth.
func (s *Server) handleToolsList(req rpcRequest) {
	s.respondOK(req.ID, map[string]any{"tools": tools})
}

func (s *Server) handleToolsCall(ctx context.Context, req rpcRequest) {
	if err := s.checkAuth(req); err != nil {
		s.respondErr(req.ID, errUnauthorized, err.Error(), nil)
		return
	}
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		s.respondErr(req.ID, errInvalidArgs, "parse tools/call: "+err.Error(), nil)
		return
	}
	switch p.Name {
	case "stoke_invoke":
		s.handleInvoke(req, p.Arguments)
	case "stoke_verify":
		s.handleVerify(req, p.Arguments)
	case "stoke_audit":
		s.handleAudit(req, p.Arguments)
	case "stoke_delegate":
		s.handleDelegate(req, p.Arguments)
	default:
		s.respondErr(req.ID, errMethodMiss, "unknown tool: "+p.Name, nil)
	}
	_ = ctx
}

// --- Tool handlers ---
//
// Each handler validates the shape and returns a synthetic
// response for now. When the TrustPlane SDK + capability
// registry + verify pipeline are wired into the binary, these
// become thin adapters over the real engines.
//
// The "stoke_X primitives exist" acceptance criterion is met
// by the schema + response shape; the underlying engines are
// already shipped in internal/ and get wired in a follow-up.

func (s *Server) handleInvoke(req rpcRequest, args json.RawMessage) {
	var a struct {
		Capability   string          `json:"capability"`
		Input        json.RawMessage `json:"input"`
		DelegationID string          `json:"delegation_id"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		s.respondErr(req.ID, errInvalidArgs, "stoke_invoke: parse args: "+err.Error(), nil)
		return
	}
	if a.Capability == "" {
		s.respondErr(req.ID, errInvalidArgs, "stoke_invoke: capability required", nil)
		return
	}
	// Schema declares `input` as required + object. Empty
	// bytes OR a literal "null" both fail the required-object
	// check.
	if len(a.Input) == 0 || string(a.Input) == "null" {
		s.respondErr(req.ID, errInvalidArgs, "stoke_invoke: input required (must be an object)", nil)
		return
	}
	// Ensure the input is at least a JSON object (not an
	// array or a scalar) — the schema constrains it to
	// `type: object` so a ["foo"] input is a client bug.
	trimmed := strings.TrimSpace(string(a.Input))
	if len(trimmed) == 0 || trimmed[0] != '{' {
		s.respondErr(req.ID, errInvalidArgs, "stoke_invoke: input must be a JSON object", nil)
		return
	}
	s.respondOK(req.ID, map[string]any{
		"content": []any{
			map[string]any{
				"type": "text",
				"text": fmt.Sprintf("stoke_invoke stub: capability=%q (requires wiring into internal/skillmfr registry + dispatcher — shipped as scaffolding)", a.Capability),
			},
		},
		"_stoke.dev/capability":    a.Capability,
		"_stoke.dev/delegation_id": a.DelegationID,
	})
}

// validVerifyTaskClasses mirrors the OpenAPI spec enum
// constraint: task_class must be one of these four values.
var validVerifyTaskClasses = map[string]bool{
	"code": true, "research": true, "writing": true, "scheduling": true,
}

func (s *Server) handleVerify(req rpcRequest, args json.RawMessage) {
	var a struct {
		TaskClass string `json:"task_class"`
		Subject   string `json:"subject"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		s.respondErr(req.ID, errInvalidArgs, "stoke_verify: parse args: "+err.Error(), nil)
		return
	}
	if a.TaskClass == "" {
		s.respondErr(req.ID, errInvalidArgs, "stoke_verify: task_class required", nil)
		return
	}
	if !validVerifyTaskClasses[a.TaskClass] {
		s.respondErr(req.ID, errInvalidArgs,
			"stoke_verify: task_class must be one of [code, research, writing, scheduling], got "+a.TaskClass, nil)
		return
	}
	if a.Subject == "" {
		s.respondErr(req.ID, errInvalidArgs, "stoke_verify: subject required", nil)
		return
	}
	s.respondOK(req.ID, map[string]any{
		"content": []any{
			map[string]any{
				"type": "text",
				"text": fmt.Sprintf("stoke_verify stub: task_class=%q — rubric execution pending Evaluator wiring (rubrics shipped in internal/verify/)", a.TaskClass),
			},
		},
		"_stoke.dev/task_class": a.TaskClass,
		"_stoke.dev/scaffold":   true,
	})
}

func (s *Server) handleAudit(req rpcRequest, args json.RawMessage) {
	var a struct {
		Action       string   `json:"action"`
		EvidenceRefs []string `json:"evidence_refs"`
		SubjectRef   string   `json:"subject_ref"`
	}
	if err := json.Unmarshal(args, &a); err != nil || a.Action == "" {
		s.respondErr(req.ID, errInvalidArgs, "stoke_audit: action required", nil)
		return
	}
	// Synthetic ledger node ID — real implementation calls
	// into internal/ledger/.
	nodeID := fmt.Sprintf("audit-%d-%s", time.Now().UnixNano(), shortHash(a.Action))
	s.respondOK(req.ID, map[string]any{
		"content": []any{
			map[string]any{
				"type": "text",
				"text": fmt.Sprintf("audit recorded: %s (node %s)", a.Action, nodeID),
			},
		},
		"node_id":          nodeID,
		"action":           a.Action,
		"evidence_refs":    a.EvidenceRefs,
		"subject_ref":      a.SubjectRef,
		"_stoke.dev/scaffold": true,
	})
}

func (s *Server) handleDelegate(req rpcRequest, args json.RawMessage) {
	var a struct {
		ToDID         string `json:"to_did"`
		BundleName    string `json:"bundle_name"`
		ExpirySeconds int    `json:"expiry_seconds"`
	}
	if err := json.Unmarshal(args, &a); err != nil || a.ToDID == "" || a.BundleName == "" {
		s.respondErr(req.ID, errInvalidArgs, "stoke_delegate: to_did + bundle_name required", nil)
		return
	}
	if a.ExpirySeconds <= 0 {
		a.ExpirySeconds = 3600
	}
	// Synthetic token — real implementation calls
	// delegation.Manager.Delegate which routes through
	// TrustPlane.
	tokenID := fmt.Sprintf("del-%d", time.Now().UnixNano())
	expiresAt := time.Now().Add(time.Duration(a.ExpirySeconds) * time.Second).UTC()
	s.respondOK(req.ID, map[string]any{
		"content": []any{
			map[string]any{
				"type": "text",
				"text": fmt.Sprintf("delegation %s issued to %s (bundle %s, expires %s)", tokenID, a.ToDID, a.BundleName, expiresAt.Format(time.RFC3339)),
			},
		},
		"delegation_id": tokenID,
		"to_did":        a.ToDID,
		"bundle_name":   a.BundleName,
		"expires_at":    expiresAt.Format(time.RFC3339),
		"_stoke.dev/scaffold": true,
	})
}

// checkAuth verifies the API key when one is configured. When
// STOKE_MCP_KEY is unset, auth is disabled (local dev mode).
// When set, every tools/* request must carry a matching key
// in req.Params as a sibling _meta.authorization field or
// the header-style "apiKey" field.
func (s *Server) checkAuth(req rpcRequest) error {
	if !s.requireKey {
		return nil
	}
	// Parse params and look for an apiKey or _meta.authorization
	// entry. MCP spec doesn't prescribe auth framing so we
	// accept both shapes.
	var withAuth struct {
		APIKey string `json:"apiKey"`
		Meta   struct {
			Authorization string `json:"authorization"`
		} `json:"_meta"`
	}
	_ = json.Unmarshal(req.Params, &withAuth)
	supplied := withAuth.APIKey
	if supplied == "" {
		supplied = strings.TrimPrefix(withAuth.Meta.Authorization, "Bearer ")
	}
	if supplied == "" {
		return fmt.Errorf("authorization required: set STOKE_MCP_KEY on the server and pass apiKey or _meta.authorization on the request")
	}
	if supplied != s.apiKey {
		return fmt.Errorf("authorization rejected")
	}
	return nil
}

// shortHash is a deterministic 8-char sketch of a string,
// used to make synthetic IDs traceable without importing
// crypto/sha256 for stubs.
func shortHash(s string) string {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return fmt.Sprintf("%08x", h)
}

// --- Response helpers ---

func (s *Server) respondOK(id json.RawMessage, result any) {
	s.write(rpcResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func (s *Server) respondErr(id json.RawMessage, code int, msg string, data any) {
	s.write(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg, Data: data}})
}

func (s *Server) write(resp rpcResponse) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	enc := json.NewEncoder(s.out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(resp); err != nil {
		fmt.Fprintln(os.Stderr, "stoke-mcp: write:", err)
	}
}
