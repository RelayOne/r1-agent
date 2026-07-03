// Package repomap generates a graph-ranked repository map for context injection.
// Inspired by Aider's repository map: a concise view of the codebase's most important
// classes, functions, and their relationships. This helps AI agents understand code
// structure without loading every file into context.
//
// Key features:
// - Real go/parser AST for accurate Go symbol extraction (not regex)
// - Graph ranking: files are nodes, imports AND call edges are weighted
// - Call-graph-weighted PageRank: files with more callers rank higher
// - Budget-aware: only emit symbols that fit in the token budget
// - Relevance scoring: rank by connectivity to the current task's files
package repomap

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/RelayOne/r1/internal/depgraph"
	"github.com/RelayOne/r1/internal/goast"
	"github.com/RelayOne/r1/internal/symindex"
)

// Symbol represents a code symbol (function, type, method, const).
type Symbol struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`               // func, type, method, const, var, interface
	File      string `json:"file"`               // relative path
	Line      int    `json:"line"`
	Package   string `json:"package"`
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

	// Call-graph-level connectivity (AST-derived)
	CalledBy int `json:"called_by"` // number of cross-file calls into this file
}

// RepoMap is a ranked view of the codebase.
//
// One *RepoMap is typically built at startup and shared across parallel
// scheduler workers, while Invalidate mutates it mid-run as tasks land
// files — so every exported entry point serializes on mu. Rendering takes
// the write lock too: RenderRelevantQuery temporarily mutates node ranks.
type RepoMap struct {
	mu      sync.RWMutex
	Root    string               `json:"root"`
	Files   map[string]*FileNode `json:"files"`
	Symbols []Symbol             `json:"symbols"` // all symbols, sorted by rank
}

// Build generates a repository map by scanning Go source files.
// Uses real AST parsing for accurate symbol extraction and call graph construction.
func Build(root string) (*RepoMap, error) {
	rm := &RepoMap{
		Root:  root,
		Files: make(map[string]*FileNode),
	}

	// Use goast for real AST-based analysis
	analysis, err := goast.AnalyzeDir(root)
	if err != nil {
		return nil, err
	}

	if analysis != nil {
		for _, fa := range analysis.Files {
			node := &FileNode{
				Path:    fa.Path,
				Package: fa.Package,
			}

			// Convert imports
			for _, imp := range fa.Imports {
				node.Imports = append(node.Imports, imp.ImportPath)
			}

			// Convert symbols — only public symbols for repo map
			for _, s := range fa.Symbols {
				if !s.Exported {
					continue
				}
				// Skip fields — too verbose for repo map
				if s.Kind == goast.KindField {
					continue
				}
				sym := Symbol{
					Name:    s.Name,
					Kind:    goastKindToString(s.Kind),
					File:    fa.Path,
					Line:    s.Line,
					Package: fa.Package,
				}
				if s.Kind == goast.KindFunction || s.Kind == goast.KindMethod {
					sym.Signature = s.Signature
				}
				node.Symbols = append(node.Symbols, sym)
			}

			rm.Files[fa.Path] = node
		}

		// Build call-graph-based cross-file connectivity
		rm.buildCallGraphEdges(analysis)
	}

	// Non-Go repos: goast yields no files. Fall back to language-agnostic
	// symbol/import extraction (symindex + depgraph) so the SAME ranking and
	// render pipeline runs for Python/TS/JS/etc. Only fires when goast produced
	// zero files, so any repo with >=1 parseable Go file is byte-identical.
	if len(rm.Files) == 0 {
		rm.buildFromSymindex(root)
	}

	// Build reverse import edges
	rm.buildReverseEdges()

	// Rank files (now uses both imports AND call graph)
	rm.rankFiles()

	// Collect all symbols sorted by file rank
	rm.collectSymbols()

	return rm, nil
}

// buildFromSymindex populates rm.Files for a non-Go repo using symindex
// (multi-language symbols: AST for Go, regex for Python/TS/JS/etc.) and depgraph
// (multi-language imports), so the unchanged buildReverseEdges/rankFiles/
// collectSymbols pipeline can rank and render non-Go repositories. Best-effort:
// on extraction error it leaves rm.Files as-is (Build still returns a valid,
// possibly-empty map and nil error, matching the prior non-Go behavior).
func (rm *RepoMap) buildFromSymindex(root string) {
	idx, err := symindex.Build(root)
	if err != nil {
		return
	}
	for _, f := range idx.Files() {
		node := rm.Files[f]
		if node == nil {
			node = &FileNode{Path: f, Package: pkgKey(f)}
			rm.Files[f] = node
		}
		for _, s := range idx.InFile(f) {
			if !s.Exported || s.Kind == symindex.KindField {
				continue
			}
			node.Symbols = append(node.Symbols, Symbol{
				Name:      s.Name,
				Kind:      symKindToString(s.Kind),
				File:      f,
				Line:      s.Line,
				Package:   node.Package,
				Signature: sigFor(s),
			})
		}
	}

	// Attach language-specific imports (best-effort) only to files symindex
	// already indexed; depgraph may include importless modules symindex skipped.
	if dg, derr := depgraph.Build(root, nil); derr == nil && dg != nil {
		for path, n := range dg.Nodes {
			if node, ok := rm.Files[path]; ok {
				node.Imports = n.Imports
			}
		}
	}
}

// pkgKey derives a non-empty package key for a relative path so reverse-edge /
// relevance matching can attempt to group files by directory. Best-effort for
// regex languages (dotted Python / relative TS imports rarely match exactly,
// so ranking falls back to the symbol-count bonus).
func pkgKey(rel string) string { return filepath.Base(filepath.Dir(rel)) }

// sigFor returns a symbol's signature only for func/method kinds (matching the
// goast path, which sets Signature only for functions/methods).
func sigFor(s symindex.Symbol) string {
	if s.Kind == symindex.KindFunction || s.Kind == symindex.KindMethod {
		return s.Signature
	}
	return ""
}

// symKindToString maps symindex symbol kinds to the repomap string kinds the
// Render switch expects (mirrors goastKindToString). repomap has no "class"
// string, so classes render as "type".
func symKindToString(k symindex.SymbolKind) string {
	switch k {
	case symindex.KindFunction:
		return "func"
	case symindex.KindMethod:
		return "method"
	case symindex.KindType:
		return "type"
	case symindex.KindClass:
		return "type"
	case symindex.KindInterface:
		return "interface"
	case symindex.KindVariable:
		return "var"
	case symindex.KindConstant:
		return "const"
	}
	return "var"
}

func goastKindToString(k goast.SymbolKind) string {
	switch k {
	case goast.KindFunction:
		return "func"
	case goast.KindMethod:
		return "method"
	case goast.KindStruct:
		return "type"
	case goast.KindType:
		return "type"
	case goast.KindInterface:
		return "interface"
	case goast.KindVariable:
		return "var"
	case goast.KindConstant:
		return "const"
	case goast.KindField:
		return "field"
	}
	return "?"
}

// buildCallGraphEdges counts cross-file call edges to weight the graph.
func (rm *RepoMap) buildCallGraphEdges(analysis *goast.Analysis) {
	// Map symbol names to their definition files
	symFile := make(map[string]string)
	for _, s := range analysis.AllSymbols {
		if s.Exported {
			symFile[s.Name] = s.File
			if s.Receiver != "" {
				symFile[s.Receiver+"."+s.Name] = s.File
			}
		}
	}

	// Count cross-file calls
	for _, c := range analysis.AllCalls {
		targetFile := ""
		if f, ok := symFile[c.Callee]; ok && f != c.File {
			targetFile = f
		}
		if targetFile != "" {
			if node, ok := rm.Files[targetFile]; ok {
				node.CalledBy++
			}
		}
	}
}

// parseGoFile is kept for backward compatibility with existing tests.
// Internally delegates to goast for real AST parsing.
func parseGoFile(path, rel string) (*FileNode, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	fa, err := goast.AnalyzeSource(src, rel)
	if err != nil {
		return nil, err
	}

	node := &FileNode{
		Path:    rel,
		Package: fa.Package,
	}

	for _, imp := range fa.Imports {
		node.Imports = append(node.Imports, imp.ImportPath)
	}

	for _, s := range fa.Symbols {
		if !s.Exported {
			continue
		}
		if s.Kind == goast.KindField {
			continue
		}
		sym := Symbol{
			Name:    s.Name,
			Kind:    goastKindToString(s.Kind),
			File:    rel,
			Line:    s.Line,
			Package: fa.Package,
		}
		if s.Kind == goast.KindFunction || s.Kind == goast.KindMethod {
			sym.Signature = s.Signature
		}
		node.Symbols = append(node.Symbols, sym)
	}

	return node, nil
}

// Render produces a human-readable map within a token budget.
// Each symbol line ~= 10 tokens. Budget 0 means unlimited.
func (rm *RepoMap) Render(budget int) string {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	return rm.renderLocked(budget)
}

// renderLocked is Render's body; callers must hold mu (read or write).
func (rm *RepoMap) renderLocked(budget int) string {
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
	return rm.RenderRelevantQuery(relevantFiles, nil, budget)
}

// RenderRelevantQuery is RenderRelevant conditioned on a task query:
// queryScores maps root-relative paths to relevance scores (e.g. TF-IDF
// cosine scores against the task text, ~0..1). Scored files get their rank
// multiplied by (1 + 2*score) on top of the file/neighbor boost, so the
// rendered map leads with the files the CURRENT task is about rather than
// the repo's static all-time ranking. Nil/empty scores render identically
// to RenderRelevant.
func (rm *RepoMap) RenderRelevantQuery(relevantFiles []string, queryScores map[string]float64, budget int) string {
	rm.mu.Lock()
	defer rm.mu.Unlock()

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
	for path, score := range queryScores {
		node, ok := rm.Files[path]
		if !ok || score <= 0 {
			continue
		}
		if _, saved := originalRanks[path]; !saved {
			originalRanks[path] = node.Rank
		}
		node.Rank *= 1.0 + 2.0*score
	}
	defer func() {
		for path, rank := range originalRanks {
			rm.Files[path].Rank = rank
		}
	}()

	rm.collectSymbols()
	return rm.renderLocked(budget)
}

// Invalidate re-reads one root-relative path from disk into the map so a
// startup-built map follows files the run itself changes. A vanished file
// is dropped; a changed Go file is re-parsed (an unparseable mid-edit file
// keeps its previous node). Per-file refresh is Go-only — non-Go trees are
// built by whole-tree symindex extraction, so a changed non-Go file is
// left as-is rather than half-merged. Reverse edges, ranks, and the symbol
// ordering are recomputed; CalledBy survives from the original whole-tree
// call-graph analysis (recomputing it needs a full re-analysis, and stale
// call counts only dampen ranking, never break rendering).
func (rm *RepoMap) Invalidate(relPath string) {
	if relPath == "" {
		return
	}
	rm.mu.Lock()
	defer rm.mu.Unlock()

	abs := filepath.Join(rm.Root, relPath)
	if _, err := os.Stat(abs); err != nil {
		if _, ok := rm.Files[relPath]; !ok {
			return
		}
		delete(rm.Files, relPath)
	} else {
		if !strings.HasSuffix(relPath, ".go") {
			return
		}
		node, err := parseGoFile(abs, relPath)
		if err != nil {
			return
		}
		if old, ok := rm.Files[relPath]; ok {
			node.CalledBy = old.CalledBy
		}
		rm.Files[relPath] = node
	}

	// buildReverseEdges appends, so reset reverse edges before rebuilding.
	for _, n := range rm.Files {
		n.ImportedBy = nil
	}
	rm.buildReverseEdges()
	rm.rankFiles()
	rm.collectSymbols()
}

// --- Internal ---

// summarizeParams shortens parameter lists for readability.
func summarizeParams(params string) string {
	params = strings.TrimSpace(params)
	if params == "" {
		return ""
	}
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

// rankFiles uses PageRank weighted by both imports AND call graph edges.
// Files with more importers and more cross-file callers rank higher.
func (rm *RepoMap) rankFiles() {
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
			// Symbol count bonus
			rank += float64(len(node.Symbols)) * 0.1
			// Call graph bonus: files that are called from other files are important
			rank += float64(node.CalledBy) * 0.15
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

