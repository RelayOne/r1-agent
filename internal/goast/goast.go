// Package goast provides real Go AST-based code analysis.
//
// Unlike regex-based extraction which misses nested declarations, misparses
// string literals, and can't understand types or call relationships, this
// package uses go/parser and go/ast for accurate structural analysis:
//
//   - Full symbol extraction with typed signatures and receiver info
//   - Call graph construction from actual CallExpr/SelectorExpr nodes
//   - Interface method sets for satisfaction checking
//   - Import graph with package-level resolution
//   - Structural similarity comparison (AST diff, not word diff)
package goast

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SymbolKind classifies a code symbol.
type SymbolKind string

const (
	KindFunction  SymbolKind = "function"
	KindMethod    SymbolKind = "method"
	KindType      SymbolKind = "type"
	KindInterface SymbolKind = "interface"
	KindStruct    SymbolKind = "struct"
	KindVariable  SymbolKind = "variable"
	KindConstant  SymbolKind = "constant"
	KindField     SymbolKind = "field"
)

// Symbol represents a code symbol extracted via AST.
type Symbol struct {
	Name      string     `json:"name"`
	Kind      SymbolKind `json:"kind"`
	File      string     `json:"file"`
	Line      int        `json:"line"`
	EndLine   int        `json:"end_line"`
	Exported  bool       `json:"exported"`
	Receiver  string     `json:"receiver,omitempty"`  // for methods
	Signature string     `json:"signature,omitempty"` // full func signature
	TypeName  string     `json:"type_name,omitempty"` // for vars/consts: the type
	Doc       string     `json:"doc,omitempty"`       // doc comment
}

// CallEdge represents a call relationship between two symbols.
type CallEdge struct {
	Caller     string `json:"caller"`      // "pkg.Func" or "pkg.Type.Method"
	Callee     string `json:"callee"`      // function/method being called
	CalleePkg  string `json:"callee_pkg"`  // package of the callee (if selector)
	File       string `json:"file"`
	Line       int    `json:"line"`
	IsMethod   bool   `json:"is_method"`   // callee is a method call (x.Foo())
	IsDeferred bool   `json:"is_deferred"` // call is deferred
}

// InterfaceInfo describes an interface's method set.
type InterfaceInfo struct {
	Name    string   `json:"name"`
	File    string   `json:"file"`
	Line    int      `json:"line"`
	Methods []string `json:"methods"` // method signatures
}

// ImportEdge represents an import relationship.
type ImportEdge struct {
	File       string `json:"file"`
	ImportPath string `json:"import_path"`
	Alias      string `json:"alias,omitempty"` // named import alias
}

// FileAnalysis is the AST analysis result for a single file.
type FileAnalysis struct {
	Path       string          `json:"path"`
	Package    string          `json:"package"`
	Symbols    []Symbol        `json:"symbols"`
	Calls      []CallEdge      `json:"calls"`
	Interfaces []InterfaceInfo `json:"interfaces"`
	Imports    []ImportEdge    `json:"imports"`
}

// Analysis is the combined result for a codebase or set of files.
type Analysis struct {
	Files      []*FileAnalysis `json:"files"`
	AllSymbols []Symbol        `json:"all_symbols"`
	AllCalls   []CallEdge      `json:"all_calls"`

	// Derived indexes (built lazily)
	symbolsByName map[string][]Symbol
	callerMap     map[string][]CallEdge // caller -> edges
	calleeMap     map[string][]CallEdge // callee -> edges
}

// AnalyzeFile parses a single Go file and extracts its AST structure.
func AnalyzeFile(path, relPath string) (*FileAnalysis, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return AnalyzeSource(src, relPath)
}

// AnalyzeSource parses Go source code and extracts AST structure.
func AnalyzeSource(src []byte, relPath string) (*FileAnalysis, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, relPath, src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", relPath, err)
	}

	fa := &FileAnalysis{
		Path:    relPath,
		Package: f.Name.Name,
	}

	// Extract imports
	for _, imp := range f.Imports {
		ie := ImportEdge{
			File:       relPath,
			ImportPath: strings.Trim(imp.Path.Value, `"`),
		}
		if imp.Name != nil {
			ie.Alias = imp.Name.Name
		}
		fa.Imports = append(fa.Imports, ie)
	}

	// Walk AST for declarations and calls
	var currentFunc string // tracks which function we're inside
	var currentRecv string

	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			sym := extractFuncSymbol(fset, d, relPath)
			fa.Symbols = append(fa.Symbols, sym)

			// Set context for call extraction
			if d.Recv != nil && len(d.Recv.List) > 0 {
				currentRecv = receiverTypeName(d.Recv.List[0].Type)
				currentFunc = currentRecv + "." + d.Name.Name
			} else {
				currentRecv = ""
				currentFunc = d.Name.Name
			}

			// Walk function body for calls
			if d.Body != nil {
				calls := extractCalls(fset, d.Body, currentFunc, relPath)
				fa.Calls = append(fa.Calls, calls...)
			}

		case *ast.GenDecl:
			symbols, ifaces := extractGenDecl(fset, d, relPath)
			fa.Symbols = append(fa.Symbols, symbols...)
			fa.Interfaces = append(fa.Interfaces, ifaces...)
		}
	}

	currentFunc = ""
	currentRecv = ""
	_ = currentRecv

	return fa, nil
}

// AnalyzeDir analyzes all Go files in a directory tree.
func AnalyzeDir(root string) (*Analysis, error) {
	a := &Analysis{}

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			name := info.Name()
			if strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules" || name == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		rel, _ := filepath.Rel(root, path)
		fa, err := AnalyzeFile(path, rel)
		if err != nil {
			return nil // skip unparseable files
		}
		a.Files = append(a.Files, fa)
		a.AllSymbols = append(a.AllSymbols, fa.Symbols...)
		a.AllCalls = append(a.AllCalls, fa.Calls...)
		return nil
	})

	return a, err
}

// AnalyzeFiles analyzes specific Go files.
func AnalyzeFiles(root string, files []string) (*Analysis, error) {
	a := &Analysis{}

	for _, file := range files {
		fullPath := filepath.Join(root, file)
		if !strings.HasSuffix(file, ".go") || strings.HasSuffix(file, "_test.go") {
			continue
		}
		fa, err := AnalyzeFile(fullPath, file)
		if err != nil {
			continue
		}
		a.Files = append(a.Files, fa)
		a.AllSymbols = append(a.AllSymbols, fa.Symbols...)
		a.AllCalls = append(a.AllCalls, fa.Calls...)
	}

	return a, nil
}

// SymbolsByName returns symbols grouped by name.
func (a *Analysis) SymbolsByName() map[string][]Symbol {
	if a.symbolsByName != nil {
		return a.symbolsByName
	}
	a.symbolsByName = make(map[string][]Symbol)
	for _, s := range a.AllSymbols {
		a.symbolsByName[s.Name] = append(a.symbolsByName[s.Name], s)
	}
	return a.symbolsByName
}

// CallersOf returns all call edges where the given name is the callee.
func (a *Analysis) CallersOf(name string) []CallEdge {
	if a.calleeMap == nil {
		a.calleeMap = make(map[string][]CallEdge)
		for _, c := range a.AllCalls {
			a.calleeMap[c.Callee] = append(a.calleeMap[c.Callee], c)
		}
	}
	return a.calleeMap[name]
}

// CalleesOf returns all call edges where the given name is the caller.
func (a *Analysis) CalleesOf(name string) []CallEdge {
	if a.callerMap == nil {
		a.callerMap = make(map[string][]CallEdge)
		for _, c := range a.AllCalls {
			a.callerMap[c.Caller] = append(a.callerMap[c.Caller], c)
		}
	}
	return a.callerMap[name]
}

// Reachable returns all symbols reachable from the given entry points
// via the call graph (transitive closure).
func (a *Analysis) Reachable(entryPoints []string) map[string]bool {
	reachable := make(map[string]bool)
	queue := make([]string, len(entryPoints))
	copy(queue, entryPoints)

	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if reachable[name] {
			continue
		}
		reachable[name] = true

		for _, edge := range a.CalleesOf(name) {
			if !reachable[edge.Callee] {
				queue = append(queue, edge.Callee)
			}
		}
	}
	return reachable
}

// DeadSymbols returns exported symbols that are not reachable from any
// entry point (main, init, Test*, Benchmark*, Example*, HTTP handlers).
func (a *Analysis) DeadSymbols() []Symbol {
	// Find entry points
	var entries []string
	for _, s := range a.AllSymbols {
		if isEntryPoint(s) {
			qualified := s.Name
			if s.Receiver != "" {
				qualified = s.Receiver + "." + s.Name
			}
			entries = append(entries, qualified)
		}
	}

	reachable := a.Reachable(entries)

	var dead []Symbol
	for _, s := range a.AllSymbols {
		if !s.Exported {
			continue
		}
		qualified := s.Name
		if s.Receiver != "" {
			qualified = s.Receiver + "." + s.Name
		}
		if !reachable[qualified] && !reachable[s.Name] {
			dead = append(dead, s)
		}
	}
	return dead
}

// InterfaceSatisfaction checks which concrete types satisfy which interfaces.
// Returns a map of interface name -> list of type names that implement it.
func (a *Analysis) InterfaceSatisfaction() map[string][]string {
	// Collect all interface method sets
	ifaceMethods := make(map[string]map[string]bool) // iface -> set of method names
	for _, f := range a.Files {
		for _, iface := range f.Interfaces {
			methods := make(map[string]bool)
			for _, m := range iface.Methods {
				methods[m] = true
			}
			ifaceMethods[iface.Name] = methods
		}
	}

	// Collect method sets per type (from method symbols)
	typeMethods := make(map[string]map[string]bool)
	for _, s := range a.AllSymbols {
		if s.Kind == KindMethod && s.Receiver != "" {
			if typeMethods[s.Receiver] == nil {
				typeMethods[s.Receiver] = make(map[string]bool)
			}
			typeMethods[s.Receiver][s.Name] = true
		}
	}

	// Check satisfaction
	result := make(map[string][]string)
	for ifaceName, required := range ifaceMethods {
		for typeName, has := range typeMethods {
			satisfies := true
			for method := range required {
				if !has[method] {
					satisfies = false
					break
				}
			}
			if satisfies && len(required) > 0 {
				result[ifaceName] = append(result[ifaceName], typeName)
			}
		}
	}

	// Sort for determinism
	for k := range result {
		sort.Strings(result[k])
	}
	return result
}

// --- Internal extraction functions ---

func extractFuncSymbol(fset *token.FileSet, d *ast.FuncDecl, file string) Symbol {
	pos := fset.Position(d.Pos())
	endPos := fset.Position(d.End())

	sym := Symbol{
		Name:     d.Name.Name,
		Kind:     KindFunction,
		File:     file,
		Line:     pos.Line,
		EndLine:  endPos.Line,
		Exported: d.Name.IsExported(),
	}

	if d.Doc != nil {
		sym.Doc = d.Doc.Text()
	}

	// Method with receiver
	if d.Recv != nil && len(d.Recv.List) > 0 {
		sym.Kind = KindMethod
		sym.Receiver = receiverTypeName(d.Recv.List[0].Type)
	}

	// Build signature
	sym.Signature = buildFuncSignature(d)

	return sym
}

func extractGenDecl(fset *token.FileSet, d *ast.GenDecl, file string) ([]Symbol, []InterfaceInfo) {
	var symbols []Symbol
	var ifaces []InterfaceInfo

	for _, spec := range d.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			pos := fset.Position(s.Pos())
			endPos := fset.Position(s.End())
			sym := Symbol{
				Name:     s.Name.Name,
				File:     file,
				Line:     pos.Line,
				EndLine:  endPos.Line,
				Exported: s.Name.IsExported(),
			}

			// Get doc from either the GenDecl or the TypeSpec
			if s.Doc != nil {
				sym.Doc = s.Doc.Text()
			} else if d.Doc != nil && len(d.Specs) == 1 {
				sym.Doc = d.Doc.Text()
			}

			switch t := s.Type.(type) {
			case *ast.StructType:
				sym.Kind = KindStruct
				// Extract fields as child symbols
				if t.Fields != nil {
					for _, field := range t.Fields.List {
						for _, name := range field.Names {
							fpos := fset.Position(name.Pos())
							symbols = append(symbols, Symbol{
								Name:     name.Name,
								Kind:     KindField,
								File:     file,
								Line:     fpos.Line,
								Exported: name.IsExported(),
								Receiver: s.Name.Name,
								TypeName: exprToString(field.Type),
							})
						}
					}
				}
			case *ast.InterfaceType:
				sym.Kind = KindInterface
				// Extract interface methods
				info := InterfaceInfo{
					Name: s.Name.Name,
					File: file,
					Line: pos.Line,
				}
				if t.Methods != nil {
					for _, method := range t.Methods.List {
						for _, name := range method.Names {
							info.Methods = append(info.Methods, name.Name)
						}
					}
				}
				ifaces = append(ifaces, info)
			default:
				sym.Kind = KindType
				sym.TypeName = exprToString(s.Type)
			}
			symbols = append(symbols, sym)

		case *ast.ValueSpec:
			for _, name := range s.Names {
				pos := fset.Position(name.Pos())
				sym := Symbol{
					Name:     name.Name,
					File:     file,
					Line:     pos.Line,
					Exported: name.IsExported(),
				}
				if d.Tok == token.CONST {
					sym.Kind = KindConstant
				} else {
					sym.Kind = KindVariable
				}
				if s.Type != nil {
					sym.TypeName = exprToString(s.Type)
				}
				// Get doc
				if s.Doc != nil {
					sym.Doc = s.Doc.Text()
				} else if d.Doc != nil && len(d.Specs) == 1 {
					sym.Doc = d.Doc.Text()
				}
				symbols = append(symbols, sym)
			}
		}
	}

	return symbols, ifaces
}

func extractCalls(fset *token.FileSet, body *ast.BlockStmt, caller, file string) []CallEdge {
	var calls []CallEdge

	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			edge := CallEdge{
				Caller: caller,
				File:   file,
				Line:   fset.Position(node.Pos()).Line,
			}

			switch fn := node.Fun.(type) {
			case *ast.Ident:
				// Direct call: Foo()
				edge.Callee = fn.Name
			case *ast.SelectorExpr:
				// Method/package call: x.Foo() or pkg.Foo()
				edge.IsMethod = true
				edge.Callee = fn.Sel.Name
				if ident, ok := fn.X.(*ast.Ident); ok {
					edge.CalleePkg = ident.Name
				}
			default:
				return true
			}

			if edge.Callee != "" {
				calls = append(calls, edge)
			}

		case *ast.DeferStmt:
			if call, ok := node.Call.Fun.(*ast.Ident); ok {
				calls = append(calls, CallEdge{
					Caller:     caller,
					Callee:     call.Name,
					File:       file,
					Line:       fset.Position(node.Pos()).Line,
					IsDeferred: true,
				})
			} else if sel, ok := node.Call.Fun.(*ast.SelectorExpr); ok {
				edge := CallEdge{
					Caller:     caller,
					Callee:     sel.Sel.Name,
					File:       file,
					Line:       fset.Position(node.Pos()).Line,
					IsMethod:   true,
					IsDeferred: true,
				}
				if ident, ok := sel.X.(*ast.Ident); ok {
					edge.CalleePkg = ident.Name
				}
				calls = append(calls, edge)
			}
			return false // don't double-count the inner CallExpr
		}
		return true
	})

	return calls
}

// --- Helpers ---

func receiverTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		if ident, ok := t.X.(*ast.Ident); ok {
			return ident.Name
		}
	case *ast.Ident:
		return t.Name
	}
	return ""
}

func buildFuncSignature(d *ast.FuncDecl) string {
	var b strings.Builder
	b.WriteString(d.Name.Name)
	b.WriteString("(")

	if d.Type.Params != nil {
		params := formatFieldList(d.Type.Params)
		b.WriteString(params)
	}
	b.WriteString(")")

	if d.Type.Results != nil && len(d.Type.Results.List) > 0 {
		results := formatFieldList(d.Type.Results)
		if len(d.Type.Results.List) == 1 && len(d.Type.Results.List[0].Names) == 0 {
			b.WriteString(" ")
			b.WriteString(results)
		} else {
			b.WriteString(" (")
			b.WriteString(results)
			b.WriteString(")")
		}
	}

	return b.String()
}

func formatFieldList(fl *ast.FieldList) string {
	var parts []string
	for _, field := range fl.List {
		typeStr := exprToString(field.Type)
		if len(field.Names) == 0 {
			parts = append(parts, typeStr)
		} else {
			for _, name := range field.Names {
				parts = append(parts, name.Name+" "+typeStr)
			}
		}
	}
	return strings.Join(parts, ", ")
}

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
		return "[...]" + exprToString(t.Elt)
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
	}
	return "?"
}

func isEntryPoint(s Symbol) bool {
	if s.Name == "main" || s.Name == "init" {
		return true
	}
	if strings.HasPrefix(s.Name, "Test") || strings.HasPrefix(s.Name, "Benchmark") ||
		strings.HasPrefix(s.Name, "Example") || strings.HasPrefix(s.Name, "Fuzz") {
		return true
	}
	// HTTP handler patterns — methods on types ending in Handler/Server
	if s.Kind == KindMethod {
		lower := strings.ToLower(s.Name)
		if lower == "servehttp" || lower == "handle" || lower == "serve" {
			return true
		}
	}
	return false
}
