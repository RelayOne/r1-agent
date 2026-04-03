// Package repomap generates a graph-ranked repository map for context injection.
// Inspired by Aider's repository map: a concise view of the codebase's most important
// classes, functions, and their relationships. This helps AI agents understand code
// structure without loading every file into context.
//
// Key patterns from Aider:
// - TreeSitter-based symbol extraction (we use regex for Go simplicity)
// - Graph ranking: files are nodes, imports/calls are edges
// - Budget-aware: only emit symbols that fit in the token budget
// - Relevance scoring: rank by connectivity to the current task's files
package repomap

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Symbol represents a code symbol (function, type, method, const).
type Symbol struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`     // func, type, method, const, var, interface
	File    string `json:"file"`     // relative path
	Line    int    `json:"line"`
	Package string `json:"package"`
	Signature string `json:"signature,omitempty"` // for functions
}

// FileNode represents a file in the dependency graph.
type FileNode struct {
	Path       string   `json:"path"`
	Package    string   `json:"package"`
	Symbols    []Symbol `json:"symbols"`
	Imports    []string `json:"imports"`
	ImportedBy []string `json:"imported_by"` // reverse edges
	Rank       float64  `json:"rank"`        // PageRank-style score
}

// RepoMap is a ranked view of the codebase.
type RepoMap struct {
	Root    string               `json:"root"`
	Files   map[string]*FileNode `json:"files"`
	Symbols []Symbol             `json:"symbols"` // all symbols, sorted by rank
}

// Go-specific symbol extraction patterns.
var (
	funcRe      = regexp.MustCompile(`^func\s+(\w+)\s*\(([^)]*)\)`)
	methodRe    = regexp.MustCompile(`^func\s+\(\w+\s+\*?(\w+)\)\s+(\w+)\s*\(([^)]*)\)`)
	typeRe      = regexp.MustCompile(`^type\s+(\w+)\s+(struct|interface)`)
	constRe     = regexp.MustCompile(`^\s*(\w+)\s*=`)
	importRe    = regexp.MustCompile(`^\s*"([^"]+)"`)
	packageRe   = regexp.MustCompile(`^package\s+(\w+)`)
	constBlockRe = regexp.MustCompile(`^const\s*\(`)
	varBlockRe  = regexp.MustCompile(`^var\s*\(`)
)

// Build generates a repository map by scanning Go source files.
func Build(root string) (*RepoMap, error) {
	rm := &RepoMap{
		Root:  root,
		Files: make(map[string]*FileNode),
	}

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip errors
		}
		// Skip hidden dirs, vendor, node_modules
		if info.IsDir() {
			base := filepath.Base(path)
			if strings.HasPrefix(base, ".") || base == "vendor" || base == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		rel, _ := filepath.Rel(root, path)
		node, err := parseGoFile(path, rel)
		if err != nil {
			return nil // skip unparseable files
		}
		rm.Files[rel] = node
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Build reverse import edges
	rm.buildReverseEdges()

	// Rank files
	rm.rankFiles()

	// Collect all symbols sorted by file rank
	rm.collectSymbols()

	return rm, nil
}

// Render produces a human-readable map within a token budget.
// Each symbol line ~= 10 tokens. Budget 0 means unlimited.
func (rm *RepoMap) Render(budget int) string {
	if budget <= 0 {
		budget = 100000
	}

	var sb strings.Builder
	sb.WriteString("# Repository Map\n\n")

	tokensUsed := 5 // header

	// Group symbols by file, sorted by rank
	type fileGroup struct {
		path    string
		rank    float64
		symbols []Symbol
	}
	groups := make(map[string]*fileGroup)
	for _, sym := range rm.Symbols {
		g, ok := groups[sym.File]
		if !ok {
			rank := 0.0
			if node, exists := rm.Files[sym.File]; exists {
				rank = node.Rank
			}
			g = &fileGroup{path: sym.File, rank: rank}
			groups[sym.File] = g
		}
		g.symbols = append(g.symbols, sym)
	}

	sorted := make([]*fileGroup, 0, len(groups))
	for _, g := range groups {
		sorted = append(sorted, g)
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].rank > sorted[j].rank
	})

	for _, g := range sorted {
		// Estimate tokens for this file section
		sectionTokens := 3 + len(g.symbols)*2 // header + symbols
		if tokensUsed+sectionTokens > budget {
			sb.WriteString(fmt.Sprintf("\n... (%d more files)\n", len(sorted)-len(groups)))
			break
		}

		sb.WriteString(fmt.Sprintf("## %s\n", g.path))
		for _, sym := range g.symbols {
			switch sym.Kind {
			case "func", "method":
				if sym.Signature != "" {
					sb.WriteString(fmt.Sprintf("  %s %s\n", sym.Kind, sym.Signature))
				} else {
					sb.WriteString(fmt.Sprintf("  %s %s()\n", sym.Kind, sym.Name))
				}
			case "type":
				sb.WriteString(fmt.Sprintf("  type %s\n", sym.Name))
			case "interface":
				sb.WriteString(fmt.Sprintf("  interface %s\n", sym.Name))
			default:
				sb.WriteString(fmt.Sprintf("  %s %s\n", sym.Kind, sym.Name))
			}
		}
		sb.WriteString("\n")
		tokensUsed += sectionTokens
	}

	return sb.String()
}

// RenderRelevant produces a map focused on files related to the given paths.
func (rm *RepoMap) RenderRelevant(relevantFiles []string, budget int) string {
	// Boost rank for relevant files and their neighbors
	boosted := make(map[string]bool)
	for _, f := range relevantFiles {
		boosted[f] = true
		if node, ok := rm.Files[f]; ok {
			for _, imp := range node.Imports {
				// Find files providing this import
				for path, n := range rm.Files {
					if strings.HasSuffix(imp, n.Package) {
						boosted[path] = true
					}
				}
			}
			for _, ib := range node.ImportedBy {
				boosted[ib] = true
			}
		}
	}

	// Temporarily boost ranks
	originalRanks := make(map[string]float64)
	for path := range boosted {
		if node, ok := rm.Files[path]; ok {
			originalRanks[path] = node.Rank
			node.Rank *= 3.0 // 3x boost for relevant files
		}
	}
	defer func() {
		for path, rank := range originalRanks {
			rm.Files[path].Rank = rank
		}
	}()

	rm.collectSymbols()
	return rm.Render(budget)
}

// --- Internal ---

func parseGoFile(path, rel string) (*FileNode, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	node := &FileNode{Path: rel}
	scanner := bufio.NewScanner(f)
	lineNum := 0
	inImportBlock := false
	inConstBlock := false
	inVarBlock := false

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		// Package
		if m := packageRe.FindStringSubmatch(line); m != nil {
			node.Package = m[1]
		}

		// Import block
		if strings.HasPrefix(trimmed, "import (") {
			inImportBlock = true
			continue
		}
		if inImportBlock {
			if trimmed == ")" {
				inImportBlock = false
				continue
			}
			if m := importRe.FindStringSubmatch(line); m != nil {
				node.Imports = append(node.Imports, m[1])
			}
			continue
		}
		// Single import
		if strings.HasPrefix(trimmed, "import ") {
			if m := importRe.FindStringSubmatch(line); m != nil {
				node.Imports = append(node.Imports, m[1])
			}
			continue
		}

		// Const/var blocks (skip internals)
		if constBlockRe.MatchString(trimmed) {
			inConstBlock = true
			continue
		}
		if varBlockRe.MatchString(trimmed) {
			inVarBlock = true
			continue
		}
		if (inConstBlock || inVarBlock) && trimmed == ")" {
			inConstBlock = false
			inVarBlock = false
			continue
		}
		if inConstBlock || inVarBlock {
			if m := constRe.FindStringSubmatch(line); m != nil {
				// Only export public consts
				if len(m[1]) > 0 && m[1][0] >= 'A' && m[1][0] <= 'Z' {
					kind := "const"
					if inVarBlock {
						kind = "var"
					}
					node.Symbols = append(node.Symbols, Symbol{
						Name:    m[1],
						Kind:    kind,
						File:    rel,
						Line:    lineNum,
						Package: node.Package,
					})
				}
			}
			continue
		}

		// Method
		if m := methodRe.FindStringSubmatch(line); m != nil {
			name := m[2]
			if len(name) > 0 && name[0] >= 'A' && name[0] <= 'Z' {
				node.Symbols = append(node.Symbols, Symbol{
					Name:      name,
					Kind:      "method",
					File:      rel,
					Line:      lineNum,
					Package:   node.Package,
					Signature: fmt.Sprintf("(%s).%s(%s)", m[1], m[2], summarizeParams(m[3])),
				})
			}
			continue
		}

		// Function
		if m := funcRe.FindStringSubmatch(line); m != nil {
			name := m[1]
			if len(name) > 0 && name[0] >= 'A' && name[0] <= 'Z' {
				node.Symbols = append(node.Symbols, Symbol{
					Name:      name,
					Kind:      "func",
					File:      rel,
					Line:      lineNum,
					Package:   node.Package,
					Signature: fmt.Sprintf("%s(%s)", name, summarizeParams(m[2])),
				})
			}
			continue
		}

		// Type/Interface
		if m := typeRe.FindStringSubmatch(line); m != nil {
			name := m[1]
			if len(name) > 0 && name[0] >= 'A' && name[0] <= 'Z' {
				kind := "type"
				if m[2] == "interface" {
					kind = "interface"
				}
				node.Symbols = append(node.Symbols, Symbol{
					Name:    name,
					Kind:    kind,
					File:    rel,
					Line:    lineNum,
					Package: node.Package,
				})
			}
		}
	}

	return node, nil
}

// summarizeParams shortens parameter lists for readability.
func summarizeParams(params string) string {
	params = strings.TrimSpace(params)
	if params == "" {
		return ""
	}
	// Count params
	parts := strings.Split(params, ",")
	if len(parts) <= 3 {
		return params
	}
	return fmt.Sprintf("%s, ... +%d more", strings.TrimSpace(parts[0]), len(parts)-1)
}

func (rm *RepoMap) buildReverseEdges() {
	// Map package name → files
	pkgFiles := make(map[string][]string)
	for path, node := range rm.Files {
		pkgFiles[node.Package] = append(pkgFiles[node.Package], path)
	}

	for path, node := range rm.Files {
		for _, imp := range node.Imports {
			// Extract package name from import path
			parts := strings.Split(imp, "/")
			pkg := parts[len(parts)-1]
			for _, targetPath := range pkgFiles[pkg] {
				if targetPath != path {
					target := rm.Files[targetPath]
					target.ImportedBy = append(target.ImportedBy, path)
				}
			}
		}
	}
}

// rankFiles uses a simplified PageRank-style algorithm.
// Files with more importers rank higher.
func (rm *RepoMap) rankFiles() {
	// Initial rank: 1.0 per file
	for _, node := range rm.Files {
		node.Rank = 1.0
	}

	// 3 iterations of rank propagation
	for iter := 0; iter < 3; iter++ {
		newRanks := make(map[string]float64)
		for path, node := range rm.Files {
			rank := 0.15 // damping
			for _, importer := range node.ImportedBy {
				if impNode, ok := rm.Files[importer]; ok {
					outDegree := len(impNode.Imports)
					if outDegree == 0 {
						outDegree = 1
					}
					rank += 0.85 * impNode.Rank / float64(outDegree)
				}
			}
			// Bonus for symbol count (more symbols = more important)
			rank += float64(len(node.Symbols)) * 0.1
			newRanks[path] = rank
		}
		for path, rank := range newRanks {
			rm.Files[path].Rank = rank
		}
	}
}

func (rm *RepoMap) collectSymbols() {
	rm.Symbols = nil
	for _, node := range rm.Files {
		rm.Symbols = append(rm.Symbols, node.Symbols...)
	}
	sort.Slice(rm.Symbols, func(i, j int) bool {
		ri := rm.Files[rm.Symbols[i].File].Rank
		rj := rm.Files[rm.Symbols[j].File].Rank
		if ri != rj {
			return ri > rj
		}
		return rm.Symbols[i].Line < rm.Symbols[j].Line
	})
}
