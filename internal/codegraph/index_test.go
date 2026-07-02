package codegraph

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureTree writes a small 3-file Go tree with cross-file call edges:
// a.go defines Foo, b.go's Bar calls Foo, c.go's main calls Bar.
func fixtureTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"a.go": `package main

func Foo() string { return "foo" }
`,
		"b.go": `package main

func Bar() string { return Foo() + "bar" }
`,
		"c.go": `package main

import "fmt"

func main() { fmt.Println(Bar()) }
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

func TestBuildAndQueries(t *testing.T) {
	dir := fixtureTree(t)
	ix, err := Build(dir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if ix.Sym() == nil || ix.Dep() == nil || ix.TFIDF() == nil || ix.Vec() == nil {
		t.Fatal("Build left an index nil")
	}
	if ix.Root() != dir {
		t.Errorf("Root() = %q, want %q", ix.Root(), dir)
	}

	tests := []struct {
		name string
		call func() (string, error)
		want []string
	}{
		{
			name: "SearchSymbols finds Foo with location",
			call: func() (string, error) { return ix.SearchSymbols("Foo", "", 20) },
			want: []string{"Foo", "a.go"},
		},
		{
			name: "SearchSymbols no match",
			call: func() (string, error) { return ix.SearchSymbols("NoSuchSymbolXyz", "", 20) },
			want: []string{"No symbols found"},
		},
		{
			name: "GetCallGraph callers of Foo include Bar",
			call: func() (string, error) { return ix.GetCallGraph("Foo", "callers", 20) },
			want: []string{"# Call Graph: Foo", "## Callers", "Bar"},
		},
		{
			name: "GetCallGraph callees of Bar include Foo",
			call: func() (string, error) { return ix.GetCallGraph("Bar", "callees", 20) },
			want: []string{"## Callees", "Foo"},
		},
		{
			name: "FindSymbolUsages of Foo lists definition and consumers",
			call: func() (string, error) { return ix.FindSymbolUsages("Foo", 20) },
			want: []string{"Definitions:", "a.go", "Referenced in:"},
		},
		{
			name: "GetFileSymbols lists Bar in b.go",
			call: func() (string, error) { return ix.GetFileSymbols("b.go") },
			want: []string{"Bar"},
		},
		{
			name: "GetDependencies renders both sections",
			call: func() (string, error) { return ix.GetDependencies("a.go") },
			want: []string{"File: a.go", "Imports", "Imported by"},
		},
		{
			name: "SearchContent ranks the file containing the terms",
			call: func() (string, error) { return ix.SearchContent("Bar bar", 5) },
			want: []string{"b.go"},
		},
		{
			name: "GetSymbolDetail renders location and export status",
			call: func() (string, error) { return ix.GetSymbolDetail("Foo") },
			want: []string{"# Symbol: Foo", "a.go", "**Exported:** true"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.call()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("result missing %q; got:\n%s", w, got)
				}
			}
		})
	}
}

func TestArgValidation(t *testing.T) {
	ix := &Index{}
	tests := []struct {
		name string
		call func() (string, error)
	}{
		{"SearchSymbols empty query", func() (string, error) { return ix.SearchSymbols("", "", 0) }},
		{"GetDependencies empty file", func() (string, error) { return ix.GetDependencies("") }},
		{"SearchContent empty query", func() (string, error) { return ix.SearchContent("", 0) }},
		{"GetFileSymbols empty file", func() (string, error) { return ix.GetFileSymbols("") }},
		{"ImpactAnalysis empty file", func() (string, error) { return ix.ImpactAnalysis("") }},
		{"FindSymbolUsages empty symbol", func() (string, error) { return ix.FindSymbolUsages("", 0) }},
		{"TraceEntryPoints empty file", func() (string, error) { return ix.TraceEntryPoints("") }},
		{"SemanticSearch empty query", func() (string, error) { return ix.SemanticSearch("", 0) }},
		{"GetCallGraph empty symbol", func() (string, error) { return ix.GetCallGraph("", "", 0) }},
		{"GetInterfaceImplementations empty", func() (string, error) { return ix.GetInterfaceImplementations("") }},
		{"GetSymbolDetail empty symbol", func() (string, error) { return ix.GetSymbolDetail("") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.call(); err == nil {
				t.Error("expected required-argument error, got nil")
			}
		})
	}
}

func TestZeroValueIndexDegradesGracefully(t *testing.T) {
	ix := &Index{}
	tests := []struct {
		name string
		call func() (string, error)
		want string
	}{
		{"SearchSymbols", func() (string, error) { return ix.SearchSymbols("x", "", 0) }, "Symbol index not available"},
		{"GetDependencies", func() (string, error) { return ix.GetDependencies("x.go") }, "Dependency graph not available"},
		{"SearchContent", func() (string, error) { return ix.SearchContent("x", 0) }, "Content search index not available"},
		{"SemanticSearch", func() (string, error) { return ix.SemanticSearch("x", 0) }, "Semantic search index not available"},
		{"GetCallGraph", func() (string, error) { return ix.GetCallGraph("x", "", 0) }, "Symbol index not available"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.call()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestImpactAnalysisFollowsDependents(t *testing.T) {
	dir := t.TempDir()
	// TS files give depgraph unambiguous file-level import edges.
	if err := os.WriteFile(filepath.Join(dir, "util.ts"), []byte("export const u = 1;\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.ts"), []byte("import { u } from './util';\nconsole.log(u);\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ix, err := Build(dir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// depgraph keys edges by import specifier, so the impact query uses
	// the path app.ts imports ("./util"), not the target's file name.
	got, err := ix.ImpactAnalysis("./util")
	if err != nil {
		t.Fatalf("ImpactAnalysis: %v", err)
	}
	if !strings.Contains(got, "app.ts") {
		t.Errorf("impact set missing dependent app.ts; got:\n%s", got)
	}
}

func TestMarkDirtyRebuildsOnQuery(t *testing.T) {
	dir := fixtureTree(t)
	ix, err := Build(dir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Baseline: Baz does not exist yet.
	got, err := ix.SearchSymbols("Baz", "", 20)
	if err != nil {
		t.Fatalf("SearchSymbols: %v", err)
	}
	if !strings.Contains(got, "No symbols found") {
		t.Fatalf("expected no Baz before write, got:\n%s", got)
	}

	if err := os.WriteFile(filepath.Join(dir, "d.go"), []byte("package main\n\nfunc Baz() {}\n"), 0o600); err != nil {
		t.Fatalf("write d.go: %v", err)
	}

	// Without MarkDirty the index stays stale.
	got, _ = ix.SearchSymbols("Baz", "", 20)
	if !strings.Contains(got, "No symbols found") {
		t.Fatalf("index refreshed without MarkDirty; got:\n%s", got)
	}

	ix.MarkDirty("d.go")
	got, err = ix.SearchSymbols("Baz", "", 20)
	if err != nil {
		t.Fatalf("SearchSymbols after MarkDirty: %v", err)
	}
	if !strings.Contains(got, "Baz") || !strings.Contains(got, "d.go") {
		t.Errorf("expected Baz (d.go) after MarkDirty refresh, got:\n%s", got)
	}
}

func TestFailedRefreshKeepsServingOldIndexes(t *testing.T) {
	dir := fixtureTree(t)
	ix, err := Build(dir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remove root: %v", err)
	}
	ix.MarkDirty("a.go")

	got, err := ix.SearchSymbols("Foo", "", 20)
	if err != nil {
		t.Fatalf("SearchSymbols after failed refresh: %v", err)
	}
	if !strings.Contains(got, "Foo") {
		t.Errorf("old index no longer serving after failed refresh; got:\n%s", got)
	}
}

func TestMarkDirtyEmptyPathIgnored(t *testing.T) {
	ix := &Index{root: "/nonexistent"}
	ix.MarkDirty("")
	if len(ix.dirty) != 0 {
		t.Error("empty path must not mark the index dirty")
	}
}
