package goast

import (
	"os"
	"path/filepath"
	"testing"
)

const testSource = `package example

import (
	"fmt"
	"strings"
)

// Config holds application configuration.
type Config struct {
	Name    string
	MaxSize int
}

// Handler is the interface for request handling.
type Handler interface {
	Handle(req string) error
	Close()
}

// StringAlias is a named type alias.
type StringAlias = string

var GlobalVar = "hello"

const MaxRetries = 3

// NewConfig creates a new Config.
func NewConfig(name string) *Config {
	return &Config{Name: name}
}

func (c *Config) String() string {
	return fmt.Sprintf("Config{%s}", c.Name)
}

func (c *Config) Handle(req string) error {
	fmt.Println(req)
	return nil
}

func (c *Config) Close() {}

func helper() {
	s := strings.Join([]string{"a", "b"}, ",")
	fmt.Println(s)
}

func init() {
	fmt.Println("init")
}
`

func TestAnalyzeSource(t *testing.T) {
	fa, err := AnalyzeSource([]byte(testSource), "example.go")
	if err != nil {
		t.Fatal(err)
	}

	if fa.Package != "example" {
		t.Errorf("expected package example, got %s", fa.Package)
	}

	if len(fa.Imports) != 2 {
		t.Errorf("expected 2 imports, got %d", len(fa.Imports))
	}
}

func TestExtractSymbols(t *testing.T) {
	fa, err := AnalyzeSource([]byte(testSource), "example.go")
	if err != nil {
		t.Fatal(err)
	}

	byName := make(map[string]Symbol)
	for _, s := range fa.Symbols {
		byName[s.Name] = s
	}

	// Struct
	if s, ok := byName["Config"]; !ok {
		t.Error("should find Config")
	} else {
		if s.Kind != KindStruct {
			t.Errorf("Config should be struct, got %s", s.Kind)
		}
		if !s.Exported {
			t.Error("Config should be exported")
		}
	}

	// Interface
	if s, ok := byName["Handler"]; !ok {
		t.Error("should find Handler")
	} else if s.Kind != KindInterface {
		t.Errorf("Handler should be interface, got %s", s.Kind)
	}

	// Function
	if s, ok := byName["NewConfig"]; !ok {
		t.Error("should find NewConfig")
	} else {
		if s.Kind != KindFunction {
			t.Errorf("NewConfig should be function, got %s", s.Kind)
		}
		if s.Signature == "" {
			t.Error("NewConfig should have signature")
		}
		if s.Doc == "" {
			t.Error("NewConfig should have doc comment")
		}
	}

	// Method
	if s, ok := byName["String"]; !ok {
		t.Error("should find String method")
	} else {
		if s.Kind != KindMethod {
			t.Errorf("String should be method, got %s", s.Kind)
		}
		if s.Receiver != "Config" {
			t.Errorf("String receiver should be Config, got %s", s.Receiver)
		}
	}

	// helper (unexported)
	if s, ok := byName["helper"]; !ok {
		t.Error("should find helper")
	} else if s.Exported {
		t.Error("helper should not be exported")
	}

	// Variable
	if s, ok := byName["GlobalVar"]; !ok {
		t.Error("should find GlobalVar")
	} else if s.Kind != KindVariable {
		t.Errorf("GlobalVar should be variable, got %s", s.Kind)
	}

	// Constant
	if s, ok := byName["MaxRetries"]; !ok {
		t.Error("should find MaxRetries")
	} else if s.Kind != KindConstant {
		t.Errorf("MaxRetries should be constant, got %s", s.Kind)
	}
}

func TestExtractFields(t *testing.T) {
	fa, _ := AnalyzeSource([]byte(testSource), "example.go")

	var fields []Symbol
	for _, s := range fa.Symbols {
		if s.Kind == KindField {
			fields = append(fields, s)
		}
	}

	if len(fields) != 2 {
		t.Fatalf("expected 2 fields (Name, MaxSize), got %d", len(fields))
	}

	fieldNames := map[string]bool{}
	for _, f := range fields {
		fieldNames[f.Name] = true
		if f.Receiver != "Config" {
			t.Errorf("field %s should have receiver Config, got %s", f.Name, f.Receiver)
		}
	}
	if !fieldNames["Name"] || !fieldNames["MaxSize"] {
		t.Error("should find fields Name and MaxSize")
	}
}

func TestExtractCalls(t *testing.T) {
	fa, _ := AnalyzeSource([]byte(testSource), "example.go")

	if len(fa.Calls) == 0 {
		t.Fatal("should extract call edges")
	}

	// NewConfig calls fmt.Sprintf (indirectly via Config.String)
	// helper calls strings.Join and fmt.Println
	callerCalls := make(map[string][]string)
	for _, c := range fa.Calls {
		callerCalls[c.Caller] = append(callerCalls[c.Caller], c.Callee)
	}

	// helper should call Join and Println
	helperCalls := callerCalls["helper"]
	hasJoin, hasPrintln := false, false
	for _, c := range helperCalls {
		if c == "Join" {
			hasJoin = true
		}
		if c == "Println" {
			hasPrintln = true
		}
	}
	if !hasJoin {
		t.Error("helper should call strings.Join")
	}
	if !hasPrintln {
		t.Error("helper should call fmt.Println")
	}

	// Config.String should call Sprintf
	stringCalls := callerCalls["Config.String"]
	hasSprintf := false
	for _, c := range stringCalls {
		if c == "Sprintf" {
			hasSprintf = true
		}
	}
	if !hasSprintf {
		t.Error("Config.String should call fmt.Sprintf")
	}
}

func TestExtractInterfaces(t *testing.T) {
	fa, _ := AnalyzeSource([]byte(testSource), "example.go")

	if len(fa.Interfaces) != 1 {
		t.Fatalf("expected 1 interface, got %d", len(fa.Interfaces))
	}

	iface := fa.Interfaces[0]
	if iface.Name != "Handler" {
		t.Errorf("expected Handler, got %s", iface.Name)
	}
	if len(iface.Methods) != 2 {
		t.Errorf("expected 2 methods (Handle, Close), got %d", len(iface.Methods))
	}
}

func TestInterfaceSatisfaction(t *testing.T) {
	a := &Analysis{}
	fa, _ := AnalyzeSource([]byte(testSource), "example.go")
	a.Files = append(a.Files, fa)
	a.AllSymbols = append(a.AllSymbols, fa.Symbols...)
	a.AllCalls = append(a.AllCalls, fa.Calls...)

	sat := a.InterfaceSatisfaction()
	types, ok := sat["Handler"]
	if !ok {
		t.Fatal("Config should satisfy Handler interface")
	}

	found := false
	for _, typ := range types {
		if typ == "Config" {
			found = true
		}
	}
	if !found {
		t.Error("Config should be listed as implementing Handler")
	}
}

func TestCallGraph(t *testing.T) {
	a := &Analysis{}
	fa, _ := AnalyzeSource([]byte(testSource), "example.go")
	a.Files = append(a.Files, fa)
	a.AllSymbols = append(a.AllSymbols, fa.Symbols...)
	a.AllCalls = append(a.AllCalls, fa.Calls...)

	// CalleesOf
	callees := a.CalleesOf("helper")
	if len(callees) == 0 {
		t.Error("helper should have callees")
	}

	// CallersOf
	callers := a.CallersOf("Println")
	if len(callers) == 0 {
		t.Error("Println should have callers")
	}
}

func TestReachability(t *testing.T) {
	a := &Analysis{}
	fa, _ := AnalyzeSource([]byte(testSource), "example.go")
	a.Files = append(a.Files, fa)
	a.AllSymbols = append(a.AllSymbols, fa.Symbols...)
	a.AllCalls = append(a.AllCalls, fa.Calls...)

	reachable := a.Reachable([]string{"helper"})
	if !reachable["helper"] {
		t.Error("helper should be reachable from itself")
	}
	if !reachable["Println"] {
		t.Error("Println should be reachable from helper")
	}
	if !reachable["Join"] {
		t.Error("Join should be reachable from helper")
	}
}

func TestDeadSymbols(t *testing.T) {
	// Source with an exported symbol that nothing calls
	src := `package dead

func init() {
	helper()
}

func helper() {}

func UsedByInit() {}

func NeverCalled() {}
`
	a := &Analysis{}
	fa, _ := AnalyzeSource([]byte(src), "dead.go")
	a.Files = append(a.Files, fa)
	a.AllSymbols = append(a.AllSymbols, fa.Symbols...)
	a.AllCalls = append(a.AllCalls, fa.Calls...)

	dead := a.DeadSymbols()
	names := map[string]bool{}
	for _, d := range dead {
		names[d.Name] = true
	}

	if !names["NeverCalled"] {
		t.Error("NeverCalled should be detected as dead")
	}
	// UsedByInit is not called by init — it should also be dead
	// (init only calls helper, not UsedByInit)
	if !names["UsedByInit"] {
		t.Error("UsedByInit should be detected as dead (not called by init)")
	}
}

func TestAnalyzeDir(t *testing.T) {
	dir := t.TempDir()

	os.WriteFile(filepath.Join(dir, "main.go"), []byte(`package main

import "fmt"

func main() {
	fmt.Println(NewApp())
}

func NewApp() string {
	return "app"
}
`), 0644)

	os.WriteFile(filepath.Join(dir, "util.go"), []byte(`package main

func Helper(x int) int {
	return x * 2
}
`), 0644)

	// Test file should be skipped
	os.WriteFile(filepath.Join(dir, "main_test.go"), []byte(`package main
import "testing"
func TestMain(t *testing.T) {}
`), 0644)

	a, err := AnalyzeDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(a.Files) != 2 {
		t.Errorf("expected 2 files (test file excluded), got %d", len(a.Files))
	}

	if len(a.AllSymbols) == 0 {
		t.Error("should have symbols")
	}

	if len(a.AllCalls) == 0 {
		t.Error("should have call edges")
	}
}

func TestAnalyzeFiles(t *testing.T) {
	dir := t.TempDir()

	os.WriteFile(filepath.Join(dir, "a.go"), []byte(`package a
func A() {}
`), 0644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte(`package a
func B() {}
`), 0644)

	a, err := AnalyzeFiles(dir, []string{"a.go"})
	if err != nil {
		t.Fatal(err)
	}

	if len(a.Files) != 1 {
		t.Errorf("expected 1 file, got %d", len(a.Files))
	}
}

func TestFuncSignature(t *testing.T) {
	src := `package sig

func Simple() {}

func WithParams(a int, b string) {}

func WithReturn(x int) error { return nil }

func MultiReturn(x int) (string, error) { return "", nil }

func Variadic(args ...string) {}
`
	fa, _ := AnalyzeSource([]byte(src), "sig.go")

	sigs := make(map[string]string)
	for _, s := range fa.Symbols {
		if s.Kind == KindFunction {
			sigs[s.Name] = s.Signature
		}
	}

	if sigs["Simple"] != "Simple()" {
		t.Errorf("Simple sig: %s", sigs["Simple"])
	}
	if sigs["WithParams"] != "WithParams(a int, b string)" {
		t.Errorf("WithParams sig: %s", sigs["WithParams"])
	}
	if sigs["WithReturn"] != "WithReturn(x int) error" {
		t.Errorf("WithReturn sig: %s", sigs["WithReturn"])
	}
	if sigs["MultiReturn"] != "MultiReturn(x int) (string, error)" {
		t.Errorf("MultiReturn sig: %s", sigs["MultiReturn"])
	}
	if sigs["Variadic"] != "Variadic(args ...string)" {
		t.Errorf("Variadic sig: %s", sigs["Variadic"])
	}
}

func TestDeferredCalls(t *testing.T) {
	src := `package d

import "os"

func Cleanup() {
	f, _ := os.Open("file")
	defer f.Close()
}
`
	fa, _ := AnalyzeSource([]byte(src), "d.go")

	foundDefer := false
	for _, c := range fa.Calls {
		if c.Callee == "Close" && c.IsDeferred {
			foundDefer = true
		}
	}
	if !foundDefer {
		t.Error("should detect deferred Close call")
	}
}

func TestSkipHiddenAndVendor(t *testing.T) {
	dir := t.TempDir()

	os.MkdirAll(filepath.Join(dir, ".git"), 0755)
	os.WriteFile(filepath.Join(dir, ".git", "hidden.go"), []byte("package hidden\nfunc H() {}"), 0644)

	os.MkdirAll(filepath.Join(dir, "vendor", "lib"), 0755)
	os.WriteFile(filepath.Join(dir, "vendor", "lib", "lib.go"), []byte("package lib\nfunc L() {}"), 0644)

	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main() {}"), 0644)

	a, _ := AnalyzeDir(dir)
	if len(a.Files) != 1 {
		t.Errorf("should only have 1 file (main.go), got %d", len(a.Files))
	}
}

func TestSymbolsByName(t *testing.T) {
	a := &Analysis{}
	fa, _ := AnalyzeSource([]byte(testSource), "example.go")
	a.AllSymbols = fa.Symbols

	byName := a.SymbolsByName()
	if _, ok := byName["Config"]; !ok {
		t.Error("should find Config by name")
	}
	if _, ok := byName["NewConfig"]; !ok {
		t.Error("should find NewConfig by name")
	}
}

func TestMethodCallEdgeHasCalleePkg(t *testing.T) {
	src := `package m

import "fmt"

func F() {
	fmt.Println("hello")
}
`
	fa, _ := AnalyzeSource([]byte(src), "m.go")

	for _, c := range fa.Calls {
		if c.Callee == "Println" {
			if c.CalleePkg != "fmt" {
				t.Errorf("Println callee pkg should be fmt, got %s", c.CalleePkg)
			}
			if !c.IsMethod {
				t.Error("pkg.Func should be marked as method call")
			}
			return
		}
	}
	t.Error("should find Println call")
}
