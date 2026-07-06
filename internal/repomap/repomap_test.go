package repomap

import (
	"fmt"
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
`), 0o600)

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
`), 0o600)

	// test file (should be skipped)
	os.WriteFile(filepath.Join(dir, "pkg", "util", "util_test.go"), []byte(`package util

func TestHello(t *testing.T) {}
`), 0o600)

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

// TestRenderBudgetMoreFilesCount proves the budget-truncation footer reports
// the real number of dropped files. The old code used len(sorted)-len(groups),
// which is structurally always 0 ("... (0 more files)").
func TestRenderBudgetMoreFilesCount(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a", "b", "c"} {
		d := filepath.Join(dir, name)
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
		src := fmt.Sprintf("package %s\n\nfunc Fn%s() {}\n", name, strings.ToUpper(name))
		if err := os.WriteFile(filepath.Join(d, name+".go"), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rm, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rm.Files) != 3 {
		t.Fatalf("expected 3 files, got %d", len(rm.Files))
	}

	// Header (5 tokens) + one 1-symbol section (5 tokens) fits in 12; the
	// remaining file sections are truncated.
	out := rm.Render(12)
	if !strings.Contains(out, "more files)") {
		t.Fatalf("expected a truncation footer, got:\n%s", out)
	}
	if strings.Contains(out, "0 more files") {
		t.Fatalf("truncation footer wrongly reports 0 more files (the off-by bug):\n%s", out)
	}
	rendered := strings.Count(out, "## ")
	wantMore := 3 - rendered
	if wantMore <= 0 {
		t.Fatalf("test setup rendered all files; nothing was truncated:\n%s", out)
	}
	if !strings.Contains(out, fmt.Sprintf("%d more files", wantMore)) {
		t.Fatalf("expected footer to report %d more files, got:\n%s", wantMore, out)
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
`), 0o600)

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
		[]byte("package hidden\nfunc Hidden() {}"), 0o600)
	os.WriteFile(filepath.Join(dir, "vendor", "pkg", "vendor.go"),
		[]byte("package pkg\nfunc Vendored() {}"), 0o600)
	os.WriteFile(filepath.Join(dir, "src", "real.go"),
		[]byte("package src\nfunc Real() {}"), 0o600)

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

// --- B3: language-agnostic fallback (non-Go repos) ---

func setupPythonRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "app"), 0755); err != nil {
		t.Fatal(err)
	}
	main := `import app.util

class App:
    def run(self):
        pass

def main():
    pass
`
	util := `def helper():
    pass

def parse():
    pass

def load():
    pass

def save():
    pass
`
	if err := os.WriteFile(filepath.Join(dir, "app", "main.py"), []byte(main), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app", "util.py"), []byte(util), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func symbolNames(rm *RepoMap) map[string]bool {
	found := make(map[string]bool)
	for _, s := range rm.Symbols {
		found[s.Name] = true
	}
	return found
}

func TestBuildPythonRepo(t *testing.T) {
	rm, err := Build(setupPythonRepo(t))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(rm.Files) == 0 {
		t.Fatal("len(rm.Files)==0 — fallback did not fire for a Python repo")
	}
	if len(rm.Symbols) == 0 {
		t.Fatal("len(rm.Symbols)==0 — no Python symbols extracted via fallback")
	}
	found := symbolNames(rm)
	if !found["App"] || !found["helper"] {
		t.Fatalf("expected Python symbols App and helper; got %v", found)
	}
}

func TestRenderPython(t *testing.T) {
	rm, _ := Build(setupPythonRepo(t))
	out := rm.Render(0)
	if !strings.Contains(out, "Repository Map") {
		t.Fatalf("Render output missing header; got: %q", out)
	}
	if !strings.Contains(out, "App") && !strings.Contains(out, "helper") {
		t.Fatalf("Render output missing any Python symbol; got: %q", out)
	}
}

func TestRenderRelevantPython(t *testing.T) {
	rm, _ := Build(setupPythonRepo(t))
	out := rm.RenderRelevant([]string{"app/main.py"}, 0)
	// The exact empty-header defect B3 fixes:
	if len(out) <= len("# Repository Map\n\n") {
		t.Fatalf("RenderRelevant emitted only the empty header (B3 defect); got %q", out)
	}
	// app/util.py has more symbols (helper/parse/load/save) -> ranks high; its
	// symbols must appear, demonstrating the symbol-count bonus ranking.
	if !strings.Contains(out, "helper") {
		t.Fatalf("expected top-ranked app/util.py symbol 'helper' in output; got: %q", out)
	}
}

func setupTSRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0755); err != nil {
		t.Fatal(err)
	}
	app := `import { Helper } from "./util";

export interface Config {
  name: string;
}

export class App {
  async start() {}
}
`
	util := `export function createHelper(): string {
  return "h";
}

export const VERSION = "1.0";
`
	if err := os.WriteFile(filepath.Join(dir, "src", "app.ts"), []byte(app), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "util.ts"), []byte(util), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestBuildTSRepo(t *testing.T) {
	rm, _ := Build(setupTSRepo(t))
	if len(rm.Files) == 0 {
		t.Fatal("len(rm.Files)==0 — fallback did not fire for a TypeScript repo")
	}
	found := symbolNames(rm)
	if !found["Config"] || !found["App"] {
		t.Fatalf("expected TS symbols Config and App; got %v", found)
	}
}

func TestRenderRelevantQueryNilMatchesRenderRelevant(t *testing.T) {
	dir := setupTestRepo(t)
	rm, _ := Build(dir)

	relevant := []string{"pkg/util/util.go"}
	plain := rm.RenderRelevant(relevant, 0)
	query := rm.RenderRelevantQuery(relevant, nil, 0)
	if plain != query {
		t.Errorf("nil-score RenderRelevantQuery diverged from RenderRelevant:\n--- plain ---\n%s\n--- query ---\n%s", plain, query)
	}
}

func TestRenderRelevantQueryBoostsScoredFile(t *testing.T) {
	dir := setupTestRepo(t)
	rm, _ := Build(dir)

	// Baseline: util.go outranks main.go (more exported symbols + it is
	// imported by main.go), so its section renders first.
	baseline := rm.RenderRelevantQuery(nil, nil, 0)
	utilAt := strings.Index(baseline, "## pkg/util/util.go")
	mainAt := strings.Index(baseline, "## cmd/main.go")
	if utilAt < 0 || mainAt < 0 {
		t.Fatalf("baseline render missing file sections:\n%s", baseline)
	}
	if utilAt > mainAt {
		t.Fatalf("fixture assumption broken: util.go should outrank main.go:\n%s", baseline)
	}

	// A strong task-query score on main.go must flip the ordering.
	scored := rm.RenderRelevantQuery(nil, map[string]float64{"cmd/main.go": 0.9}, 0)
	utilAt = strings.Index(scored, "## pkg/util/util.go")
	mainAt = strings.Index(scored, "## cmd/main.go")
	if utilAt < 0 || mainAt < 0 {
		t.Fatalf("scored render missing file sections:\n%s", scored)
	}
	if mainAt > utilAt {
		t.Errorf("query-scored file did not lead the map:\n%s", scored)
	}

	// The boost is per-call: the next unscored render sees original ranks.
	after := rm.RenderRelevantQuery(nil, nil, 0)
	if after != baseline {
		t.Errorf("query boost leaked into subsequent renders:\n--- baseline ---\n%s\n--- after ---\n%s", baseline, after)
	}
}

func TestInvalidateRefreshesChangedFile(t *testing.T) {
	dir := setupTestRepo(t)
	rm, _ := Build(dir)

	if out := rm.Render(0); strings.Contains(out, "ZzNewExported") {
		t.Fatalf("fixture already contains ZzNewExported:\n%s", out)
	}

	// Append a new exported symbol on disk, then invalidate.
	path := filepath.Join(dir, "pkg", "util", "util.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	src = append(src, []byte("\nfunc ZzNewExported() {}\n")...)
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	rm.Invalidate("pkg/util/util.go")

	out := rm.Render(0)
	if !strings.Contains(out, "ZzNewExported") {
		t.Errorf("Invalidate did not pick up new symbol:\n%s", out)
	}
	if !strings.Contains(out, "Hello") {
		t.Errorf("Invalidate lost pre-existing symbol Hello:\n%s", out)
	}
}

func TestInvalidateDropsDeletedFile(t *testing.T) {
	dir := setupTestRepo(t)
	rm, _ := Build(dir)

	if out := rm.Render(0); !strings.Contains(out, "## cmd/main.go") {
		t.Fatalf("fixture render missing cmd/main.go:\n%s", out)
	}

	if err := os.Remove(filepath.Join(dir, "cmd", "main.go")); err != nil {
		t.Fatal(err)
	}
	rm.Invalidate("cmd/main.go")

	out := rm.Render(0)
	if strings.Contains(out, "## cmd/main.go") {
		t.Errorf("deleted file still rendered:\n%s", out)
	}
	if strings.Contains(out, "NewConfig") {
		t.Errorf("deleted file's symbols still rendered:\n%s", out)
	}
}

func TestInvalidateAddsNewFile(t *testing.T) {
	dir := setupTestRepo(t)
	rm, _ := Build(dir)

	path := filepath.Join(dir, "pkg", "util", "extra.go")
	if err := os.WriteFile(path, []byte("package util\n\nfunc ZzExtraFunc() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rm.Invalidate("pkg/util/extra.go")

	if out := rm.Render(0); !strings.Contains(out, "ZzExtraFunc") {
		t.Errorf("Invalidate did not add new file's symbol:\n%s", out)
	}
}

func TestInvalidateKeepsNodeWhenUnparseable(t *testing.T) {
	dir := setupTestRepo(t)
	rm, _ := Build(dir)

	// A mid-edit syntactically broken file must not evict the last good view.
	path := filepath.Join(dir, "pkg", "util", "util.go")
	if err := os.WriteFile(path, []byte("package util\n\nfunc Broken( {\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rm.Invalidate("pkg/util/util.go")

	if out := rm.Render(0); !strings.Contains(out, "Hello") {
		t.Errorf("unparseable rewrite evicted previous symbols:\n%s", out)
	}
}

func TestInvalidateNonGoFileIsNoOp(t *testing.T) {
	dir := setupTestRepo(t)
	rm, _ := Build(dir)
	before := rm.Render(0)

	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# readme\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rm.Invalidate("README.md")

	if after := rm.Render(0); after != before {
		t.Errorf("non-Go Invalidate changed the render:\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
}

// TestInvalidateConcurrentWithRender exercises the mu-serialized entry
// points together; run under -race this proves the rank mutation inside
// RenderRelevantQuery no longer races Invalidate on the shared map.
func TestInvalidateConcurrentWithRender(t *testing.T) {
	dir := setupTestRepo(t)
	rm, _ := Build(dir)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 25; i++ {
			rm.Invalidate("pkg/util/util.go")
		}
	}()
	for i := 0; i < 25; i++ {
		_ = rm.RenderRelevant([]string{"pkg/util/util.go"}, 0)
		_ = rm.RenderRelevantQuery(nil, map[string]float64{"cmd/main.go": 0.5}, 0)
		_ = rm.Render(0)
	}
	<-done
}
