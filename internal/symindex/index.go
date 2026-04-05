// Package symindex builds a symbol index for a codebase.
// Inspired by Aider's repo-map and claw-code's code intelligence:
//
// Fast symbol lookup is critical for AI coding tools:
// - Find function/type/method definitions without full-text search
// - Build call graphs for impact analysis
// - Generate repo-maps (compact summaries of what's where)
// - Support "go to definition" for LLM context injection
//
// For Go files, uses real go/parser AST for accurate extraction with
// call graph construction, interface satisfaction, and typed signatures.
// For other languages, falls back to regex-based extraction.
package symindex

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/ericmacdougall/stoke/internal/goast"
)

// SymbolKind classifies a code symbol.
type SymbolKind string

const (
	KindFunction  SymbolKind = "function"
	KindMethod    SymbolKind = "method"
	KindType      SymbolKind = "type"
	KindInterface SymbolKind = "interface"
	KindClass     SymbolKind = "class"
	KindVariable  SymbolKind = "variable"
	KindConstant  SymbolKind = "constant"
	KindImport    SymbolKind = "import"
	KindPackage   SymbolKind = "package"
	KindField     SymbolKind = "field"
)

// Symbol represents a code symbol with its location.
type Symbol struct {
	Name      string     `json:"name"`
	Kind      SymbolKind `json:"kind"`
	File      string     `json:"file"`
	Line      int        `json:"line"`
	EndLine   int        `json:"end_line,omitempty"`
	Parent    string     `json:"parent,omitempty"`    // receiver type for methods, class for nested
	Signature string     `json:"signature,omitempty"` // full declaration line
	Exported  bool       `json:"exported"`
	TypeName  string     `json:"type_name,omitempty"` // typed info from AST
	Doc       string     `json:"doc,omitempty"`       // doc comment
}

// CallEdge represents a call from one symbol to another.
type CallEdge struct {
	Caller    string `json:"caller"`
	Callee    string `json:"callee"`
	CalleePkg string `json:"callee_pkg,omitempty"`
	File      string `json:"file"`
	Line      int    `json:"line"`
}

// Index is an in-memory symbol index with call graph.
type Index struct {
	root    string
	symbols []Symbol
	byName  map[string][]int // name -> indices
	byFile  map[string][]int // file -> indices
	byKind  map[SymbolKind][]int

	// Call graph (populated for Go files via AST)
	calls     []CallEdge
	callerMap map[string][]int // caller -> call edge indices
	calleeMap map[string][]int // callee -> call edge indices

	// Interface satisfaction (Go files via AST)
	ifaceSatisfaction map[string][]string // interface -> implementing types
}

// langPattern holds extraction patterns for a language.
type langPattern struct {
	Extensions []string
	Patterns   []symbolPattern
}

type symbolPattern struct {
	Kind   SymbolKind
	Regex  *regexp.Regexp
	Parent int // capture group for parent (0 = none)
	Name   int // capture group for name
}

var languages = []langPattern{
	{
		Extensions: []string{".py"},
		Patterns: []symbolPattern{
			{Kind: KindFunction, Regex: regexp.MustCompile(`^def\s+(\w+)\s*\(`), Name: 1},
			{Kind: KindClass, Regex: regexp.MustCompile(`^class\s+(\w+)`), Name: 1},
			{Kind: KindMethod, Regex: regexp.MustCompile(`^\s+def\s+(\w+)\s*\(self`), Name: 1},
			{Kind: KindVariable, Regex: regexp.MustCompile(`^(\w+)\s*=`), Name: 1},
		},
	},
	{
		Extensions: []string{".ts", ".tsx", ".js", ".jsx"},
		Patterns: []symbolPattern{
			{Kind: KindFunction, Regex: regexp.MustCompile(`^(?:export\s+)?(?:async\s+)?function\s+(\w+)`), Name: 1},
			{Kind: KindClass, Regex: regexp.MustCompile(`^(?:export\s+)?class\s+(\w+)`), Name: 1},
			{Kind: KindInterface, Regex: regexp.MustCompile(`^(?:export\s+)?interface\s+(\w+)`), Name: 1},
			{Kind: KindType, Regex: regexp.MustCompile(`^(?:export\s+)?type\s+(\w+)\s*=`), Name: 1},
			{Kind: KindVariable, Regex: regexp.MustCompile(`^(?:export\s+)?(?:const|let|var)\s+(\w+)`), Name: 1},
			{Kind: KindMethod, Regex: regexp.MustCompile(`^\s+(?:async\s+)?(\w+)\s*\(`), Name: 1},
		},
	},
	{
		Extensions: []string{".rs"},
		Patterns: []symbolPattern{
			{Kind: KindFunction, Regex: regexp.MustCompile(`^(?:pub\s+)?fn\s+(\w+)`), Name: 1},
			{Kind: KindType, Regex: regexp.MustCompile(`^(?:pub\s+)?struct\s+(\w+)`), Name: 1},
			{Kind: KindInterface, Regex: regexp.MustCompile(`^(?:pub\s+)?trait\s+(\w+)`), Name: 1},
			{Kind: KindType, Regex: regexp.MustCompile(`^(?:pub\s+)?enum\s+(\w+)`), Name: 1},
			{Kind: KindConstant, Regex: regexp.MustCompile(`^(?:pub\s+)?const\s+(\w+)`), Name: 1},
		},
	},
	{
		Extensions: []string{".java"},
		Patterns: []symbolPattern{
			{Kind: KindClass, Regex: regexp.MustCompile(`(?:public|private|protected)?\s*class\s+(\w+)`), Name: 1},
			{Kind: KindInterface, Regex: regexp.MustCompile(`(?:public|private|protected)?\s*interface\s+(\w+)`), Name: 1},
			{Kind: KindMethod, Regex: regexp.MustCompile(`(?:public|private|protected)\s+\w+\s+(\w+)\s*\(`), Name: 1},
		},
	},
}

// Build creates a symbol index for the directory tree.
// For Go files, uses real AST parsing with call graph construction.
func Build(root string) (*Index, error) {
	idx := &Index{
		root:   root,
		byName: make(map[string][]int),
		byFile: make(map[string][]int),
		byKind: make(map[SymbolKind][]int),
	}

	// First pass: AST-based analysis for Go files
	goAnalysis, _ := goast.AnalyzeDir(root)
	if goAnalysis != nil {
		idx.ingestGoAST(goAnalysis)
	}

	// Second pass: regex-based extraction for non-Go files
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			name := info.Name()
			if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == "__pycache__" {
				return filepath.SkipDir
			}
			return nil
		}

		ext := filepath.Ext(path)
		// Skip Go files — already handled by AST
		if ext == ".go" {
			return nil
		}

		lang := findLang(ext)
		if lang == nil {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		rel, _ := filepath.Rel(root, path)
		symbols := extractSymbolsRegex(string(data), rel, lang)
		for _, sym := range symbols {
			idx.addSymbol(sym)
		}
		return nil
	})

	return idx, err
}

// BuildFromFiles indexes specific files.
func BuildFromFiles(root string, files []string) (*Index, error) {
	idx := &Index{
		root:   root,
		byName: make(map[string][]int),
		byFile: make(map[string][]int),
		byKind: make(map[SymbolKind][]int),
	}

	// Separate Go and non-Go files
	var goFiles []string
	for _, file := range files {
		if strings.HasSuffix(file, ".go") && !strings.HasSuffix(file, "_test.go") {
			goFiles = append(goFiles, file)
		}
	}

	// AST for Go files
	if len(goFiles) > 0 {
		goAnalysis, _ := goast.AnalyzeFiles(root, goFiles)
		if goAnalysis != nil {
			idx.ingestGoAST(goAnalysis)
		}
	}

	// Regex for non-Go files
	for _, file := range files {
		if strings.HasSuffix(file, ".go") {
			continue
		}
		fullPath := filepath.Join(root, file)
		ext := filepath.Ext(fullPath)
		lang := findLang(ext)
		if lang == nil {
			continue
		}

		data, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}

		symbols := extractSymbolsRegex(string(data), file, lang)
		for _, sym := range symbols {
			idx.addSymbol(sym)
		}
	}

	return idx, nil
}

// ingestGoAST converts goast.Analysis into symindex symbols and call edges.
func (idx *Index) ingestGoAST(a *goast.Analysis) {
	for _, s := range a.AllSymbols {
		sym := Symbol{
			Name:      s.Name,
			Kind:      goastKindToSymKind(s.Kind),
			File:      s.File,
			Line:      s.Line,
			EndLine:   s.EndLine,
			Parent:    s.Receiver,
			Signature: s.Signature,
			Exported:  s.Exported,
			TypeName:  s.TypeName,
			Doc:       s.Doc,
		}
		idx.addSymbol(sym)
	}

	// Ingest call graph
	for _, c := range a.AllCalls {
		edge := CallEdge{
			Caller:    c.Caller,
			Callee:    c.Callee,
			CalleePkg: c.CalleePkg,
			File:      c.File,
			Line:      c.Line,
		}
		idx.addCallEdge(edge)
	}

	// Ingest interface satisfaction
	idx.ifaceSatisfaction = a.InterfaceSatisfaction()
}

func goastKindToSymKind(k goast.SymbolKind) SymbolKind {
	switch k {
	case goast.KindFunction:
		return KindFunction
	case goast.KindMethod:
		return KindMethod
	case goast.KindType:
		return KindType
	case goast.KindInterface:
		return KindInterface
	case goast.KindStruct:
		return KindType
	case goast.KindVariable:
		return KindVariable
	case goast.KindConstant:
		return KindConstant
	case goast.KindField:
		return KindField
	}
	return KindVariable
}

func (idx *Index) addSymbol(sym Symbol) {
	i := len(idx.symbols)
	idx.symbols = append(idx.symbols, sym)
	idx.byName[sym.Name] = append(idx.byName[sym.Name], i)
	idx.byFile[sym.File] = append(idx.byFile[sym.File], i)
	idx.byKind[sym.Kind] = append(idx.byKind[sym.Kind], i)
}

func (idx *Index) addCallEdge(edge CallEdge) {
	i := len(idx.calls)
	idx.calls = append(idx.calls, edge)
	if idx.callerMap == nil {
		idx.callerMap = make(map[string][]int)
	}
	if idx.calleeMap == nil {
		idx.calleeMap = make(map[string][]int)
	}
	idx.callerMap[edge.Caller] = append(idx.callerMap[edge.Caller], i)
	idx.calleeMap[edge.Callee] = append(idx.calleeMap[edge.Callee], i)
}

// Lookup finds symbols by name.
func (idx *Index) Lookup(name string) []Symbol {
	indices := idx.byName[name]
	result := make([]Symbol, len(indices))
	for i, j := range indices {
		result[i] = idx.symbols[j]
	}
	return result
}

// Search finds symbols matching a prefix.
func (idx *Index) Search(prefix string) []Symbol {
	var result []Symbol
	prefix = strings.ToLower(prefix)
	for name, indices := range idx.byName {
		if strings.HasPrefix(strings.ToLower(name), prefix) {
			for _, j := range indices {
				result = append(result, idx.symbols[j])
			}
		}
	}
	return result
}

// InFile returns all symbols in a file.
func (idx *Index) InFile(file string) []Symbol {
	indices := idx.byFile[file]
	result := make([]Symbol, len(indices))
	for i, j := range indices {
		result[i] = idx.symbols[j]
	}
	return result
}

// ByKind returns all symbols of a given kind.
func (idx *Index) ByKind(kind SymbolKind) []Symbol {
	indices := idx.byKind[kind]
	result := make([]Symbol, len(indices))
	for i, j := range indices {
		result[i] = idx.symbols[j]
	}
	return result
}

// Exported returns only exported/public symbols.
func (idx *Index) Exported() []Symbol {
	var result []Symbol
	for _, s := range idx.symbols {
		if s.Exported {
			result = append(result, s)
		}
	}
	return result
}

// AllSymbols returns every indexed symbol.
func (idx *Index) AllSymbols() []Symbol {
	result := make([]Symbol, len(idx.symbols))
	copy(result, idx.symbols)
	return result
}

// Count returns the total number of symbols.
func (idx *Index) Count() int {
	return len(idx.symbols)
}

// Files returns all indexed files.
func (idx *Index) Files() []string {
	files := make([]string, 0, len(idx.byFile))
	for f := range idx.byFile {
		files = append(files, f)
	}
	sort.Strings(files)
	return files
}

// --- Call Graph API ---

// Callers returns all call edges where the given symbol is the callee.
func (idx *Index) Callers(name string) []CallEdge {
	var result []CallEdge
	for _, i := range idx.calleeMap[name] {
		result = append(result, idx.calls[i])
	}
	return result
}

// Callees returns all call edges where the given symbol is the caller.
func (idx *Index) Callees(name string) []CallEdge {
	var result []CallEdge
	for _, i := range idx.callerMap[name] {
		result = append(result, idx.calls[i])
	}
	return result
}

// CallGraph returns all call edges.
func (idx *Index) CallGraph() []CallEdge {
	result := make([]CallEdge, len(idx.calls))
	copy(result, idx.calls)
	return result
}

// Implementors returns types that implement the given interface.
func (idx *Index) Implementors(ifaceName string) []string {
	if idx.ifaceSatisfaction == nil {
		return nil
	}
	return idx.ifaceSatisfaction[ifaceName]
}

// RepoMap generates a compact summary of the codebase (like Aider's repo-map).
func (idx *Index) RepoMap() string {
	var b strings.Builder
	files := idx.Files()

	for _, file := range files {
		symbols := idx.InFile(file)
		if len(symbols) == 0 {
			continue
		}
		fmt.Fprintf(&b, "%s:\n", file)
		for _, s := range symbols {
			if !s.Exported {
				continue
			}
			// Skip fields in repo map — too verbose
			if s.Kind == KindField {
				continue
			}
			prefix := "  "
			if s.Parent != "" {
				prefix = "    "
			}
			if s.Signature != "" {
				fmt.Fprintf(&b, "%s%s %s: %s\n", prefix, s.Kind, s.Name, s.Signature)
			} else {
				fmt.Fprintf(&b, "%s%s %s\n", prefix, s.Kind, s.Name)
			}
		}
	}
	return b.String()
}

// Stats returns index statistics.
func (idx *Index) Stats() map[string]int {
	stats := map[string]int{
		"total_symbols": len(idx.symbols),
		"files":         len(idx.byFile),
		"call_edges":    len(idx.calls),
	}
	for kind, indices := range idx.byKind {
		stats[string(kind)] = len(indices)
	}
	exported := 0
	for _, s := range idx.symbols {
		if s.Exported {
			exported++
		}
	}
	stats["exported"] = exported
	if idx.ifaceSatisfaction != nil {
		stats["interfaces_with_implementors"] = len(idx.ifaceSatisfaction)
	}
	return stats
}

// --- Regex fallback for non-Go files ---

func extractSymbolsRegex(source, file string, lang *langPattern) []Symbol {
	lines := strings.Split(source, "\n")
	var symbols []Symbol

	for lineNum, line := range lines {
		for _, pat := range lang.Patterns {
			m := pat.Regex.FindStringSubmatch(line)
			if m == nil {
				continue
			}

			name := ""
			if pat.Name > 0 && pat.Name < len(m) {
				name = m[pat.Name]
			}
			if name == "" {
				continue
			}

			parent := ""
			if pat.Parent > 0 && pat.Parent < len(m) {
				parent = m[pat.Parent]
			}

			sym := Symbol{
				Name:      name,
				Kind:      pat.Kind,
				File:      file,
				Line:      lineNum + 1,
				Parent:    parent,
				Signature: strings.TrimSpace(line),
				Exported:  isExported(name, lang),
			}
			symbols = append(symbols, sym)
			break // first match wins per line
		}
	}

	return symbols
}

func isExported(name string, lang *langPattern) bool {
	if len(name) == 0 {
		return false
	}
	ext := lang.Extensions[0]
	switch ext {
	case ".py":
		return !strings.HasPrefix(name, "_")
	case ".rs":
		return true
	default:
		return true
	}
}

func findLang(ext string) *langPattern {
	for i := range languages {
		for _, e := range languages[i].Extensions {
			if e == ext {
				return &languages[i]
			}
		}
	}
	return nil
}
