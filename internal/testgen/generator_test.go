package testgen

import (
	"strings"
	"testing"
)

func TestParseGoFunc(t *testing.T) {
	source := `package main

func Hello(name string) string {
	return "Hello " + name
}

func Add(a int, b int) int {
	return a + b
}

func (s *Server) Start(port int) error {
	return nil
}

func internal() {
}
`

	sigs := ParseGoFunc(source)
	if len(sigs) != 4 {
		t.Fatalf("expected 4 functions, got %d", len(sigs))
	}

	// Hello
	if sigs[0].Name != "Hello" || len(sigs[0].Params) != 1 {
		t.Errorf("Hello: %+v", sigs[0])
	}
	if !sigs[0].IsExported {
		t.Error("Hello should be exported")
	}

	// Add
	if sigs[1].Name != "Add" || len(sigs[1].Params) != 2 {
		t.Errorf("Add: %+v", sigs[1])
	}

	// Server.Start (method)
	if sigs[2].Name != "Start" || sigs[2].Receiver == "" {
		t.Errorf("Start: %+v", sigs[2])
	}

	// internal
	if sigs[3].IsExported {
		t.Error("internal should not be exported")
	}
}

func TestGoTestSimple(t *testing.T) {
	sig := FuncSig{
		Name:    "Add",
		Params:  []Param{{Name: "a", Type: "int"}, {Name: "b", Type: "int"}},
		Returns: []string{"int"},
	}

	test := GoTest(sig)
	if !strings.Contains(test, "func TestAdd(t *testing.T)") {
		t.Error("should contain test function")
	}
	if !strings.Contains(test, "tests := []struct") {
		t.Error("should use table-driven tests")
	}
	if !strings.Contains(test, "zero values") {
		t.Error("should have zero values case")
	}
	if !strings.Contains(test, "basic") {
		t.Error("should have basic case")
	}
}

func TestGoTestWithError(t *testing.T) {
	sig := FuncSig{
		Name:    "Parse",
		Params:  []Param{{Name: "input", Type: "string"}},
		Returns: []string{"int", "error"},
	}

	test := GoTest(sig)
	if !strings.Contains(test, "wantErr") {
		t.Error("should include error handling")
	}
	if !strings.Contains(test, "err != nil") {
		t.Error("should check error")
	}
}

func TestGoTestNoParams(t *testing.T) {
	sig := FuncSig{
		Name:    "Init",
		Returns: []string{"error"},
	}

	test := GoTest(sig)
	if strings.Contains(test, "tests := []struct") {
		t.Error("no-param func should not use table-driven")
	}
	if !strings.Contains(test, "Init()") {
		t.Error("should call Init()")
	}
}

func TestGoTestMethod(t *testing.T) {
	sig := FuncSig{
		Name:     "Start",
		Receiver: "Server",
		Params:   []Param{{Name: "port", Type: "int"}},
		Returns:  []string{"error"},
	}

	test := GoTest(sig)
	if !strings.Contains(test, "Server{}") {
		t.Error("should construct receiver")
	}
}

func TestGenerateFile(t *testing.T) {
	source := `package mypackage

func Add(a int, b int) int {
	return a + b
}

func internal() {
}
`
	file := GenerateFile("mypackage", source)
	if !strings.Contains(file, "package mypackage") {
		t.Error("should have package declaration")
	}
	if !strings.Contains(file, "TestAdd") {
		t.Error("should generate TestAdd")
	}
	if strings.Contains(file, "Testinternal") {
		t.Error("should not generate test for unexported")
	}
}

func TestZeroValues(t *testing.T) {
	tests := []struct {
		typ, want string
	}{
		{"string", `""`},
		{"int", "0"},
		{"bool", "false"},
		{"[]string", "nil"},
		{"*Config", "nil"},
		{"map[string]int", "nil"},
		{"Config", "Config{}"},
	}
	for _, tt := range tests {
		got := zeroValue(tt.typ)
		if got != tt.want {
			t.Errorf("zeroValue(%s) = %s, want %s", tt.typ, got, tt.want)
		}
	}
}

func TestTypicalValues(t *testing.T) {
	if typicalValue("string") != `"test"` {
		t.Error("string typical should be test")
	}
	if typicalValue("int") != "42" {
		t.Error("int typical should be 42")
	}
}

func TestParseParams(t *testing.T) {
	params := parseParams("name string, age int")
	if len(params) != 2 {
		t.Fatalf("expected 2 params, got %d", len(params))
	}
	if params[0].Name != "name" || params[0].Type != "string" {
		t.Errorf("param 0: %+v", params[0])
	}
}

func TestParseReturns(t *testing.T) {
	returns := parseReturns("int, error")
	if len(returns) != 2 {
		t.Fatalf("expected 2 returns, got %d", len(returns))
	}
	if returns[0] != "int" || returns[1] != "error" {
		t.Errorf("returns: %v", returns)
	}
}

func TestParseGoFuncComplexSignatures(t *testing.T) {
	source := `package example

func Process(data map[string][]int, opts *Config) ([]Result, error) {
	return nil, nil
}

func (s *Server) Handle(ctx context.Context, req *http.Request) (*Response, error) {
	return nil, nil
}

func Variadic(parts ...string) string {
	return ""
}

func MultiReturn() (int, string, error) {
	return 0, "", nil
}

func NoArgs() {
}
`
	sigs := ParseGoFunc(source)
	if len(sigs) != 5 {
		t.Fatalf("expected 5 functions, got %d", len(sigs))
	}

	// Process: complex params and returns
	proc := sigs[0]
	if proc.Name != "Process" {
		t.Errorf("expected Process, got %s", proc.Name)
	}
	if len(proc.Params) != 2 {
		t.Errorf("Process params: expected 2, got %d", len(proc.Params))
	}
	if proc.Params[0].Type != "map[string][]int" {
		t.Errorf("Process param 0 type: expected map[string][]int, got %s", proc.Params[0].Type)
	}
	if proc.Params[1].Type != "*Config" {
		t.Errorf("Process param 1 type: expected *Config, got %s", proc.Params[1].Type)
	}
	if len(proc.Returns) != 2 || proc.Returns[0] != "[]Result" || proc.Returns[1] != "error" {
		t.Errorf("Process returns: %v", proc.Returns)
	}

	// Handle: method with receiver
	handle := sigs[1]
	if handle.Receiver != "Server" {
		t.Errorf("Handle receiver: expected Server, got %s", handle.Receiver)
	}

	// Variadic
	variadic := sigs[2]
	if len(variadic.Params) != 1 || variadic.Params[0].Type != "...string" {
		t.Errorf("Variadic params: %+v", variadic.Params)
	}

	// MultiReturn
	multi := sigs[3]
	if len(multi.Returns) != 3 {
		t.Errorf("MultiReturn: expected 3 returns, got %d", len(multi.Returns))
	}

	// NoArgs
	noArgs := sigs[4]
	if len(noArgs.Params) != 0 || len(noArgs.Returns) != 0 {
		t.Errorf("NoArgs should have no params/returns: %+v", noArgs)
	}
}

func TestEdgeCaseGeneration(t *testing.T) {
	sig := FuncSig{
		Name:   "Greet",
		Params: []Param{{Name: "name", Type: "string"}},
	}
	cases := generateCases(sig)
	hasEdge := false
	for _, c := range cases {
		if strings.Contains(c.name, "empty") {
			hasEdge = true
		}
	}
	if !hasEdge {
		t.Error("should generate edge case for string param")
	}
}
