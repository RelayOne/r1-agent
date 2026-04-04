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
//
// These tools give the model structured access to the codebase during agentic
// discovery and validation loops, replacing the need for grep/find heuristics
// with real symbol-level and dependency-level understanding.
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ericmacdougall/stoke/internal/depgraph"
	"github.com/ericmacdougall/stoke/internal/symindex"
	"github.com/ericmacdougall/stoke/internal/tfidf"
)

// CodebaseServer is an MCP tool server that exposes codebase analysis.
type CodebaseServer struct {
	symIdx   *symindex.Index
	depGraph *depgraph.Graph
	tfidfIdx *tfidf.Index
	repoRoot string
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
	exts := []string{".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".rs", ".java",
		".css", ".scss", ".html", ".vue", ".svelte", ".yaml", ".yml", ".json"}

	symIdx, err := symindex.Build(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("build symbol index: %w", err)
	}

	depGraph, err := depgraph.Build(repoRoot, exts)
	if err != nil {
		return nil, fmt.Errorf("build dependency graph: %w", err)
	}

	tfidfIdx, err := tfidf.Build(repoRoot, exts)
	if err != nil {
		return nil, fmt.Errorf("build tfidf index: %w", err)
	}

	return NewCodebaseServer(repoRoot, symIdx, depGraph, tfidfIdx), nil
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
	}
}

// HandleToolCall processes a tool invocation and returns the result.
func (s *CodebaseServer) HandleToolCall(toolName string, args map[string]interface{}) (string, error) {
	switch toolName {
	case "search_symbols":
		return s.handleSearchSymbols(args)
	case "get_dependencies":
		return s.handleGetDependencies(args)
	case "search_content":
		return s.handleSearchContent(args)
	case "get_file_symbols":
		return s.handleGetFileSymbols(args)
	case "impact_analysis":
		return s.handleImpactAnalysis(args)
	default:
		return "", fmt.Errorf("unknown tool: %s", toolName)
	}
}

func (s *CodebaseServer) handleSearchSymbols(args map[string]interface{}) (string, error) {
	query, _ := args["query"].(string)
	if query == "" {
		return "", fmt.Errorf("query is required")
	}

	limit := 20
	if l, ok := args["limit"].(float64); ok && l > 0 {
		limit = int(l)
	}

	kindFilter, _ := args["kind"].(string)

	if s.symIdx == nil {
		return "Symbol index not available", nil
	}

	results := s.symIdx.Search(query)

	var filtered []symindex.Symbol
	for _, sym := range results {
		if kindFilter != "" && string(sym.Kind) != kindFilter {
			continue
		}
		filtered = append(filtered, sym)
		if len(filtered) >= limit {
			break
		}
	}

	if len(filtered) == 0 {
		return fmt.Sprintf("No symbols found matching %q", query), nil
	}

	var sb strings.Builder
	for _, sym := range filtered {
		fmt.Fprintf(&sb, "%s %s (%s:%d)", sym.Kind, sym.Name, sym.File, sym.Line)
		if sym.Parent != "" {
			fmt.Fprintf(&sb, " [parent: %s]", sym.Parent)
		}
		if sym.Exported {
			sb.WriteString(" [exported]")
		}
		sb.WriteString("\n")
	}
	return sb.String(), nil
}

func (s *CodebaseServer) handleGetDependencies(args map[string]interface{}) (string, error) {
	file, _ := args["file"].(string)
	if file == "" {
		return "", fmt.Errorf("file is required")
	}

	if s.depGraph == nil {
		return "Dependency graph not available", nil
	}

	deps := s.depGraph.Dependencies(file)
	dependents := s.depGraph.Dependents(file)

	var sb strings.Builder
	fmt.Fprintf(&sb, "File: %s\n\n", file)

	fmt.Fprintf(&sb, "Imports (%d):\n", len(deps))
	for _, d := range deps {
		fmt.Fprintf(&sb, "  %s\n", d)
	}

	fmt.Fprintf(&sb, "\nImported by (%d):\n", len(dependents))
	for _, d := range dependents {
		fmt.Fprintf(&sb, "  %s\n", d)
	}

	return sb.String(), nil
}

func (s *CodebaseServer) handleSearchContent(args map[string]interface{}) (string, error) {
	query, _ := args["query"].(string)
	if query == "" {
		return "", fmt.Errorf("query is required")
	}

	limit := 10
	if l, ok := args["limit"].(float64); ok && l > 0 {
		limit = int(l)
	}

	if s.tfidfIdx == nil {
		return "Content search index not available", nil
	}

	results := s.tfidfIdx.Search(query, limit)

	if len(results) == 0 {
		return fmt.Sprintf("No files found matching %q", query), nil
	}

	var sb strings.Builder
	for _, r := range results {
		fmt.Fprintf(&sb, "%.3f  %s\n", r.Score, r.Path)
	}
	return sb.String(), nil
}

func (s *CodebaseServer) handleGetFileSymbols(args map[string]interface{}) (string, error) {
	file, _ := args["file"].(string)
	if file == "" {
		return "", fmt.Errorf("file is required")
	}

	if s.symIdx == nil {
		return "Symbol index not available", nil
	}

	symbols := s.symIdx.InFile(file)
	if len(symbols) == 0 {
		return fmt.Sprintf("No symbols found in %s", file), nil
	}

	var sb strings.Builder
	for _, sym := range symbols {
		fmt.Fprintf(&sb, "  L%-4d %s %s", sym.Line, sym.Kind, sym.Name)
		if sym.Exported {
			sb.WriteString(" [exported]")
		}
		sb.WriteString("\n")
	}
	return sb.String(), nil
}

func (s *CodebaseServer) handleImpactAnalysis(args map[string]interface{}) (string, error) {
	file, _ := args["file"].(string)
	if file == "" {
		return "", fmt.Errorf("file is required")
	}

	if s.depGraph == nil {
		return "Dependency graph not available", nil
	}

	impact := s.depGraph.ImpactSet(file)
	if len(impact) == 0 {
		return fmt.Sprintf("No files impacted by changes to %s", file), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Files impacted by changes to %s (%d):\n", file, len(impact))
	for _, f := range impact {
		fmt.Fprintf(&sb, "  %s\n", f)
	}
	return sb.String(), nil
}

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
		case "initialize":
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

		case "tools/list":
			tools := s.ToolDefinitions()
			var toolList []map[string]interface{}
			for _, t := range tools {
				toolList = append(toolList, map[string]interface{}{
					"name":        t.Name,
					"description": t.Description,
					"inputSchema": json.RawMessage(t.InputSchema),
				})
			}
			writeJSONRPC(os.Stdout, req.ID, map[string]interface{}{
				"tools": toolList,
			}, nil)

		case "tools/call":
			var params struct {
				Name      string                 `json:"name"`
				Arguments map[string]interface{} `json:"arguments"`
			}
			paramsBytes, _ := json.Marshal(req.Params)
			if err := json.Unmarshal(paramsBytes, &params); err != nil {
				writeJSONRPC(os.Stdout, req.ID, nil, &jsonRPCError{Code: -32602, Message: "Invalid params"})
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
	return os.WriteFile(configPath, data, 0o644)
}
