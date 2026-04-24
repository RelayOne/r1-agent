// Package testgen generates test scaffolds from function signatures.
// Inspired by Aider's test-writing patterns and SWE-agent's test-first approach:
//
// Auto-generating test stubs:
// - Speeds up the test-write phase (agent fills in assertions)
// - Ensures consistent test naming conventions
// - Creates table-driven tests for Go (the idiomatic pattern)
// - Covers basic cases: zero values, edge cases, error paths
//
// The generated tests are scaffolds, not complete tests — they need
// agent or human review to add meaningful assertions.
package testgen

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strings"
)

// FuncSig is a parsed function signature.
type FuncSig struct {
	Name       string   `json:"name"`
	Receiver   string   `json:"receiver,omitempty"`
	Params     []Param  `json:"params"`
	Returns    []string `json:"returns"`
	IsExported bool     `json:"is_exported"`
}

// Param is a function parameter.
type Param struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// GoTest generates a Go test function for a signature.
func GoTest(sig FuncSig) string {
	var b strings.Builder
	testName := "Test" + sig.Name

	fmt.Fprintf(&b, "func %s(t *testing.T) {\n", testName)

	// Table-driven if there are params
	if len(sig.Params) > 0 {
		b.WriteString("\ttests := []struct {\n")
		b.WriteString("\t\tname string\n")
		for _, p := range sig.Params {
			fmt.Fprintf(&b, "\t\t%s %s\n", p.Name, p.Type)
		}
		if hasErrorReturn(sig.Returns) {
			b.WriteString("\t\twantErr bool\n")
		}
		b.WriteString("\t}{\n")

		// Generate test cases
		for _, tc := range generateCases(sig) {
			fmt.Fprintf(&b, "\t\t{name: %q", tc.name)
			for _, p := range sig.Params {
				fmt.Fprintf(&b, ", %s: %s", p.Name, tc.values[p.Name])
			}
			if hasErrorReturn(sig.Returns) {
				fmt.Fprintf(&b, ", wantErr: %v", tc.wantErr)
			}
			b.WriteString("},\n")
		}

		b.WriteString("\t}\n\n")

		// Range over cases
		b.WriteString("\tfor _, tt := range tests {\n")
		b.WriteString("\t\tt.Run(tt.name, func(t *testing.T) {\n")

		// Call the function
		b.WriteString("\t\t\t")
		if len(sig.Returns) > 0 {
			b.WriteString(returnVars(sig.Returns) + " := ")
		}
		if sig.Receiver != "" {
			fmt.Fprintf(&b, "(%s{}).%s(", sig.Receiver, sig.Name)
		} else {
			fmt.Fprintf(&b, "%s(", sig.Name)
		}
		for i, p := range sig.Params {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "tt.%s", p.Name)
		}
		b.WriteString(")\n")

		// Error check
		if hasErrorReturn(sig.Returns) {
			b.WriteString("\t\t\tif (err != nil) != tt.wantErr {\n")
			b.WriteString("\t\t\t\tt.Errorf(\"error = %v, wantErr %v\", err, tt.wantErr)\n")
			b.WriteString("\t\t\t}\n")
		}

		b.WriteString("\t\t\t// TODO: add assertions\n")
		b.WriteString("\t\t})\n")
		b.WriteString("\t}\n")
	} else {
		// Simple test for no-arg functions
		b.WriteString("\t")
		if len(sig.Returns) > 0 {
			b.WriteString(returnVars(sig.Returns) + " := ")
		}
		if sig.Receiver != "" {
			fmt.Fprintf(&b, "(%s{}).%s()\n", sig.Receiver, sig.Name)
		} else {
			fmt.Fprintf(&b, "%s()\n", sig.Name)
		}
		if hasErrorReturn(sig.Returns) {
			b.WriteString("\tif err != nil {\n")
			b.WriteString("\t\tt.Errorf(\"unexpected error: %v\", err)\n")
			b.WriteString("\t}\n")
		}
		b.WriteString("\t// TODO: add assertions\n")
	}

	b.WriteString("}\n")
	return b.String()
}

// ParseGoFunc extracts function signatures from Go source code.
// Uses go/parser AST for accurate extraction, falling back to regex
// if AST parsing fails.
func ParseGoFunc(source string) []FuncSig {
	if sigs := parseGoFuncAST(source); sigs != nil {
		return sigs
	}
	return parseGoFuncRegex(source)
}

// parseGoFuncAST uses go/parser for accurate function signature extraction.
func parseGoFuncAST(source string) []FuncSig {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "source.go", source, parser.ParseComments)
	if err != nil {
		return nil
	}

	var sigs []FuncSig
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok {
			sig := FuncSig{
				Name: fn.Name.Name,
			}
			if fn.Recv != nil && len(fn.Recv.List) > 0 {
				sig.Receiver = exprToTypeName(fn.Recv.List[0].Type)
			}
			if fn.Type.Params != nil {
				sig.Params = fieldListToParams(fn.Type.Params)
			}
			if fn.Type.Results != nil {
				sig.Returns = fieldListToReturns(fn.Type.Results)
			}
			sig.IsExported = sig.Name[0] >= 'A' && sig.Name[0] <= 'Z'
			sigs = append(sigs, sig)
		}
	}
	return sigs
}

// exprToTypeName extracts the base type name from an AST expression,
// stripping pointer indirection.
func exprToTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return exprToTypeName(t.X)
	case *ast.SelectorExpr:
		return exprToString(t.X) + "." + t.Sel.Name
	default:
		return exprToString(expr)
	}
}

// exprToString renders an AST expression as Go source text.
func exprToString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + exprToString(t.X)
	case *ast.SelectorExpr:
		return exprToString(t.X) + "." + t.Sel.Name
	case *ast.ArrayType:
		if t.Len == nil {
			return "[]" + exprToString(t.Elt)
		}
		return "[" + exprToString(t.Len) + "]" + exprToString(t.Elt)
	case *ast.MapType:
		return "map[" + exprToString(t.Key) + "]" + exprToString(t.Value)
	case *ast.InterfaceType:
		return "interface{}"
	case *ast.FuncType:
		return "func(...)"
	case *ast.ChanType:
		return "chan " + exprToString(t.Value)
	case *ast.Ellipsis:
		return "..." + exprToString(t.Elt)
	case *ast.BasicLit:
		return t.Value
	case *ast.IndexExpr:
		return exprToString(t.X) + "[" + exprToString(t.Index) + "]"
	default:
		return "any"
	}
}

// fieldListToParams converts AST field list to Param slice.
func fieldListToParams(fl *ast.FieldList) []Param {
	var params []Param
	argIdx := 0
	for _, field := range fl.List {
		typStr := exprToString(field.Type)
		if len(field.Names) == 0 {
			params = append(params, Param{
				Name: fmt.Sprintf("arg%d", argIdx),
				Type: typStr,
			})
			argIdx++
		} else {
			for _, name := range field.Names {
				params = append(params, Param{
					Name: name.Name,
					Type: typStr,
				})
				argIdx++
			}
		}
	}
	return params
}

// fieldListToReturns converts AST field list to return type strings.
func fieldListToReturns(fl *ast.FieldList) []string {
	var returns []string
	for _, field := range fl.List {
		typStr := exprToString(field.Type)
		count := len(field.Names)
		if count == 0 {
			count = 1
		}
		for range count {
			returns = append(returns, typStr)
		}
	}
	return returns
}

// parseGoFuncRegex is the regex fallback for non-parseable source.
func parseGoFuncRegex(source string) []FuncSig {
	matches := goFuncRegex.FindAllStringSubmatch(source, -1)
	sigs := make([]FuncSig, 0, len(matches))

	for _, m := range matches {
		sig := FuncSig{
			Name: m[2],
		}
		if m[1] != "" {
			sig.Receiver = strings.TrimSpace(m[1])
			parts := strings.Fields(sig.Receiver)
			if len(parts) >= 2 {
				sig.Receiver = strings.TrimPrefix(parts[1], "*")
			}
		}
		if m[3] != "" {
			sig.Params = parseParams(m[3])
		}
		if m[4] != "" {
			sig.Returns = parseReturns(m[4])
		}
		sig.IsExported = sig.Name[0] >= 'A' && sig.Name[0] <= 'Z'
		sigs = append(sigs, sig)
	}
	return sigs
}

// GenerateFile creates a complete test file for all exported functions.
func GenerateFile(pkg, source string) string {
	sigs := ParseGoFunc(source)

	var b strings.Builder
	fmt.Fprintf(&b, "package %s\n\n", pkg)
	b.WriteString("import (\n\t\"testing\"\n)\n\n")

	for _, sig := range sigs {
		if !sig.IsExported {
			continue
		}
		b.WriteString(GoTest(sig))
		b.WriteString("\n")
	}

	return b.String()
}

// --- internals ---

var goFuncRegex = regexp.MustCompile(`func\s*(?:\(([^)]*)\))?\s*(\w+)\s*\(([^)]*)\)\s*(?:\(([^)]*)\)|(\w+))?\s*\{`)

type testCase struct {
	name    string
	values  map[string]string
	wantErr bool
}

func generateCases(sig FuncSig) []testCase {
	var cases []testCase

	// Zero value case
	zeroCase := testCase{name: "zero values", values: make(map[string]string)}
	for _, p := range sig.Params {
		zeroCase.values[p.Name] = zeroValue(p.Type)
	}
	cases = append(cases, zeroCase)

	// Basic case with typical values
	basicCase := testCase{name: "basic", values: make(map[string]string)}
	for _, p := range sig.Params {
		basicCase.values[p.Name] = typicalValue(p.Type)
	}
	cases = append(cases, basicCase)

	// Edge case for strings: empty
	for _, p := range sig.Params {
		if p.Type == "string" {
			edgeCase := testCase{name: "empty " + p.Name, values: make(map[string]string), wantErr: true}
			for _, p2 := range sig.Params {
				if p2.Name == p.Name {
					edgeCase.values[p2.Name] = `""`
				} else {
					edgeCase.values[p2.Name] = typicalValue(p2.Type)
				}
			}
			cases = append(cases, edgeCase)
			break
		}
	}

	return cases
}

func zeroValue(typ string) string {
	switch typ {
	case "string":
		return `""`
	case "int", "int64", "int32", "float64", "float32":
		return "0"
	case "bool":
		return "false"
	case "error":
		return "nil"
	default:
		if strings.HasPrefix(typ, "[]") {
			return "nil"
		}
		if strings.HasPrefix(typ, "*") {
			return "nil"
		}
		if strings.HasPrefix(typ, "map[") {
			return "nil"
		}
		return typ + "{}"
	}
}

func typicalValue(typ string) string {
	switch typ {
	case "string":
		return `"test"`
	case "int", "int64", "int32":
		return "42"
	case "float64", "float32":
		return "3.14"
	case "bool":
		return "true"
	default:
		return zeroValue(typ)
	}
}

func hasErrorReturn(returns []string) bool {
	for _, r := range returns {
		if r == "error" {
			return true
		}
	}
	return false
}

func returnVars(returns []string) string {
	if len(returns) == 0 {
		return ""
	}
	vars := make([]string, len(returns))
	usedNames := make(map[string]int)
	for i, r := range returns {
		if r == "error" {
			vars[i] = "err"
		} else {
			name := "got"
			if usedNames[name] > 0 {
				name = fmt.Sprintf("got%d", usedNames[name]+1)
			}
			usedNames[name]++
			vars[i] = name
		}
	}
	return strings.Join(vars, ", ")
}

func parseParams(s string) []Param {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}

	var params []Param
	parts := strings.Split(s, ",")

	for _, part := range parts {
		part = strings.TrimSpace(part)
		fields := strings.Fields(part)
		if len(fields) >= 2 {
			params = append(params, Param{
				Name: fields[0],
				Type: strings.Join(fields[1:], " "),
			})
		} else if len(fields) == 1 {
			params = append(params, Param{
				Name: fmt.Sprintf("arg%d", len(params)),
				Type: fields[0],
			})
		}
	}
	return params
}

func parseReturns(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}

	var returns []string
	parts := strings.Split(s, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		fields := strings.Fields(part)
		if len(fields) > 0 {
			returns = append(returns, fields[len(fields)-1])
		}
	}
	return returns
}
