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
//
// SECURITY (outbound sanitization policy):
// Tool responses (invoke / verify / audit / delegate outputs and the
// TrustPlane pass-through) are returned verbatim and are NOT pre-sanitized
// for prompt-injection. Non-LLM consumers (CI, dashboards, downstream agent
// frameworks that parse structured JSON) need raw data; different LLM
// consumers have different sanitization requirements. Downstream MCP clients
// that forward these payloads into an LLM prompt MUST apply their own
// prompt-injection defenses. See docs/mcp-security.md for the full
// responsibility boundary.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/ericmacdougall/stoke/internal/r1env"
	"github.com/ericmacdougall/stoke/internal/verify"
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
	out        io.Writer
	writeMu    sync.Mutex
	apiKey     string // empty = no auth
	requireKey bool
	backends   *Backends
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "stoke-mcp:", err)
		os.Exit(1)
	}
}

func run() error {
	apiKey := r1env.Get("R1_MCP_KEY", "STOKE_MCP_KEY")
	ledgerDir := r1env.Get("R1_MCP_LEDGER_DIR", "STOKE_MCP_LEDGER_DIR")
	backends, err := NewBackends(ledgerDir)
	if err != nil {
		return fmt.Errorf("init backends: %w", err)
	}
	defer backends.Close()
	registered, skipped := backends.SeedBuiltinSkillManifests()
	fmt.Fprintf(os.Stderr, "stoke-mcp: seeded %d builtin skill manifests (%d already registered)\n", registered, skipped)
	srv := &Server{
		out:        os.Stdout,
		apiKey:     apiKey,
		requireKey: apiKey != "",
		backends:   backends,
	}
	return srv.serve(os.Stdin)
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
	// Oversize-request handling: bufio.Scanner returns
	// bufio.ErrTooLong if a single line exceeds the 16 MiB
	// buffer cap. Previously that tore the whole server
	// down — a single malformed or inflated request killed
	// every subsequent tool call. Instead emit an RPC error
	// for the bad input and keep serving.
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			s.respondErr(nil, errInvalidReq, "request exceeds 16 MiB line limit", nil)
			// Continue serving by re-entering the loop with
			// a fresh scanner — the underlying reader is
			// still live after ErrTooLong.
			return s.serve(in)
		}
		return err
	}
	return nil
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

// baseTools is the canonical primitive tool set using the legacy
// stoke_* naming. S1-4 of work-r1-rename.md mandates dual-registration:
// every stoke_X tool is also published as r1_X. Both names resolve
// to the same handler. Legacy stoke_* names stay live until v2.0.0
// (S6-6), per ≥2-week external notice requirement.
//
// The 4 shapes STOKE-023 AC requires. The TrustPlane MCP tool
// pass-through layer lands in a follow-up commit once the TrustPlane
// Go SDK is wired into go.mod.
var baseTools = []Tool{
	{
		Name:        "stoke_invoke",
		Description: "Invoke a registered R1 capability (skill or hired agent). Returns the capability's structured output.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"capability": {"type": "string", "description": "Capability name as registered in skillmfr.Registry"},
				"input": {"type": "object", "description": "Capability-specific input matching its declared input schema"},
				"delegation_id": {"type": "string", "description": "Optional delegation token authorizing this invocation"},
				"mission_id": {"type": "string", "description": "Optional mission/session bucket for the audit node; defaults to 'mcp-invoke'"}
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
		Description: "Write an audit entry to the R1 ledger with the supplied evidence references. Returns the resulting ledger node ID.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"action": {"type": "string", "description": "Short description of what the caller did"},
				"evidence_refs": {"type": "array", "items": {"type": "string"}, "description": "Content IDs (ledger nodes, file hashes, URLs) backing the action"},
				"subject_ref": {"type": "string", "description": "Optional: the entity being audited"},
				"mission_id": {"type": "string", "description": "Optional mission/session bucket for the audit node; defaults to 'mcp-audit'"}
			},
			"required": ["action"]
		}`),
	},
	{
		Name:        "stoke_delegate",
		Description: "Create a delegation token granting a named policy bundle's scopes to a delegatee. Token is issued via trustplane.Client. Currently the stoke-mcp binary ships StubClient only; SOW task B-5 will add a NewFromEnv factory that swaps in RealClient (hand-written HTTP against the TrustPlane gateway, no Go SDK) when STOKE_TRUSTPLANE_MODE=real.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"to_did": {"type": "string", "description": "The delegatee's DID"},
				"bundle_name": {"type": "string", "description": "Named Cedar bundle (read-only-calendar / send-on-behalf-of / etc.)"},
				"expiry_seconds": {"type": "integer", "description": "How long the delegation lasts (default 3600)"},
				"mission_id": {"type": "string", "description": "Optional mission/session bucket for the audit node; defaults to 'mcp-delegation'"}
			},
			"required": ["to_did", "bundle_name"]
		}`),
	},
}

// tools is the full tool surface published over MCP tools/list.
// Each baseTools entry is emitted twice: once under the canonical
// r1_* name (S1-4 canonical prefix) and once under the legacy
// stoke_* name (retained until v2.0.0 per S6-6). Both names
// dispatch to the same handler in handleToolsCall.
var tools = buildDualTools(baseTools)

// buildDualTools emits each base tool twice — canonical r1_* first,
// legacy stoke_* second — so tools/list advertises both names. The
// canonical r1_* ordering mirrors the S1-4 dual-accept convention
// (canonical preferred, legacy retained during the window).
func buildDualTools(base []Tool) []Tool {
	out := make([]Tool, 0, len(base)*2)
	for _, t := range base {
		r1Name := canonicalToolName(t.Name)
		if r1Name != t.Name {
			r1 := t
			r1.Name = r1Name
			out = append(out, r1)
		}
		out = append(out, t)
	}
	return out
}

// canonicalToolName maps a legacy stoke_* tool name to its r1_*
// canonical alias. Names that don't carry the stoke_ prefix are
// returned unchanged, so this function is safe to call on any
// tool name.
func canonicalToolName(legacy string) string {
	const legacyPrefix = "stoke_"
	if strings.HasPrefix(legacy, legacyPrefix) {
		return "r1_" + strings.TrimPrefix(legacy, legacyPrefix)
	}
	return legacy
}

// legacyToolName maps a canonical r1_* tool name back to its legacy
// stoke_* form so the dispatch switch can resolve either prefix to
// the same handler. Names that don't carry the r1_ prefix are
// returned unchanged.
func legacyToolName(canonical string) string {
	const canonicalPrefix = "r1_"
	if strings.HasPrefix(canonical, canonicalPrefix) {
		return "stoke_" + strings.TrimPrefix(canonical, canonicalPrefix)
	}
	return canonical
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
	// S1-4: accept both legacy stoke_* and canonical r1_* names.
	// legacyToolName normalizes r1_* → stoke_* so the switch only
	// needs a single case per primitive.
	switch legacyToolName(p.Name) {
	case "stoke_invoke":
		s.handleInvoke(ctx, req, p.Arguments)
	case "stoke_verify":
		s.handleVerify(ctx, req, p.Arguments)
	case "stoke_audit":
		s.handleAudit(ctx, req, p.Arguments)
	case "stoke_delegate":
		s.handleDelegate(ctx, req, p.Arguments)
	default:
		s.respondErr(req.ID, errMethodMiss, "unknown tool: "+p.Name, nil)
	}
}

// --- Tool handlers ---
//
// Each handler validates the shape and returns a synthetic
// response for now. When the TrustPlane RealClient + capability
// registry + verify pipeline are wired into the binary, these
// become thin adapters over the real engines.
//
// The "stoke_X primitives exist" acceptance criterion is met
// by the schema + response shape; the underlying engines are
// already shipped in internal/ and get wired in a follow-up.

func (s *Server) handleInvoke(ctx context.Context, req rpcRequest, args json.RawMessage) {
	var a struct {
		Capability   string          `json:"capability"`
		Input        json.RawMessage `json:"input"`
		DelegationID string          `json:"delegation_id"`
		MissionID    string          `json:"mission_id"`
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
	result, err := s.backends.Invoke(ctx, a.MissionID, a.Capability, a.Input, a.DelegationID)
	if err != nil {
		s.respondErr(req.ID, errInternal, "stoke_invoke: "+err.Error(), nil)
		return
	}
	// Return the backend's structured result + a content
	// array so the MCP client sees both machine-parseable
	// fields AND a human-readable summary.
	text := fmt.Sprintf("invoked %q (manifest hash %v)", a.Capability, result["manifest_hash"])
	if nid, ok := result["audit_node_id"]; ok {
		text += fmt.Sprintf(" — audited as ledger node %v", nid)
	}
	resp := map[string]any{
		"content": []any{
			map[string]any{"type": "text", "text": text},
		},
	}
	for k, v := range result {
		resp[k] = v
	}
	s.respondOK(req.ID, resp)
}

// validVerifyTaskClasses mirrors the authoritative
// verify.TaskClass enumeration. Populated at init from the
// package-level constants so this list can't drift from the
// typed constants if one is added/removed.
var validVerifyTaskClasses = func() map[string]bool {
	set := map[string]bool{}
	for _, c := range []verify.TaskClass{
		verify.TaskClassCode,
		verify.TaskClassResearch,
		verify.TaskClassWriting,
		verify.TaskClassScheduling,
	} {
		set[string(c)] = true
	}
	return set
}()

func (s *Server) handleVerify(ctx context.Context, req rpcRequest, args json.RawMessage) {
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
		// Build the expected list from the authoritative set so
		// the error message mirrors the live enum.
		want := make([]string, 0, len(validVerifyTaskClasses))
		for k := range validVerifyTaskClasses {
			want = append(want, k)
		}
		sort.Strings(want)
		s.respondErr(req.ID, errInvalidArgs,
			"stoke_verify: task_class must be one of ["+strings.Join(want, ", ")+"], got "+a.TaskClass, nil)
		return
	}
	if a.Subject == "" {
		s.respondErr(req.ID, errInvalidArgs, "stoke_verify: subject required", nil)
		return
	}
	result, err := s.backends.Verify(ctx, verify.TaskClass(a.TaskClass), a.Subject)
	if err != nil {
		s.respondErr(req.ID, errInternal, "stoke_verify: "+err.Error(), nil)
		return
	}
	var outcomeCount int
	if outs, ok := result["outcomes"].([]map[string]any); ok {
		outcomeCount = len(outs)
	}
	summary := fmt.Sprintf("verify %s: %d/%d criteria passed (weighted %.2f)",
		a.TaskClass,
		countPassedOutcomes(result["outcomes"]),
		outcomeCount,
		result["weighted_score"])
	resp := map[string]any{
		"content": []any{map[string]any{"type": "text", "text": summary}},
	}
	for k, v := range result {
		resp[k] = v
	}
	s.respondOK(req.ID, resp)
}

// countPassedOutcomes counts RubricResult entries marked
// Passed=true. Tolerates the untyped-slice shape the
// backend returns.
func countPassedOutcomes(v any) int {
	os, ok := v.([]map[string]any)
	if !ok {
		return 0
	}
	n := 0
	for _, o := range os {
		if p, ok := o["passed"].(bool); ok && p {
			n++
		}
	}
	return n
}

func (s *Server) handleAudit(ctx context.Context, req rpcRequest, args json.RawMessage) {
	var a struct {
		Action       string   `json:"action"`
		EvidenceRefs []string `json:"evidence_refs"`
		SubjectRef   string   `json:"subject_ref"`
		MissionID    string   `json:"mission_id"`
	}
	if err := json.Unmarshal(args, &a); err != nil || a.Action == "" {
		s.respondErr(req.ID, errInvalidArgs, "stoke_audit: action required", nil)
		return
	}
	result, err := s.backends.Audit(ctx, a.MissionID, a.Action, a.EvidenceRefs, a.SubjectRef)
	if err != nil {
		s.respondErr(req.ID, errInternal, "stoke_audit: "+err.Error(), nil)
		return
	}
	resp := map[string]any{
		"content": []any{
			map[string]any{
				"type": "text",
				"text": fmt.Sprintf("audit recorded: %s (node %v)", a.Action, result["node_id"]),
			},
		},
	}
	for k, v := range result {
		resp[k] = v
	}
	s.respondOK(req.ID, resp)
}

func (s *Server) handleDelegate(ctx context.Context, req rpcRequest, args json.RawMessage) {
	var a struct {
		ToDID         string `json:"to_did"`
		BundleName    string `json:"bundle_name"`
		ExpirySeconds int    `json:"expiry_seconds"`
		MissionID     string `json:"mission_id"`
	}
	if err := json.Unmarshal(args, &a); err != nil || a.ToDID == "" || a.BundleName == "" {
		s.respondErr(req.ID, errInvalidArgs, "stoke_delegate: to_did + bundle_name required", nil)
		return
	}
	result, err := s.backends.Delegate(ctx, a.MissionID, a.ToDID, a.BundleName, a.ExpirySeconds)
	if err != nil {
		s.respondErr(req.ID, errInternal, "stoke_delegate: "+err.Error(), nil)
		return
	}
	resp := map[string]any{
		"content": []any{
			map[string]any{
				"type": "text",
				"text": fmt.Sprintf("delegation %v issued to %s (bundle %s, expires %v)",
					result["delegation_id"], a.ToDID, a.BundleName, result["expires_at"]),
			},
		},
	}
	for k, v := range result {
		resp[k] = v
	}
	s.respondOK(req.ID, resp)
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
