package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// codeFixtureRegistry writes a small Go tree with a cross-file call edge
// (Bar calls Foo) and returns a registry rooted at it.
func codeFixtureRegistry(t *testing.T) *Registry {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"a.go": "package main\n\nfunc Foo() string { return \"foo\" }\n",
		"b.go": "package main\n\nfunc Bar() string { return Foo() + \"bar\" }\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return NewRegistry(dir)
}

var codeToolNames = []string{
	"search_symbols", "get_call_graph", "find_symbol_usages",
	"impact_analysis", "get_dependencies", "get_file_symbols", "search_content",
}

func definitionNames(r *Registry) map[string]bool {
	names := make(map[string]bool)
	for _, d := range r.Definitions() {
		names[d.Name] = true
	}
	return names
}

func TestCodeToolsAdvertised(t *testing.T) {
	names := definitionNames(codeFixtureRegistry(t))
	for _, want := range codeToolNames {
		if !names[want] {
			t.Errorf("Definitions() missing code tool %q", want)
		}
	}
}

func TestCodeToolsKillSwitch(t *testing.T) {
	t.Setenv(codeToolsDisabledEnv, "1")

	r := codeFixtureRegistry(t)
	names := definitionNames(r)
	for _, name := range codeToolNames {
		if names[name] {
			t.Errorf("Definitions() advertises %q despite %s=1", name, codeToolsDisabledEnv)
		}
	}

	// A disabled tool dispatched anyway degrades to an informational
	// result, not an error.
	got, err := r.Handle(context.Background(), "search_symbols", json.RawMessage(`{"query":"Foo"}`))
	if err != nil {
		t.Fatalf("Handle under kill switch errored: %v", err)
	}
	if got != msgCodeToolsDisabled {
		t.Errorf("got %q, want %q", got, msgCodeToolsDisabled)
	}
}

func TestCodeToolQueries(t *testing.T) {
	r := codeFixtureRegistry(t)
	ctx := context.Background()

	tests := []struct {
		name  string
		tool  string
		input string
		want  []string
	}{
		{
			name:  "search_symbols finds Foo with location",
			tool:  "search_symbols",
			input: `{"query":"Foo"}`,
			want:  []string{"Foo", "a.go"},
		},
		{
			name:  "get_call_graph callers of Foo include Bar",
			tool:  "get_call_graph",
			input: `{"symbol":"Foo","direction":"callers"}`,
			want:  []string{"## Callers", "Bar"},
		},
		{
			name:  "find_symbol_usages of Foo lists consumers",
			tool:  "find_symbol_usages",
			input: `{"symbol":"Foo"}`,
			want:  []string{"Definitions:", "a.go"},
		},
		{
			name:  "get_file_symbols lists Bar in b.go",
			tool:  "get_file_symbols",
			input: `{"file":"b.go"}`,
			want:  []string{"Bar"},
		},
		{
			name:  "get_dependencies renders both sections",
			tool:  "get_dependencies",
			input: `{"file":"a.go"}`,
			want:  []string{"File: a.go", "Imports", "Imported by"},
		},
		{
			name:  "impact_analysis answers for a leaf file",
			tool:  "impact_analysis",
			input: `{"file":"a.go"}`,
			want:  []string{"a.go"},
		},
		{
			name:  "search_content ranks the file containing the terms",
			tool:  "search_content",
			input: `{"query":"Bar bar"}`,
			want:  []string{"b.go"},
		},
		{
			name:  "PascalCase alias dispatches",
			tool:  "SearchSymbols",
			input: `{"query":"Bar"}`,
			want:  []string{"Bar", "b.go"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := r.Handle(ctx, tt.tool, json.RawMessage(tt.input))
			if err != nil {
				t.Fatalf("Handle(%s): %v", tt.tool, err)
			}
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("result missing %q; got:\n%s", w, got)
				}
			}
		})
	}
}

func TestCodeToolsMalformedInput(t *testing.T) {
	r := codeFixtureRegistry(t)
	ctx := context.Background()
	for _, tool := range codeToolNames {
		if _, err := r.Handle(ctx, tool, json.RawMessage(`{not json`)); err == nil {
			t.Errorf("Handle(%s) accepted malformed input", tool)
		}
	}
	// Missing required argument surfaces as an error the model can act on.
	if _, err := r.Handle(ctx, "search_symbols", json.RawMessage(`{}`)); err == nil {
		t.Error("search_symbols accepted an empty query")
	}
}

// TestWriteFileInvalidatesCodeIndex proves the write_file success path marks
// the lazy code index dirty, so symbols written mid-run are queryable.
func TestWriteFileInvalidatesCodeIndex(t *testing.T) {
	r := codeFixtureRegistry(t)
	ctx := context.Background()

	// Force the index to build BEFORE the write — only a pre-built index
	// can go stale.
	got, err := r.Handle(ctx, "search_symbols", json.RawMessage(`{"query":"Baz"}`))
	if err != nil {
		t.Fatalf("search_symbols: %v", err)
	}
	if !strings.Contains(got, "No symbols found") {
		t.Fatalf("Baz exists before write; got:\n%s", got)
	}

	if _, err := r.Handle(ctx, "write_file", json.RawMessage(`{"path":"d.go","content":"package main\n\nfunc Baz() {}\n"}`)); err != nil {
		t.Fatalf("write_file: %v", err)
	}

	got, err = r.Handle(ctx, "search_symbols", json.RawMessage(`{"query":"Baz"}`))
	if err != nil {
		t.Fatalf("search_symbols after write: %v", err)
	}
	if !strings.Contains(got, "Baz") || !strings.Contains(got, "d.go") {
		t.Errorf("write_file did not invalidate the code index; got:\n%s", got)
	}
}

// TestEditFileInvalidatesCodeIndex proves the edit_file success path marks
// the lazy code index dirty.
func TestEditFileInvalidatesCodeIndex(t *testing.T) {
	r := codeFixtureRegistry(t)
	ctx := context.Background()

	if _, err := r.Handle(ctx, "search_symbols", json.RawMessage(`{"query":"Qux"}`)); err != nil {
		t.Fatalf("search_symbols: %v", err)
	}

	// edit_file requires a prior read_file on the path.
	if _, err := r.Handle(ctx, "read_file", json.RawMessage(`{"path":"a.go"}`)); err != nil {
		t.Fatalf("read_file: %v", err)
	}
	edit := `{"path":"a.go","old_string":"func Foo() string { return \"foo\" }","new_string":"func Foo() string { return \"foo\" }\n\nfunc Qux() {}"}`
	if _, err := r.Handle(ctx, "edit_file", json.RawMessage(edit)); err != nil {
		t.Fatalf("edit_file: %v", err)
	}

	got, err := r.Handle(ctx, "search_symbols", json.RawMessage(`{"query":"Qux"}`))
	if err != nil {
		t.Fatalf("search_symbols after edit: %v", err)
	}
	if !strings.Contains(got, "Qux") {
		t.Errorf("edit_file did not invalidate the code index; got:\n%s", got)
	}
}

// TestWriteObserverFires proves SetWriteObserver sees the resolved absolute
// path for both write paths, and only on success.
func TestWriteObserverFires(t *testing.T) {
	r := codeFixtureRegistry(t)
	ctx := context.Background()

	var observed []string
	r.SetWriteObserver(func(absPath string) { observed = append(observed, absPath) })

	if _, err := r.Handle(ctx, "write_file", json.RawMessage(`{"path":"obs.go","content":"package main\n"}`)); err != nil {
		t.Fatalf("write_file: %v", err)
	}
	wantAbs := filepath.Join(r.WorkDir(), "obs.go")
	if len(observed) != 1 || observed[0] != wantAbs {
		t.Fatalf("observer saw %v, want [%s]", observed, wantAbs)
	}

	// A rejected write (path escape) must not fire the observer.
	if _, err := r.Handle(ctx, "write_file", json.RawMessage(`{"path":"../escape.go","content":"package main\n"}`)); err == nil {
		t.Fatal("expected path-escape write to fail")
	}
	if len(observed) != 1 {
		t.Errorf("observer fired on a failed write: %v", observed)
	}
}
