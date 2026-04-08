package repomap

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
	os.MkdirAll(filepath.Join(dir, "cmd"), 0755)
	os.MkdirAll(filepath.Join(dir, "pkg", "util"), 0755)

	// main.go
	os.WriteFile(filepath.Join(dir, "cmd", "main.go"), []byte(`package main

import (
	"fmt"
	"myapp/pkg/util"
)

func Main() {
	fmt.Println(util.Hello())
}

type Config struct {
	Name string
}

func NewConfig(name string) *Config {
	return &Config{Name: name}
}
`), 0644)

	// util.go
	os.WriteFile(filepath.Join(dir, "pkg", "util", "util.go"), []byte(`package util

import "strings"

func Hello() string {
	return "hello"
}

func FormatName(first, last string) string {
	return strings.TrimSpace(first + " " + last)
}

type Helper struct {
	Value int
}

func (h *Helper) Process() int {
	return h.Value * 2
}

func (h *Helper) Reset() {
	h.Value = 0
}
`), 0644)

	// test file (should be skipped)
	os.WriteFile(filepath.Join(dir, "pkg", "util", "util_test.go"), []byte(`package util

func TestHello(t *testing.T) {}
`), 0644)

	return dir
}

func TestBuild(t *testing.T) {
	dir := setupTestRepo(t)

	rm, err := Build(dir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if len(rm.Files) != 2 {
		t.Errorf("expected 2 files (excluding tests), got %d", len(rm.Files))
	}

	// Check symbols were extracted
	if len(rm.Symbols) == 0 {
		t.Fatal("expected symbols to be extracted")
	}

	// Check specific symbols
	found := map[string]bool{}
	for _, sym := range rm.Symbols {
		found[sym.Name] = true
	}
	for _, name := range []string{"Main", "Config", "NewConfig", "Hello", "FormatName", "Helper", "Process", "Reset"} {
		if !found[name] {
			t.Errorf("expected to find symbol %q", name)
		}
	}
}

func TestRender(t *testing.T) {
	dir := setupTestRepo(t)
	rm, _ := Build(dir)

	output := rm.Render(0)
	if !strings.Contains(output, "Repository Map") {
		t.Error("expected header")
	}
	if !strings.Contains(output, "Hello") {
		t.Error("expected Hello function in output")
	}
	if !strings.Contains(output, "type Config") || !strings.Contains(output, "type Helper") {
		t.Error("expected type declarations")
	}
}

func TestRenderBudget(t *testing.T) {
	dir := setupTestRepo(t)
	rm, _ := Build(dir)

	small := rm.Render(10) // very small budget
	full := rm.Render(0)

	if len(small) >= len(full) {
		t.Error("budget-limited output should be shorter")
	}
}

func TestRenderRelevant(t *testing.T) {
	dir := setupTestRepo(t)
	rm, _ := Build(dir)

	// Rendering relevant to util should boost util symbols
	output := rm.RenderRelevant([]string{"pkg/util/util.go"}, 0)
	if !strings.Contains(output, "Hello") {
		t.Error("expected Hello in relevant output")
	}
}

func TestParseGoFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.go")
	os.WriteFile(path, []byte(`package foo

import (
	"fmt"
	"strings"
)

const (
	MaxSize = 100
	minSize = 10
)

type MyStruct struct {
	Field int
}

type Doer interface {
	Do()
}

func (m *MyStruct) PublicMethod(ctx context.Context, name string) error {
	return nil
}

func privateFunc() {}

func PublicFunc(a, b, c, d int) int {
	return a + b + c + d
}
`), 0644)

	node, err := parseGoFile(path, "test.go")
	if err != nil {
		t.Fatal(err)
	}

	if node.Package != "foo" {
		t.Errorf("expected package foo, got %s", node.Package)
	}
	if len(node.Imports) != 2 {
		t.Errorf("expected 2 imports, got %d", len(node.Imports))
	}

	// Should only have public symbols
	names := map[string]bool{}
	for _, sym := range node.Symbols {
		names[sym.Name] = true
	}
	if !names["MaxSize"] {
		t.Error("expected MaxSize const")
	}
	if names["minSize"] {
		t.Error("did not expect private minSize")
	}
	if !names["MyStruct"] {
		t.Error("expected MyStruct type")
	}
	if !names["Doer"] {
		t.Error("expected Doer interface")
	}
	if !names["PublicMethod"] {
		t.Error("expected PublicMethod")
	}
	if names["privateFunc"] {
		t.Error("did not expect privateFunc")
	}
	if !names["PublicFunc"] {
		t.Error("expected PublicFunc")
	}
}

func TestSkipHiddenAndVendor(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".hidden"), 0755)
	os.MkdirAll(filepath.Join(dir, "vendor", "pkg"), 0755)
	os.MkdirAll(filepath.Join(dir, "src"), 0755)

	os.WriteFile(filepath.Join(dir, ".hidden", "hidden.go"),
		[]byte("package hidden\nfunc Hidden() {}"), 0644)
	os.WriteFile(filepath.Join(dir, "vendor", "pkg", "vendor.go"),
		[]byte("package pkg\nfunc Vendored() {}"), 0644)
	os.WriteFile(filepath.Join(dir, "src", "real.go"),
		[]byte("package src\nfunc Real() {}"), 0644)

	rm, _ := Build(dir)
	if len(rm.Files) != 1 {
		t.Errorf("expected 1 file (src/real.go only), got %d", len(rm.Files))
	}
}

func TestSummarizeParams(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", ""},
		{"ctx context.Context", "ctx context.Context"},
		{"a, b, c int", "a, b, c int"},
		{"a int, b int, c int, d int", "a int, ... +3 more"},
	}
	for _, tc := range tests {
		got := summarizeParams(tc.in)
		if got != tc.want {
			t.Errorf("summarizeParams(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
