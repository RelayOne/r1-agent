package symindex

import (
	"os"
	"path/filepath"
	"testing"
)

func setupGoFiles(t *testing.T) string {
	dir := t.TempDir()

	main := `package main

import "fmt"

var Version = "1.0"

type Config struct {
	Name string
}

type Handler interface {
	Handle()
}

func main() {
	fmt.Println("hello")
}

func helper() {
	// unexported
}

func (c *Config) String() string {
	return c.Name
}
`
	os.WriteFile(filepath.Join(dir, "main.go"), []byte(main), 0644)

	utils := `package main

const MaxRetries = 3

func Compute(x int) int {
	return x * 2
}
`
	os.WriteFile(filepath.Join(dir, "utils.go"), []byte(utils), 0644)
	return dir
}

func TestBuild(t *testing.T) {
	dir := setupGoFiles(t)
	idx, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	if idx.Count() == 0 {
		t.Error("should find symbols")
	}
}

func TestLookup(t *testing.T) {
	dir := setupGoFiles(t)
	idx, _ := Build(dir)

	syms := idx.Lookup("Config")
	if len(syms) == 0 {
		t.Fatal("should find Config")
	}
	if syms[0].Kind != KindType {
		t.Errorf("expected type, got %s", syms[0].Kind)
	}
	if !syms[0].Exported {
		t.Error("Config should be exported")
	}
}

func TestLookupFunction(t *testing.T) {
	dir := setupGoFiles(t)
	idx, _ := Build(dir)

	syms := idx.Lookup("Compute")
	if len(syms) == 0 {
		t.Fatal("should find Compute")
	}
	if syms[0].Kind != KindFunction {
		t.Errorf("expected function, got %s", syms[0].Kind)
	}
}

func TestLookupMethod(t *testing.T) {
	dir := setupGoFiles(t)
	idx, _ := Build(dir)

	syms := idx.Lookup("String")
	if len(syms) == 0 {
		t.Fatal("should find String method")
	}
	if syms[0].Kind != KindMethod {
		t.Errorf("expected method, got %s", syms[0].Kind)
	}
	if syms[0].Parent != "Config" {
		t.Errorf("expected Config parent, got %s", syms[0].Parent)
	}
}

func TestSearch(t *testing.T) {
	dir := setupGoFiles(t)
	idx, _ := Build(dir)

	results := idx.Search("Con")
	found := false
	for _, s := range results {
		if s.Name == "Config" {
			found = true
		}
	}
	if !found {
		t.Error("prefix search should find Config")
	}
}

func TestInFile(t *testing.T) {
	dir := setupGoFiles(t)
	idx, _ := Build(dir)

	syms := idx.InFile("utils.go")
	if len(syms) == 0 {
		t.Error("should find symbols in utils.go")
	}
}

func TestByKind(t *testing.T) {
	dir := setupGoFiles(t)
	idx, _ := Build(dir)

	types := idx.ByKind(KindType)
	if len(types) == 0 {
		t.Error("should find types")
	}

	funcs := idx.ByKind(KindFunction)
	if len(funcs) == 0 {
		t.Error("should find functions")
	}
}

func TestExported(t *testing.T) {
	dir := setupGoFiles(t)
	idx, _ := Build(dir)

	exported := idx.Exported()
	for _, s := range exported {
		if s.Name == "helper" {
			t.Error("helper should not be in exported list")
		}
	}
}

func TestInterface(t *testing.T) {
	dir := setupGoFiles(t)
	idx, _ := Build(dir)

	syms := idx.Lookup("Handler")
	if len(syms) == 0 {
		t.Fatal("should find Handler interface")
	}
	if syms[0].Kind != KindInterface {
		t.Errorf("expected interface, got %s", syms[0].Kind)
	}
}

func TestFiles(t *testing.T) {
	dir := setupGoFiles(t)
	idx, _ := Build(dir)

	files := idx.Files()
	if len(files) != 2 {
		t.Errorf("expected 2 files, got %d", len(files))
	}
}

func TestRepoMap(t *testing.T) {
	dir := setupGoFiles(t)
	idx, _ := Build(dir)

	rm := idx.RepoMap()
	if rm == "" {
		t.Error("repo map should not be empty")
	}
	if !containsStr(rm, "Config") {
		t.Error("repo map should contain Config")
	}
	if !containsStr(rm, "Compute") {
		t.Error("repo map should contain Compute")
	}
}

func TestStats(t *testing.T) {
	dir := setupGoFiles(t)
	idx, _ := Build(dir)

	stats := idx.Stats()
	if stats["total_symbols"] == 0 {
		t.Error("should have symbols")
	}
	if stats["files"] != 2 {
		t.Errorf("expected 2 files, got %d", stats["files"])
	}
}

func TestBuildFromFiles(t *testing.T) {
	dir := setupGoFiles(t)
	idx, err := BuildFromFiles(dir, []string{"main.go"})
	if err != nil {
		t.Fatal(err)
	}
	files := idx.Files()
	if len(files) != 1 {
		t.Errorf("expected 1 file, got %d", len(files))
	}
}

func TestPythonSymbols(t *testing.T) {
	dir := t.TempDir()
	py := `class MyClass:
    def __init__(self):
        pass

    def method(self):
        pass

def standalone():
    pass

_private = 1
public_var = 2
`
	os.WriteFile(filepath.Join(dir, "app.py"), []byte(py), 0644)

	idx, _ := Build(dir)
	if idx.Count() == 0 {
		t.Error("should find Python symbols")
	}

	classes := idx.ByKind(KindClass)
	if len(classes) == 0 {
		t.Error("should find class")
	}
}

func TestTypeScriptSymbols(t *testing.T) {
	dir := t.TempDir()
	ts := `export interface Config {
  name: string;
}

export class App {
  async start() {}
}

export function createApp(): App {
  return new App();
}

const VERSION = "1.0";
export type ID = string;
`
	os.WriteFile(filepath.Join(dir, "app.ts"), []byte(ts), 0644)

	idx, _ := Build(dir)
	if idx.Count() == 0 {
		t.Error("should find TS symbols")
	}

	ifaces := idx.ByKind(KindInterface)
	if len(ifaces) == 0 {
		t.Error("should find interface")
	}
}

func TestSkipHiddenDirs(t *testing.T) {
	dir := t.TempDir()
	hidden := filepath.Join(dir, ".hidden")
	os.MkdirAll(hidden, 0755)
	os.WriteFile(filepath.Join(hidden, "secret.go"), []byte("package secret\nfunc Secret() {}"), 0644)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc Main() {}"), 0644)

	idx, _ := Build(dir)
	files := idx.Files()
	for _, f := range files {
		if f == ".hidden/secret.go" {
			t.Error("should skip hidden directories")
		}
	}
}

func TestEmptyIndex(t *testing.T) {
	dir := t.TempDir()
	idx, _ := Build(dir)
	if idx.Count() != 0 {
		t.Error("empty dir should have no symbols")
	}
	if idx.RepoMap() != "" {
		t.Error("empty repo map should be empty string")
	}
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && searchStr(s, substr)
}

func searchStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
