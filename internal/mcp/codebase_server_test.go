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
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
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
	if len(defs) != 5 {
		t.Errorf("expected 5 tool definitions, got %d", len(defs))
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

	for _, expected := range []string{"search_symbols", "get_dependencies", "search_content", "get_file_symbols", "impact_analysis"} {
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
