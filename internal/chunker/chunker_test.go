package chunker

import (
	"strings"
	"testing"
)

func TestDetectLanguage(t *testing.T) {
	tests := []struct {
		path string
		want Language
	}{
		{"main.go", LangGo},
		{"app.py", LangPython},
		{"index.ts", LangTypeScript},
		{"lib.rs", LangRust},
		{"Main.java", LangJava},
		{"data.csv", LangUnknown},
	}
	for _, tt := range tests {
		got := DetectLanguage(tt.path)
		if got != tt.want {
			t.Errorf("DetectLanguage(%s) = %s, want %s", tt.path, got, tt.want)
		}
	}
}

func TestChunkGoFile(t *testing.T) {
	src := `package main

func hello() {
	fmt.Println("hello")
}

func world() {
	fmt.Println("world")
}

type Config struct {
	Name string
}`

	chunks := ChunkFile("main.go", src)
	if len(chunks) < 3 {
		t.Fatalf("expected at least 3 chunks, got %d", len(chunks))
	}

	names := make(map[string]bool)
	for _, c := range chunks {
		names[c.Name] = true
	}
	if !names["hello"] {
		t.Error("missing hello chunk")
	}
	if !names["world"] {
		t.Error("missing world chunk")
	}
	if !names["Config"] {
		t.Error("missing Config chunk")
	}
}

func TestChunkPythonFile(t *testing.T) {
	src := `class MyClass:
    def __init__(self):
        pass

    def method(self):
        return 1

def standalone():
    pass`

	chunks := ChunkFile("app.py", src)
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}

	hasClass := false
	for _, c := range chunks {
		if c.Kind == "class" {
			hasClass = true
		}
	}
	if !hasClass {
		t.Error("expected a class chunk")
	}
}

func TestChunkUnknownLanguage(t *testing.T) {
	src := "block one\n\nblock two\n\nblock three"
	chunks := ChunkFile("data.txt", src)
	if len(chunks) != 3 {
		t.Errorf("expected 3 blank-line-separated blocks, got %d", len(chunks))
	}
	for _, c := range chunks {
		if c.Kind != "block" {
			t.Errorf("expected block kind, got %s", c.Kind)
		}
	}
}

func TestChunkFileWithBudget(t *testing.T) {
	src := `func a() {
	// lots of code
}

func b() {
	// lots of code
}

func c() {
	// lots of code
}`

	chunks := ChunkFileWithBudget("main.go", src, 10)
	// Should only fit first chunk or so
	if len(chunks) > 2 {
		t.Errorf("budget should limit chunks, got %d", len(chunks))
	}
}

func TestFilterByName(t *testing.T) {
	chunks := []Chunk{
		{Name: "foo", Kind: "function"},
		{Name: "bar", Kind: "function"},
		{Name: "baz", Kind: "type"},
	}
	filtered := FilterByName(chunks, "foo", "baz")
	if len(filtered) != 2 {
		t.Errorf("expected 2, got %d", len(filtered))
	}
}

func TestFilterByKind(t *testing.T) {
	chunks := []Chunk{
		{Name: "foo", Kind: "function"},
		{Name: "bar", Kind: "function"},
		{Name: "baz", Kind: "type"},
	}
	filtered := FilterByKind(chunks, "function")
	if len(filtered) != 2 {
		t.Errorf("expected 2 functions, got %d", len(filtered))
	}
}

func TestRender(t *testing.T) {
	chunks := []Chunk{
		{File: "main.go", Name: "foo", Kind: "function", StartLine: 1, EndLine: 3, Content: "func foo() {}"},
	}
	out := Render(chunks)
	if !strings.Contains(out, "function foo") {
		t.Error("render should include kind and name")
	}
	if !strings.Contains(out, "main.go:1-3") {
		t.Error("render should include file:line range")
	}
}

func TestChunkTokenEstimate(t *testing.T) {
	src := `func example() {
	x := 1
	y := 2
	return x + y
}`
	chunks := ChunkFile("main.go", src)
	if len(chunks) == 0 {
		t.Fatal("expected at least 1 chunk")
	}
	if chunks[0].Tokens <= 0 {
		t.Error("token estimate should be positive")
	}
}
