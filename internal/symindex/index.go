// Package symindex builds an AST-level symbol index for a codebase.
// Inspired by Aider's repo-map and claw-code's code intelligence:
//
// Fast symbol lookup is critical for AI coding tools:
// - Find function/type/method definitions without full-text search
// - Build call graphs for impact analysis
// - Generate repo-maps (compact summaries of what's where)
// - Support "go to definition" for LLM context injection
//
// This uses regex-based extraction (not full AST parsing) for speed
// and multi-language support without compiler dependencies.
package symindex

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
	KindClass     SymbolKind = "class"
	KindVariable  SymbolKind = "variable"
	KindConstant  SymbolKind = "constant"
	KindImport    SymbolKind = "import"
	KindPackage   SymbolKind = "package"
)

// Symbol represents a code symbol with its location.
type Symbol struct {
	Name      string     `json:"name"`
	Kind      SymbolKind `json:"kind"`
	File      string     `json:"file"`
	Line      int        `json:"line"`
	Parent    string     `json:"parent,omitempty"`    // receiver type for methods, class for nested
	Signature string     `json:"signature,omitempty"` // full declaration line
	Exported  bool       `json:"exported"`
}

// Index is an in-memory symbol index.
type Index struct {
	root    string
	symbols []Symbol
	byName  map[string][]int // name -> indices
	byFile  map[string][]int // file -> indices
	byKind  map[SymbolKind][]int
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
		Extensions: []string{".go"},
		Patterns: []symbolPattern{
			{Kind: KindFunction, Regex: regexp.MustCompile(`^func\s+(\w+)\s*\(`), Name: 1},
			{Kind: KindMethod, Regex: regexp.MustCompile(`^func\s+\(\w+\s+\*?(\w+)\)\s+(\w+)\s*\(`), Parent: 1, Name: 2},
			{Kind: KindType, Regex: regexp.MustCompile(`^type\s+(\w+)\s+struct\b`), Name: 1},
			{Kind: KindInterface, Regex: regexp.MustCompile(`^type\s+(\w+)\s+interface\b`), Name: 1},
			{Kind: KindVariable, Regex: regexp.MustCompile(`^var\s+(\w+)\s`), Name: 1},
			{Kind: KindConstant, Regex: regexp.MustCompile(`^\s*(\w+)\s*=`), Name: 1}, // inside const block
			{Kind: KindPackage, Regex: regexp.MustCompile(`^package\s+(\w+)`), Name: 1},
		},
	},
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
func Build(root string) (*Index, error) {
	idx := &Index{
		root:   root,
		byName: make(map[string][]int),
		byFile: make(map[string][]int),
		byKind: make(map[SymbolKind][]int),
	}

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
		lang := findLang(ext)
		if lang == nil {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		rel, _ := filepath.Rel(root, path)
		symbols := extractSymbols(string(data), rel, lang)
		for _, sym := range symbols {
			i := len(idx.symbols)
			idx.symbols = append(idx.symbols, sym)
			idx.byName[sym.Name] = append(idx.byName[sym.Name], i)
			idx.byFile[sym.File] = append(idx.byFile[sym.File], i)
			idx.byKind[sym.Kind] = append(idx.byKind[sym.Kind], i)
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

	for _, file := range files {
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

		symbols := extractSymbols(string(data), file, lang)
		for _, sym := range symbols {
			i := len(idx.symbols)
			idx.symbols = append(idx.symbols, sym)
			idx.byName[sym.Name] = append(idx.byName[sym.Name], i)
			idx.byFile[sym.File] = append(idx.byFile[sym.File], i)
			idx.byKind[sym.Kind] = append(idx.byKind[sym.Kind], i)
		}
	}

	return idx, nil
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
	return stats
}

func extractSymbols(source, file string, lang *langPattern) []Symbol {
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
	case ".go":
		// Go: uppercase first letter
		return name[0] >= 'A' && name[0] <= 'Z'
	case ".py":
		// Python: no leading underscore
		return !strings.HasPrefix(name, "_")
	case ".rs":
		// Rust: pub keyword (already in regex), assume exported if matched
		return true
	default:
		// JS/TS/Java: assume exported (export keyword in regex)
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
