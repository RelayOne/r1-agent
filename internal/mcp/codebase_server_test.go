package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// Create a small Go project
	writeFile(t, dir, "main.go", `package main

import "fmt"

func main() {
	svc := NewService()
	fmt.Println(svc.Process("hello"))
}
`)
	writeFile(t, dir, "service.go", `package main

type Service struct {
	cache map[string]string
}

func NewService() *Service {
	return &Service{cache: make(map[string]string)}
}

func (s *Service) Process(input string) string {
	return "processed: " + input
}
`)
	writeFile(t, dir, "service_test.go", `package main

import "testing"

func TestProcess(t *testing.T) {
	svc := NewService()
	if svc.Process("x") != "processed: x" {
		t.Error("unexpected result")
	}
}
`)
	return dir
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestBuildCodebaseServer(t *testing.T) {
	dir := setupTestRepo(t)
	srv, err := BuildCodebaseServer(dir)
	if err != nil {
		t.Fatalf("BuildCodebaseServer: %v", err)
	}
	if srv == nil {
		t.Fatal("server is nil")
	}
	if srv.symIdx == nil {
		t.Error("symIdx is nil")
	}
	if srv.depGraph == nil {
		t.Error("depGraph is nil")
	}
	if srv.tfidfIdx == nil {
		t.Error("tfidfIdx is nil")
	}
}

func TestToolDefinitions(t *testing.T) {
	srv := &CodebaseServer{}
	defs := srv.ToolDefinitions()
	if len(defs) != 11 {
		t.Errorf("expected 11 tool definitions, got %d", len(defs))
	}

	names := map[string]bool{}
	for _, d := range defs {
		names[d.Name] = true
		if d.Description == "" {
			t.Errorf("tool %s has empty description", d.Name)
		}
		if len(d.InputSchema) == 0 {
			t.Errorf("tool %s has empty input schema", d.Name)
		}
	}

	for _, expected := range []string{"search_symbols", "get_dependencies", "search_content", "get_file_symbols", "impact_analysis", "find_symbol_usages", "trace_entry_points", "semantic_search"} {
		if !names[expected] {
			t.Errorf("missing tool: %s", expected)
		}
	}
}

func TestSearchSymbols(t *testing.T) {
	dir := setupTestRepo(t)
	srv, err := BuildCodebaseServer(dir)
	if err != nil {
		t.Fatalf("BuildCodebaseServer: %v", err)
	}

	result, err := srv.HandleToolCall("search_symbols", map[string]interface{}{
		"query": "Service",
	})
	if err != nil {
		t.Fatalf("search_symbols error: %v", err)
	}
	if !strings.Contains(result, "Service") {
		t.Errorf("expected Service in results, got: %s", result)
	}
}

func TestSearchSymbolsWithKindFilter(t *testing.T) {
	dir := setupTestRepo(t)
	srv, err := BuildCodebaseServer(dir)
	if err != nil {
		t.Fatalf("BuildCodebaseServer: %v", err)
	}

	result, err := srv.HandleToolCall("search_symbols", map[string]interface{}{
		"query": "Service",
		"kind":  "type",
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if !strings.Contains(result, "type") {
		t.Errorf("expected 'type' kind in results, got: %s", result)
	}
	if strings.Contains(result, "function") {
		t.Errorf("should not contain function kinds when filtering for type")
	}
}

func TestSearchSymbolsEmpty(t *testing.T) {
	dir := setupTestRepo(t)
	srv, err := BuildCodebaseServer(dir)
	if err != nil {
		t.Fatalf("BuildCodebaseServer: %v", err)
	}

	result, err := srv.HandleToolCall("search_symbols", map[string]interface{}{
		"query": "NonExistentXYZ123",
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if !strings.Contains(result, "No symbols found") {
		t.Errorf("expected 'No symbols found', got: %s", result)
	}
}

func TestGetDependencies(t *testing.T) {
	dir := setupTestRepo(t)
	srv, err := BuildCodebaseServer(dir)
	if err != nil {
		t.Fatalf("BuildCodebaseServer: %v", err)
	}

	result, err := srv.HandleToolCall("get_dependencies", map[string]interface{}{
		"file": "main.go",
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if !strings.Contains(result, "main.go") {
		t.Errorf("expected file path in results, got: %s", result)
	}
	if !strings.Contains(result, "Imports") {
		t.Errorf("expected Imports section, got: %s", result)
	}
	if !strings.Contains(result, "Imported by") {
		t.Errorf("expected Imported by section, got: %s", result)
	}
}

func TestSearchContent(t *testing.T) {
	dir := setupTestRepo(t)
	srv, err := BuildCodebaseServer(dir)
	if err != nil {
		t.Fatalf("BuildCodebaseServer: %v", err)
	}

	result, err := srv.HandleToolCall("search_content", map[string]interface{}{
		"query": "process service cache",
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if !strings.Contains(result, "service.go") {
		t.Errorf("expected service.go in results, got: %s", result)
	}
}

func TestGetFileSymbols(t *testing.T) {
	dir := setupTestRepo(t)
	srv, err := BuildCodebaseServer(dir)
	if err != nil {
		t.Fatalf("BuildCodebaseServer: %v", err)
	}

	result, err := srv.HandleToolCall("get_file_symbols", map[string]interface{}{
		"file": "service.go",
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if !strings.Contains(result, "Service") {
		t.Errorf("expected Service type in results, got: %s", result)
	}
	if !strings.Contains(result, "NewService") {
		t.Errorf("expected NewService function in results, got: %s", result)
	}
}

func TestImpactAnalysis(t *testing.T) {
	dir := setupTestRepo(t)
	srv, err := BuildCodebaseServer(dir)
	if err != nil {
		t.Fatalf("BuildCodebaseServer: %v", err)
	}

	result, err := srv.HandleToolCall("impact_analysis", map[string]interface{}{
		"file": "service.go",
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	// Result may or may not contain impacted files depending on depgraph resolution
	if !strings.Contains(result, "service.go") {
		t.Errorf("expected file name in results, got: %s", result)
	}
}

func TestFindSymbolUsages(t *testing.T) {
	dir := setupTestRepo(t)
	srv, err := BuildCodebaseServer(dir)
	if err != nil {
		t.Fatalf("BuildCodebaseServer: %v", err)
	}

	result, err := srv.HandleToolCall("find_symbol_usages", map[string]interface{}{
		"symbol": "NewService",
	})
	if err != nil {
		t.Fatalf("find_symbol_usages error: %v", err)
	}
	if !strings.Contains(result, "Definitions:") {
		t.Errorf("expected Definitions section, got: %s", result)
	}
	if !strings.Contains(result, "NewService") {
		t.Errorf("expected NewService in results, got: %s", result)
	}
	if !strings.Contains(result, "Referenced in:") {
		t.Errorf("expected Referenced in section, got: %s", result)
	}
}

func TestFindSymbolUsagesNoDefinition(t *testing.T) {
	dir := setupTestRepo(t)
	srv, err := BuildCodebaseServer(dir)
	if err != nil {
		t.Fatalf("BuildCodebaseServer: %v", err)
	}

	result, err := srv.HandleToolCall("find_symbol_usages", map[string]interface{}{
		"symbol": "NonExistentSymbol",
	})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	// Should not have a Definitions section when symbol not found
	if strings.Contains(result, "Definitions:") {
		t.Errorf("should not have Definitions for unknown symbol, got: %s", result)
	}
}

func TestFindSymbolUsagesMissingArg(t *testing.T) {
	srv := &CodebaseServer{}
	_, err := srv.HandleToolCall("find_symbol_usages", map[string]interface{}{})
	if err == nil {
		t.Error("find_symbol_usages should require symbol")
	}
}

func TestTraceEntryPoints(t *testing.T) {
	dir := setupTestRepo(t)
	srv, err := BuildCodebaseServer(dir)
	if err != nil {
		t.Fatalf("BuildCodebaseServer: %v", err)
	}

	// service.go should be reachable from main.go (which is a root)
	result, err := srv.HandleToolCall("trace_entry_points", map[string]interface{}{
		"file": "service.go",
	})
	if err != nil {
		t.Fatalf("trace_entry_points error: %v", err)
	}
	if !strings.Contains(result, "service.go") {
		t.Errorf("expected service.go in results, got: %s", result)
	}
}

func TestTraceEntryPointsMissingArg(t *testing.T) {
	srv := &CodebaseServer{}
	_, err := srv.HandleToolCall("trace_entry_points", map[string]interface{}{})
	if err == nil {
		t.Error("trace_entry_points should require file")
	}
}

func TestTraceEntryPointsNilGraph(t *testing.T) {
	srv := &CodebaseServer{}
	result, err := srv.HandleToolCall("trace_entry_points", map[string]interface{}{"file": "test.go"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "not available") {
		t.Errorf("expected 'not available' for nil graph, got: %s", result)
	}
}

func TestSemanticSearch(t *testing.T) {
	dir := setupTestRepo(t)
	srv, err := BuildCodebaseServer(dir)
	if err != nil {
		t.Fatalf("BuildCodebaseServer: %v", err)
	}

	result, err := srv.HandleToolCall("semantic_search", map[string]interface{}{
		"query": "service process",
	})
	if err != nil {
		t.Fatalf("semantic_search error: %v", err)
	}
	// Should find something — the test repo has Service/Process/NewService
	if strings.Contains(result, "not available") {
		t.Error("semantic search should be available after build")
	}
}

func TestSemanticSearchNilIndex(t *testing.T) {
	// With no vecindex but with tfidf, should fall back
	srv := &CodebaseServer{}
	result, err := srv.HandleToolCall("semantic_search", map[string]interface{}{"query": "test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "not available") {
		t.Errorf("expected 'not available' for nil indexes, got: %s", result)
	}
}

func TestExpandIdentifier(t *testing.T) {
	tests := []struct {
		input  string
		expect []string
	}{
		{"NewService", []string{"New", "Service"}},
		{"getHTTPClient", []string{"get", "HTTPClient"}},
		{"snake_case_name", []string{"snake", "case", "name"}},
		{"simple", []string{"simple"}},
	}
	for _, tt := range tests {
		got := expandIdentifier(tt.input)
		if len(got) != len(tt.expect) {
			t.Errorf("expandIdentifier(%q) = %v, want %v", tt.input, got, tt.expect)
		}
	}
}

func TestClassifyEntryPoint(t *testing.T) {
	tests := []struct {
		path   string
		expect string
	}{
		{"cmd/server/main.go", "CLI"},
		{"internal/api/handler.go", "API"},
		{"src/pages/index.tsx", "Web"},
		{"mobile/ios/App.swift", "Mobile"},
		{"desktop/electron/main.js", "Desktop"},
		{"internal/mcp/server.go", "MCP"},
		{"internal/utils/helper.go", ""},
	}
	for _, tt := range tests {
		got := classifyEntryPoint(tt.path)
		if got != tt.expect {
			t.Errorf("classifyEntryPoint(%q) = %q, want %q", tt.path, got, tt.expect)
		}
	}
}

func TestUnknownTool(t *testing.T) {
	srv := &CodebaseServer{}
	_, err := srv.HandleToolCall("nonexistent", nil)
	if err == nil {
		t.Error("expected error for unknown tool")
	}
}

func TestNilIndexes(t *testing.T) {
	srv := &CodebaseServer{}

	result, _ := srv.HandleToolCall("search_symbols", map[string]interface{}{"query": "test"})
	if !strings.Contains(result, "not available") {
		t.Errorf("expected 'not available' for nil index, got: %s", result)
	}

	result, _ = srv.HandleToolCall("get_dependencies", map[string]interface{}{"file": "test.go"})
	if !strings.Contains(result, "not available") {
		t.Errorf("expected 'not available' for nil graph, got: %s", result)
	}

	result, _ = srv.HandleToolCall("search_content", map[string]interface{}{"query": "test"})
	if !strings.Contains(result, "not available") {
		t.Errorf("expected 'not available' for nil tfidf, got: %s", result)
	}
}

func TestWriteMCPConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "mcp.json")

	err := WriteMCPConfig(configPath, "/usr/bin/stoke", "/repo")
	if err != nil {
		t.Fatalf("WriteMCPConfig: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "stoke-codebase") {
		t.Error("config should contain stoke-codebase server name")
	}
	if !strings.Contains(content, "mcp-serve") {
		t.Error("config should contain mcp-serve command")
	}
}

func TestMissingRequiredArgs(t *testing.T) {
	srv := &CodebaseServer{}

	_, err := srv.HandleToolCall("search_symbols", map[string]interface{}{})
	if err == nil {
		t.Error("search_symbols should require query")
	}

	_, err = srv.HandleToolCall("get_dependencies", map[string]interface{}{})
	if err == nil {
		t.Error("get_dependencies should require file")
	}

	_, err = srv.HandleToolCall("search_content", map[string]interface{}{})
	if err == nil {
		t.Error("search_content should require query")
	}
}
