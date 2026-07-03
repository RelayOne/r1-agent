// MCP tool server that exposes codebase analysis tools to Claude Code.
//
// When started as a subprocess, this server speaks JSON-RPC 2.0 over stdin/stdout
// (the MCP stdio transport). Claude Code connects to it via --mcp-config and
// gains access to semantic codebase tools:
//
//   - search_symbols: Find functions, types, classes by name (via symindex)
//   - get_dependencies: Get imports and dependents of a file (via depgraph)
//   - search_content: Semantic content search across the codebase (via tfidf)
//   - get_file_symbols: List all symbols defined in a specific file
//   - impact_analysis: Compute the transitive set of files affected by a change
//   - find_symbol_usages: Find all consumer files that reference a symbol
//   - trace_entry_points: Find all entry points (roots) that can reach a file
//   - semantic_search: Vector-based semantic search by meaning, not keywords
//
// These tools give the model structured access to the codebase during agentic
// discovery and validation loops, replacing the need for grep/find heuristics
// with real symbol-level and dependency-level understanding.
//
// The query implementations live in internal/codegraph, which the native tool
// registry (internal/tools) shares — this server is a JSON-RPC transport over
// the same index facade, so both surfaces render identical results.
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"context"

	"github.com/RelayOne/r1/internal/codegraph"
	"github.com/RelayOne/r1/internal/depgraph"
	"github.com/RelayOne/r1/internal/symindex"
	"github.com/RelayOne/r1/internal/tfidf"
	"github.com/RelayOne/r1/internal/throttle"
	"github.com/RelayOne/r1/internal/vecindex"
)

// MCP JSON-RPC 2.0 method names handled by CodebaseServer.
const (
	mcpMethodInitialize = "initialize"
	mcpMethodToolsList  = "tools/list"
	mcpMethodToolsCall  = "tools/call"
)

// CodebaseServer is an MCP tool server that exposes codebase analysis.
type CodebaseServer struct {
	symIdx   *symindex.Index
	depGraph *depgraph.Graph
	tfidfIdx *tfidf.Index
	vecIdx   *vecindex.Index
	repoRoot string

	// preDispatch carries the optional throttle + input-validation
	// gates that fire BEFORE HandleToolCall. Wired via
	// WithThrottler / WithPromptGuard so the two policy gates can be
	// configured independently. Either field may be nil; an entirely
	// nil PreDispatch is the open-local-dev posture (no gates).
	preDispatch PreDispatch
}

// WithThrottler installs the rate-limit gate that runs BEFORE
// HandleToolCall on every tools/call request. Mirrors the
// builder-option pattern used by WithAuthKey / WithCortex on
// StokeServer. Passing nil clears any previously-installed throttler
// (open local-dev mode). See specs/per-tool-throttling.md task T7.
func (s *CodebaseServer) WithThrottler(l throttle.Limiter) *CodebaseServer {
	s.preDispatch.Throttler = l
	return s
}

// WithPromptGuard installs the tool-input validation gate (A1-T2,
// parallel branch). Wiring lives in the parallel branch; the field
// is declared here so the two branches' edits sit as siblings
// inside PreDispatch rather than nested in each other's code.
func (s *CodebaseServer) WithPromptGuard(fn func(tc ToolCallContext) PreDispatchDecision) *CodebaseServer {
	s.preDispatch.ValidateInput = fn
	return s
}

// NewCodebaseServer creates a server with pre-built indexes.
func NewCodebaseServer(repoRoot string, symIdx *symindex.Index, depGraph *depgraph.Graph, tfidfIdx *tfidf.Index) *CodebaseServer {
	return &CodebaseServer{
		symIdx:   symIdx,
		depGraph: depGraph,
		tfidfIdx: tfidfIdx,
		repoRoot: repoRoot,
	}
}

// BuildCodebaseServer creates a server, building indexes from disk.
func BuildCodebaseServer(repoRoot string) (*CodebaseServer, error) {
	graph, err := codegraph.Build(repoRoot)
	if err != nil {
		return nil, err
	}

	srv := NewCodebaseServer(repoRoot, graph.Sym(), graph.Dep(), graph.TFIDF())
	srv.vecIdx = graph.Vec()
	return srv, nil
}

// graph assembles the codegraph query facade over the server's current
// indexes. Views are cheap (a handful of pointer copies) and any nil index
// degrades to an informational "not available" result, which keeps
// zero-value CodebaseServer instances answering gracefully.
func (s *CodebaseServer) graph() *codegraph.Index {
	return codegraph.New(s.repoRoot, s.symIdx, s.depGraph, s.tfidfIdx, s.vecIdx)
}

// ToolDefinitions returns the MCP tool definitions this server provides.
func (s *CodebaseServer) ToolDefinitions() []ToolDefinition {
	return []ToolDefinition{
		{
			Name:        "search_symbols",
			Description: "Search for code symbols (functions, types, classes, interfaces) by name prefix. Returns symbol name, kind, file, and line number.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"query": {"type": "string", "description": "Symbol name or prefix to search for"},
					"kind": {"type": "string", "description": "Filter by kind: function, method, type, interface, class, variable, constant", "enum": ["function", "method", "type", "interface", "class", "variable", "constant", ""]},
					"limit": {"type": "integer", "description": "Maximum results (default 20)", "default": 20}
				},
				"required": ["query"]
			}`),
		},
		{
			Name:        "get_dependencies",
			Description: "Get the import dependencies and reverse dependencies (dependents) of a file. Shows what a file imports and what imports it.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"file": {"type": "string", "description": "File path relative to repo root"}
				},
				"required": ["file"]
			}`),
		},
		{
			Name:        "search_content",
			Description: "Semantic content search across the codebase. Finds files whose content is most relevant to a natural language query. Uses TF-IDF ranking.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"query": {"type": "string", "description": "Natural language search query describing what you're looking for"},
					"limit": {"type": "integer", "description": "Maximum results (default 10)", "default": 10}
				},
				"required": ["query"]
			}`),
		},
		{
			Name:        "get_file_symbols",
			Description: "List all symbols (functions, types, classes, methods) defined in a specific file.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"file": {"type": "string", "description": "File path relative to repo root"}
				},
				"required": ["file"]
			}`),
		},
		{
			Name:        "impact_analysis",
			Description: "Compute the transitive set of files affected by changes to a given file. Follows the dependency graph to find all direct and indirect dependents.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"file": {"type": "string", "description": "File path relative to repo root"}
				},
				"required": ["file"]
			}`),
		},
		{
			Name:        "find_symbol_usages",
			Description: "Find all files that reference a symbol (function, type, class). Shows where a symbol is consumed across the codebase, with context about what each consuming file defines. Essential for tracing producer/consumer relationships.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"symbol": {"type": "string", "description": "Exact symbol name to find usages of"},
					"limit": {"type": "integer", "description": "Maximum results (default 20)", "default": 20}
				},
				"required": ["symbol"]
			}`),
		},
		{
			Name:        "trace_entry_points",
			Description: "Trace all entry points (roots) that can reach a given file through the dependency graph. Shows the dependency chain from each root to the target file. Essential for determining which surfaces (API, CLI, web, etc.) can trigger code in a file.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"file": {"type": "string", "description": "File path relative to repo root"}
				},
				"required": ["file"]
			}`),
		},
		{
			Name:        "semantic_search",
			Description: "Vector-based semantic search that finds code by meaning, not just keywords. Understands that 'authentication' relates to 'login', 'session', 'token'. Superior to keyword search for conceptual queries like 'error handling patterns' or 'data validation logic'.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"query": {"type": "string", "description": "Natural language query describing the concept you're looking for"},
					"limit": {"type": "integer", "description": "Maximum results (default 10)", "default": 10}
				},
				"required": ["query"]
			}`),
		},
		{
			Name:        "get_call_graph",
			Description: "Get the call graph for a symbol — who calls it (callers) and what it calls (callees). Uses real AST-parsed call edges for Go files. Essential for understanding code flow, impact of changes, and dependency chains between functions.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"symbol": {"type": "string", "description": "Symbol name to get call graph for"},
					"direction": {"type": "string", "description": "callers, callees, or both (default: both)", "enum": ["callers", "callees", "both", ""], "default": "both"},
					"limit": {"type": "integer", "description": "Maximum results per direction (default 20)", "default": 20}
				},
				"required": ["symbol"]
			}`),
		},
		{
			Name:        "get_interface_implementations",
			Description: "Find all types that implement a given interface. Uses real AST method-set analysis for Go files. Critical for understanding polymorphism, finding concrete implementations, and verifying interface contracts.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"interface": {"type": "string", "description": "Interface name to find implementations of"}
				},
				"required": ["interface"]
			}`),
		},
		{
			Name:        "get_symbol_detail",
			Description: "Get detailed information about a specific symbol: full typed signature, doc comment, line range, parent type, type name, and export status. Uses real AST parsing for Go files.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"symbol": {"type": "string", "description": "Exact symbol name to get details for"}
				},
				"required": ["symbol"]
			}`),
		},
	}
}

// HandleToolCall processes a tool invocation and returns the result.
func (s *CodebaseServer) HandleToolCall(toolName string, args map[string]interface{}) (string, error) {
	g := s.graph()
	switch toolName {
	case "search_symbols":
		return g.SearchSymbols(stringArg(args, "query"), stringArg(args, "kind"), intArg(args, "limit", 20))
	case "get_dependencies":
		return g.GetDependencies(stringArg(args, "file"))
	case "search_content":
		return g.SearchContent(stringArg(args, "query"), intArg(args, "limit", 10))
	case "get_file_symbols":
		return g.GetFileSymbols(stringArg(args, "file"))
	case "impact_analysis":
		return g.ImpactAnalysis(stringArg(args, "file"))
	case "find_symbol_usages":
		return g.FindSymbolUsages(stringArg(args, "symbol"), intArg(args, "limit", 20))
	case "trace_entry_points":
		return g.TraceEntryPoints(stringArg(args, "file"))
	case "semantic_search":
		return g.SemanticSearch(stringArg(args, "query"), intArg(args, "limit", 10))
	case "get_call_graph":
		return g.GetCallGraph(stringArg(args, "symbol"), stringArg(args, "direction"), intArg(args, "limit", 20))
	case "get_interface_implementations":
		return g.GetInterfaceImplementations(stringArg(args, "interface"))
	case "get_symbol_detail":
		return g.GetSymbolDetail(stringArg(args, "symbol"))
	default:
		return "", fmt.Errorf("unknown tool: %s", toolName)
	}
}

// stringArg extracts a string argument from an MCP tools/call arguments map.
func stringArg(args map[string]interface{}, key string) string {
	v, _ := args[key].(string)
	return v
}

// intArg extracts a positive integer argument (JSON numbers arrive as
// float64), falling back to def when absent or non-positive.
func intArg(args map[string]interface{}, key string, def int) int {
	if v, ok := args[key].(float64); ok && v > 0 {
		return int(v)
	}
	return def
}

// classifyEntryPoint and expandIdentifier delegate to codegraph, where the
// shared implementations live.
func classifyEntryPoint(path string) string { return codegraph.ClassifyEntryPoint(path) }

func expandIdentifier(name string) []string { return codegraph.ExpandIdentifier(name) }

// ServeStdio runs the MCP server on stdin/stdout using JSON-RPC 2.0.
// This is the main entry point when the server is started as a subprocess.
func (s *CodebaseServer) ServeStdio() error {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB buffer

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req jsonRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			writeJSONRPC(os.Stdout, req.ID, nil, &jsonRPCError{Code: -32700, Message: "Parse error"})
			continue
		}

		switch req.Method {
		case mcpMethodInitialize:
			writeJSONRPC(os.Stdout, req.ID, map[string]interface{}{
				"protocolVersion": "2024-11-05",
				"capabilities": map[string]interface{}{
					"tools": map[string]bool{"listChanged": false},
				},
				"serverInfo": map[string]string{
					"name":    "stoke-codebase",
					"version": "1.0.0",
				},
			}, nil)

		case "notifications/initialized":
			// No response needed for notifications

		case mcpMethodToolsList:
			tools := s.ToolDefinitions()
			var toolList []map[string]interface{}
			for _, t := range tools {
				toolList = append(toolList, map[string]interface{}{
					"name":        t.Name,
					"description": t.Description,
					"inputSchema": t.InputSchema,
				})
			}
			writeJSONRPC(os.Stdout, req.ID, map[string]interface{}{
				"tools": toolList,
			}, nil)

		case mcpMethodToolsCall:
			var params struct {
				Name      string                 `json:"name"`
				Arguments map[string]interface{} `json:"arguments"`
				Meta      map[string]interface{} `json:"meta,omitempty"`
			}
			paramsBytes, _ := json.Marshal(req.Params)
			if err := json.Unmarshal(paramsBytes, &params); err != nil {
				writeJSONRPC(os.Stdout, req.ID, nil, &jsonRPCError{Code: -32602, Message: "Invalid params"})
				continue
			}

			// Pre-dispatch gates (throttle + promptguard) run as
			// SIBLINGS via the shared PreDispatch.Check helper. C3
			// wires Throttler, A1-T2 wires ValidateInput; both live
			// next to each other inside this single call so the two
			// branches' edits do not collide on the dispatch site.
			sessionID, tenantID, rawMeta := extractAuthMeta(params.Meta)
			if dec := s.preDispatch.Check(ToolCallContext{
				Ctx:       context.Background(),
				SessionID: sessionID,
				TenantID:  tenantID,
				ToolName:  params.Name,
				Args:      params.Arguments,
				Meta:      rawMeta,
			}); !dec.Allowed {
				writeJSONRPC(os.Stdout, req.ID, map[string]interface{}{
					"content": []map[string]string{{"type": "text", "text": dec.Message}},
					"isError": true,
					"_meta":   map[string]any{"r1_error": dec.MetaError},
				}, nil)
				continue
			}

			result, err := s.HandleToolCall(params.Name, params.Arguments)
			if err != nil {
				writeJSONRPC(os.Stdout, req.ID, map[string]interface{}{
					"content": []map[string]string{{"type": "text", "text": fmt.Sprintf("Error: %v", err)}},
					"isError": true,
				}, nil)
			} else {
				writeJSONRPC(os.Stdout, req.ID, map[string]interface{}{
					"content": []map[string]string{{"type": "text", "text": result}},
				}, nil)
			}

		default:
			writeJSONRPC(os.Stdout, req.ID, nil, &jsonRPCError{Code: -32601, Message: "Method not found"})
		}
	}

	return scanner.Err()
}

// writeJSONRPC writes a JSON-RPC 2.0 response to the writer.
func writeJSONRPC(w io.Writer, id int, result interface{}, rpcErr *jsonRPCError) {
	resp := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
	}
	if rpcErr != nil {
		resp["error"] = rpcErr
	} else {
		resp["result"] = result
	}
	data, _ := json.Marshal(resp)
	fmt.Fprintf(w, "%s\n", data)
}

// WriteMCPConfig writes an MCP configuration file that tells Claude Code
// how to start this server. The config can be passed to --mcp-config.
func WriteMCPConfig(configPath, binaryPath, repoRoot string) error {
	config := map[string]interface{}{
		"mcpServers": map[string]interface{}{
			"stoke-codebase": map[string]interface{}{
				"command": binaryPath,
				"args":    []string{"mcp-serve", "--repo", repoRoot},
			},
		},
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath, data, 0o644) // #nosec G306 -- codebase server artefact; user-readable.
}
