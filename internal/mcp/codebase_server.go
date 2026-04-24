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
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ericmacdougall/stoke/internal/depgraph"
	"github.com/ericmacdougall/stoke/internal/symindex"
	"github.com/ericmacdougall/stoke/internal/tfidf"
	"github.com/ericmacdougall/stoke/internal/vecindex"
)

// Error messages returned when a backing index/graph is not loaded.
// Kept centralized so we emit a consistent, greppable string everywhere.
const (
	msgSymbolIndexUnavailable = "Symbol index not available"
	msgDepGraphUnavailable    = "Dependency graph not available"
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

	// Build vector index for semantic search using symbol vocabulary
	// This captures dimensional relationships between code concepts
	// that TF-IDF misses (e.g., "authentication" ≈ "login" ≈ "session")
	vecIdx := buildVectorIndex(symIdx, tfidfIdx)

	srv := NewCodebaseServer(repoRoot, symIdx, depGraph, tfidfIdx)
	srv.vecIdx = vecIdx
	return srv, nil
}

// buildVectorIndex creates a vector index from the codebase vocabulary.
// Uses bag-of-words embedding over the symbol+term vocabulary, which
// captures richer relationships than pure TF-IDF matching.
func buildVectorIndex(symIdx *symindex.Index, tfidfIdx *tfidf.Index) *vecindex.Index {
	// Build vocabulary from symbol names (the semantic backbone of the codebase)
	vocabSet := make(map[string]bool)
	if symIdx != nil {
		for _, sym := range symIdx.AllSymbols() {
			// Expand camelCase/PascalCase into components
			for _, word := range expandIdentifier(sym.Name) {
				w := strings.ToLower(word)
				if len(w) >= 3 {
					vocabSet[w] = true
				}
			}
		}
	}

	vocab := make([]string, 0, len(vocabSet))
	for w := range vocabSet {
		vocab = append(vocab, w)
	}
	sort.Strings(vocab)

	// Cap vocabulary at 2000 dimensions for performance
	if len(vocab) > 2000 {
		vocab = vocab[:2000]
	}

	embedFn := vecindex.BagOfWordsEmbed(vocab)
	idx := vecindex.New(vecindex.Config{
		Dimension: len(vocab),
		EmbedFunc: embedFn,
	})

	// Index each file's symbols as a document
	if symIdx != nil {
		for _, file := range symIdx.Files() {
			syms := symIdx.InFile(file)
			var content strings.Builder
			for _, sym := range syms {
				fmt.Fprintf(&content, "%s %s %s ", sym.Kind, sym.Name, sym.Parent)
			}
			if content.Len() > 0 {
				idx.AddText(file, content.String(), file)
			}
		}
	}

	return idx
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
	case "find_symbol_usages":
		return s.handleFindSymbolUsages(args)
	case "trace_entry_points":
		return s.handleTraceEntryPoints(args)
	case "semantic_search":
		return s.handleSemanticSearch(args)
	case "get_call_graph":
		return s.handleGetCallGraph(args)
	case "get_interface_implementations":
		return s.handleGetInterfaceImplementations(args)
	case "get_symbol_detail":
		return s.handleGetSymbolDetail(args)
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
		return msgSymbolIndexUnavailable, nil
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
		return msgDepGraphUnavailable, nil
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

	results := s.tfidfIdx.SearchWithContext(query, limit)

	if len(results) == 0 {
		return fmt.Sprintf("No files found matching %q", query), nil
	}

	var sb strings.Builder
	for _, r := range results {
		fmt.Fprintf(&sb, "%.3f  %s", r.Score, r.Path)
		// Show matching terms so the model understands WHY this file matched
		if len(r.MatchingTerms) > 0 {
			fmt.Fprintf(&sb, "  (matched: %s)", strings.Join(r.MatchingTerms, ", "))
		}
		// Include exported symbols from the file for quick triage
		if s.symIdx != nil {
			syms := s.symIdx.InFile(r.Path)
			if len(syms) > 0 {
				var exported []string
				for _, sym := range syms {
					if sym.Exported && len(exported) < 5 {
						exported = append(exported, sym.Name)
					}
				}
				if len(exported) > 0 {
					fmt.Fprintf(&sb, "  [%s]", strings.Join(exported, ", "))
				}
			}
		}
		sb.WriteByte('\n')
	}
	return sb.String(), nil
}

func (s *CodebaseServer) handleGetFileSymbols(args map[string]interface{}) (string, error) {
	file, _ := args["file"].(string)
	if file == "" {
		return "", fmt.Errorf("file is required")
	}

	if s.symIdx == nil {
		return msgSymbolIndexUnavailable, nil
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
		return msgDepGraphUnavailable, nil
	}

	impact := s.depGraph.ImpactSet(file)
	if len(impact) == 0 {
		return fmt.Sprintf("No files impacted by changes to %s", file), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Files impacted by changes to %s (%d):\n", file, len(impact))
	for _, f := range impact {
		fmt.Fprintf(&sb, "  %s", f)
		// Show key exports so the model understands what each consumer provides
		if s.symIdx != nil {
			syms := s.symIdx.InFile(f)
			var exported []string
			for _, sym := range syms {
				if sym.Exported && len(exported) < 3 {
					exported = append(exported, sym.Name)
				}
			}
			if len(exported) > 0 {
				fmt.Fprintf(&sb, "  [%s]", strings.Join(exported, ", "))
			}
		}
		sb.WriteByte('\n')
	}
	return sb.String(), nil
}

func (s *CodebaseServer) handleFindSymbolUsages(args map[string]interface{}) (string, error) {
	symbol, _ := args["symbol"].(string)
	if symbol == "" {
		return "", fmt.Errorf("symbol is required")
	}

	limit := 20
	if l, ok := args["limit"].(float64); ok && l > 0 {
		limit = int(l)
	}

	if s.symIdx == nil {
		return msgSymbolIndexUnavailable, nil
	}

	// Find the definition(s) of this symbol
	defs := s.symIdx.Lookup(symbol)

	var sb strings.Builder
	if len(defs) > 0 {
		sb.WriteString("Definitions:\n")
		for _, d := range defs {
			fmt.Fprintf(&sb, "  %s %s (%s:%d)", d.Kind, d.Name, d.File, d.Line)
			if d.Parent != "" {
				fmt.Fprintf(&sb, " [parent: %s]", d.Parent)
			}
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	// Build set of files where the symbol is defined to exclude them
	defFiles := make(map[string]bool)
	for _, d := range defs {
		defFiles[d.File] = true
	}

	// Search content for files that reference this symbol
	if s.tfidfIdx != nil {
		results := s.tfidfIdx.SearchWithContext(symbol, limit*2)
		sb.WriteString("Referenced in:\n")
		count := 0
		for _, r := range results {
			if defFiles[r.Path] {
				continue
			}
			fmt.Fprintf(&sb, "  %.3f  %s", r.Score, r.Path)
			// Show what this consumer file defines
			if syms := s.symIdx.InFile(r.Path); len(syms) > 0 {
				var exported []string
				for _, sym := range syms {
					if sym.Exported && len(exported) < 3 {
						exported = append(exported, string(sym.Kind)+" "+sym.Name)
					}
				}
				if len(exported) > 0 {
					fmt.Fprintf(&sb, "  [defines: %s]", strings.Join(exported, ", "))
				}
			}
			// Show if this consumer has a direct dependency on a definition file
			if s.depGraph != nil {
				deps := s.depGraph.Dependencies(r.Path)
				for _, dep := range deps {
					if defFiles[dep] {
						fmt.Fprintf(&sb, " ←imports %s", filepath.Base(dep))
						break
					}
				}
			}
			sb.WriteByte('\n')
			count++
			if count >= limit {
				break
			}
		}
		if count == 0 {
			sb.WriteString("  (no consumer files found)\n")
		}
	} else {
		sb.WriteString("Content search index not available\n")
	}

	return sb.String(), nil
}

func (s *CodebaseServer) handleTraceEntryPoints(args map[string]interface{}) (string, error) {
	file, _ := args["file"].(string)
	if file == "" {
		return "", fmt.Errorf("file is required")
	}

	if s.depGraph == nil {
		return msgDepGraphUnavailable, nil
	}

	roots := s.depGraph.Roots()
	rootSet := make(map[string]bool, len(roots))
	for _, r := range roots {
		rootSet[r] = true
	}

	// BFS backward from the target file, recording the parent at each step
	// so we can reconstruct paths from roots to the target.
	parent := map[string]string{file: ""}
	queue := []string{file}

	// Build reverse adjacency for BFS
	rev := make(map[string][]string)
	rev[file] = append(rev[file], s.depGraph.Dependents(file)...)
	visited := map[string]bool{file: true}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		for _, dep := range s.depGraph.Dependents(current) {
			if !visited[dep] {
				visited[dep] = true
				parent[dep] = current
				queue = append(queue, dep)
			}
		}
	}

	// Collect paths from roots that can reach this file
	type entryPath struct {
		root string
		path []string
	}
	var entries []entryPath
	for node := range visited {
		if rootSet[node] && node != file {
			// Reconstruct path from this root to the target
			var chain []string
			cur := node
			for cur != "" {
				chain = append(chain, cur)
				cur = parent[cur]
			}
			entries = append(entries, entryPath{root: node, path: chain})
		}
	}

	// Also check if the file itself is a root
	if rootSet[file] {
		entries = append(entries, entryPath{root: file, path: []string{file}})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].root < entries[j].root
	})

	if len(entries) == 0 {
		return fmt.Sprintf("No entry points found that can reach %s", file), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Entry points that reach %s (%d):\n\n", file, len(entries))
	for _, e := range entries {
		// Classify the entry point by name heuristics
		surface := classifyEntryPoint(e.root)
		fmt.Fprintf(&sb, "  %s", e.root)
		if surface != "" {
			fmt.Fprintf(&sb, " [%s]", surface)
		}
		// Show key symbols in the entry point
		if s.symIdx != nil {
			syms := s.symIdx.InFile(e.root)
			var names []string
			for _, sym := range syms {
				if sym.Exported && len(names) < 3 {
					names = append(names, sym.Name)
				}
			}
			if len(names) > 0 {
				fmt.Fprintf(&sb, " {%s}", strings.Join(names, ", "))
			}
		}
		sb.WriteString("\n")
		// Show the dependency chain
		if len(e.path) > 1 {
			sb.WriteString("    chain: ")
			for i, p := range e.path {
				if i > 0 {
					sb.WriteString(" → ")
				}
				sb.WriteString(filepath.Base(p))
			}
			sb.WriteString("\n")
		}
	}
	return sb.String(), nil
}

func (s *CodebaseServer) handleSemanticSearch(args map[string]interface{}) (string, error) {
	query, _ := args["query"].(string)
	if query == "" {
		return "", fmt.Errorf("query is required")
	}

	limit := 10
	if l, ok := args["limit"].(float64); ok && l > 0 {
		limit = int(l)
	}

	if s.vecIdx == nil || s.vecIdx.Count() == 0 {
		// Fall back to TF-IDF if vector index not available
		if s.tfidfIdx != nil {
			return s.handleSearchContent(args)
		}
		return "Semantic search index not available", nil
	}

	results, err := s.vecIdx.SearchText(query, limit)
	if err != nil {
		return "", fmt.Errorf("semantic search: %w", err)
	}

	if len(results) == 0 {
		return fmt.Sprintf("No files found semantically matching %q", query), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Semantic matches for %q (%d results):\n", query, len(results))
	for _, r := range results {
		fmt.Fprintf(&sb, "  %.3f  %s", r.Score, r.Document.Path)
		// Show what this file defines
		if s.symIdx != nil {
			syms := s.symIdx.InFile(r.Document.Path)
			var exported []string
			for _, sym := range syms {
				if sym.Exported && len(exported) < 4 {
					exported = append(exported, string(sym.Kind)+" "+sym.Name)
				}
			}
			if len(exported) > 0 {
				fmt.Fprintf(&sb, "  [%s]", strings.Join(exported, ", "))
			}
		}
		sb.WriteByte('\n')
	}
	return sb.String(), nil
}

// classifyEntryPoint guesses the surface type from filename patterns.
func classifyEntryPoint(path string) string {
	base := strings.ToLower(filepath.Base(path))
	dir := strings.ToLower(filepath.Dir(path))

	// Check most specific directory patterns first, then fall back to name heuristics.
	switch {
	case strings.Contains(base, "mcp") || strings.Contains(dir, "mcp"):
		return "MCP"
	case strings.Contains(dir, "mobile") || strings.Contains(dir, "ios") ||
		strings.Contains(dir, "android"):
		return "Mobile"
	case strings.Contains(dir, "desktop") || strings.Contains(dir, "electron"):
		return "Desktop"
	case strings.Contains(base, "app.tsx") || strings.Contains(base, "app.jsx") ||
		strings.Contains(base, "index.html") || strings.Contains(dir, "pages") ||
		strings.Contains(dir, "components"):
		return "Web"
	case strings.Contains(base, "server") || strings.Contains(base, "handler") ||
		strings.Contains(base, "route") || strings.Contains(dir, "api"):
		return "API"
	case strings.Contains(base, "main") || strings.HasSuffix(base, "_cmd.go") || strings.Contains(dir, "cmd"):
		return "CLI"
	default:
		return ""
	}
}

// expandIdentifier splits camelCase/PascalCase/snake_case into words.
func expandIdentifier(name string) []string {
	// Split on underscores first
	parts := strings.Split(name, "_")
	var words []string
	for _, part := range parts {
		if part == "" {
			continue
		}
		// Split camelCase: insert boundary at lowercase→uppercase transition
		var current strings.Builder
		for i, r := range part {
			if i > 0 && r >= 'A' && r <= 'Z' {
				prev := rune(part[i-1])
				if prev >= 'a' && prev <= 'z' {
					if current.Len() > 0 {
						words = append(words, current.String())
						current.Reset()
					}
				}
			}
			current.WriteRune(r)
		}
		if current.Len() > 0 {
			words = append(words, current.String())
		}
	}
	return words
}

// ServeStdio runs the MCP server on stdin/stdout using JSON-RPC 2.0.
// This is the main entry point when the server is started as a subprocess.
func (s *CodebaseServer) handleGetCallGraph(args map[string]interface{}) (string, error) {
	symbol, _ := args["symbol"].(string)
	if symbol == "" {
		return "", fmt.Errorf("symbol is required")
	}
	direction := "both"
	if d, ok := args["direction"].(string); ok && d != "" {
		direction = d
	}
	limit := 20
	if l, ok := args["limit"].(float64); ok && l > 0 {
		limit = int(l)
	}

	if s.symIdx == nil {
		return msgSymbolIndexUnavailable, nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "# Call Graph: %s\n\n", symbol)

	if direction == "callers" || direction == "both" {
		callers := s.symIdx.Callers(symbol)
		fmt.Fprintf(&sb, "## Callers (%d)\n", len(callers))
		for i, c := range callers {
			if i >= limit {
				fmt.Fprintf(&sb, "... and %d more\n", len(callers)-limit)
				break
			}
			fmt.Fprintf(&sb, "- %s (%s:%d)\n", c.Caller, c.File, c.Line)
		}
		if len(callers) == 0 {
			sb.WriteString("No callers found (may be an entry point or external caller)\n")
		}
		sb.WriteString("\n")
	}

	if direction == "callees" || direction == "both" {
		callees := s.symIdx.Callees(symbol)
		fmt.Fprintf(&sb, "## Callees (%d)\n", len(callees))
		for i, c := range callees {
			if i >= limit {
				fmt.Fprintf(&sb, "... and %d more\n", len(callees)-limit)
				break
			}
			loc := ""
			if c.CalleePkg != "" {
				loc = c.CalleePkg + "."
			}
			fmt.Fprintf(&sb, "- %s%s (%s:%d)\n", loc, c.Callee, c.File, c.Line)
		}
		if len(callees) == 0 {
			sb.WriteString("No callees found (leaf function)\n")
		}
		sb.WriteString("\n")
	}

	return sb.String(), nil
}

func (s *CodebaseServer) handleGetInterfaceImplementations(args map[string]interface{}) (string, error) {
	ifaceName, _ := args["interface"].(string)
	if ifaceName == "" {
		return "", fmt.Errorf("interface is required")
	}

	if s.symIdx == nil {
		return msgSymbolIndexUnavailable, nil
	}

	implementors := s.symIdx.Implementors(ifaceName)

	var sb strings.Builder
	fmt.Fprintf(&sb, "# Types implementing %s\n\n", ifaceName)

	if len(implementors) == 0 {
		sb.WriteString("No implementations found.\n")
		sb.WriteString("Note: Interface satisfaction is checked via AST method-set analysis for Go files.\n")
	} else {
		for _, typ := range implementors {
			// Find the type's location
			syms := s.symIdx.Lookup(typ)
			for _, sym := range syms {
				if sym.Kind == symindex.KindType || sym.Kind == symindex.KindInterface {
					fmt.Fprintf(&sb, "- %s (%s:%d)\n", typ, sym.File, sym.Line)
					break
				}
			}
		}
	}

	return sb.String(), nil
}

func (s *CodebaseServer) handleGetSymbolDetail(args map[string]interface{}) (string, error) {
	name, _ := args["symbol"].(string)
	if name == "" {
		return "", fmt.Errorf("symbol is required")
	}

	if s.symIdx == nil {
		return msgSymbolIndexUnavailable, nil
	}

	syms := s.symIdx.Lookup(name)
	if len(syms) == 0 {
		return fmt.Sprintf("Symbol %q not found", name), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "# Symbol: %s (%d definitions)\n\n", name, len(syms))

	for _, sym := range syms {
		fmt.Fprintf(&sb, "## %s %s\n", sym.Kind, sym.Name)
		fmt.Fprintf(&sb, "- **File:** %s:%d", sym.File, sym.Line)
		if sym.EndLine > 0 {
			fmt.Fprintf(&sb, "-%d", sym.EndLine)
		}
		sb.WriteString("\n")
		fmt.Fprintf(&sb, "- **Exported:** %v\n", sym.Exported)
		if sym.Parent != "" {
			fmt.Fprintf(&sb, "- **Receiver/Parent:** %s\n", sym.Parent)
		}
		if sym.Signature != "" {
			fmt.Fprintf(&sb, "- **Signature:** `%s`\n", sym.Signature)
		}
		if sym.TypeName != "" {
			fmt.Fprintf(&sb, "- **Type:** %s\n", sym.TypeName)
		}
		if sym.Doc != "" {
			fmt.Fprintf(&sb, "- **Doc:** %s\n", strings.TrimSpace(sym.Doc))
		}

		// Show callers/callees if available
		qualified := sym.Name
		if sym.Parent != "" {
			qualified = sym.Parent + "." + sym.Name
		}
		callers := s.symIdx.Callers(sym.Name)
		callees := s.symIdx.Callees(qualified)
		if len(callers) > 0 {
			fmt.Fprintf(&sb, "- **Called by:** %d callers\n", len(callers))
		}
		if len(callees) > 0 {
			fmt.Fprintf(&sb, "- **Calls:** %d callees\n", len(callees))
		}

		sb.WriteString("\n")
	}

	return sb.String(), nil
}

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
	return os.WriteFile(configPath, data, 0o644) // #nosec G306 -- codebase server artefact; user-readable.
}
