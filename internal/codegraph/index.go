// Package codegraph is the shared facade over the codebase-graph indexes:
// symbols (symindex), file dependencies (depgraph), TF-IDF content (tfidf),
// and bag-of-words semantic vectors (vecindex). Both the MCP codebase server
// and the native tool registry query code structure through this one
// implementation, so the two surfaces render byte-identical results.
//
// Query methods return model-facing text. A missing backing index is NOT an
// error — the method returns an informational string ("Symbol index not
// available") with a nil error so agent loops degrade gracefully instead of
// aborting the tool call.
//
// Staleness: callers report file writes via MarkDirty; the next query
// rebuilds the indexes from disk before answering. Rebuild failures are
// fail-open (previous indexes keep serving) and retried at most once per
// rebuildRetryInterval so a permanently broken tree cannot turn every query
// into a full-tree walk.
package codegraph

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/RelayOne/r1/internal/depgraph"
	"github.com/RelayOne/r1/internal/symindex"
	"github.com/RelayOne/r1/internal/tfidf"
	"github.com/RelayOne/r1/internal/vecindex"
)

// Messages returned when a backing index/graph is not loaded.
// Centralized so every surface emits a consistent, greppable string.
const (
	msgSymbolIndexUnavailable = "Symbol index not available"
	msgDepGraphUnavailable    = "Dependency graph not available"
)

// DefaultExtensions is the file-extension allowlist shared by every index
// builder (depgraph, tfidf) so all graph layers see the same corpus.
var DefaultExtensions = []string{".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".rs", ".java",
	".css", ".scss", ".html", ".vue", ".svelte", ".yaml", ".yml", ".json"}

// rebuildRetryInterval bounds how often a FAILING refresh is retried.
// Successful refreshes clear the dirty set, so they are naturally paced by
// writes; only failures need throttling to avoid a walk-the-tree retry storm.
const rebuildRetryInterval = 5 * time.Second

// defaultListLimit caps the per-list render size of the flat listing queries
// (GetDependencies, GetFileSymbols, ImpactAnalysis) when the caller passes a
// non-positive limit. A wide file — a "god" package imported everywhere, or a
// generated file with thousands of symbols — would otherwise render an
// unbounded wall of text (tens of thousands of tokens) that only the 200KB
// loop sanitizer would clip. Overrun is elided with a "... and N more" line,
// mirroring GetCallGraph.
const defaultListLimit = 50

// Index bundles the four codebase indexes behind one query surface.
// The zero value answers every query with "not available" text.
type Index struct {
	mu    sync.Mutex
	root  string
	sym   *symindex.Index
	dep   *depgraph.Graph
	tfidf *tfidf.Index
	vec   *vecindex.Index

	dirty    map[string]bool
	lastFail time.Time
}

// New wraps pre-built indexes. Any of the index arguments may be nil; the
// corresponding queries answer with an informational "not available" string.
func New(root string, sym *symindex.Index, dep *depgraph.Graph, tf *tfidf.Index, vec *vecindex.Index) *Index {
	return &Index{root: root, sym: sym, dep: dep, tfidf: tf, vec: vec}
}

// Build constructs all four indexes from the tree rooted at root.
func Build(root string) (*Index, error) {
	sym, err := symindex.Build(root)
	if err != nil {
		return nil, fmt.Errorf("build symbol index: %w", err)
	}

	dep, err := depgraph.Build(root, DefaultExtensions)
	if err != nil {
		return nil, fmt.Errorf("build dependency graph: %w", err)
	}

	tf, err := tfidf.Build(root, DefaultExtensions)
	if err != nil {
		return nil, fmt.Errorf("build tfidf index: %w", err)
	}

	// Vector index for semantic search using the symbol vocabulary.
	// Captures dimensional relationships between code concepts that
	// TF-IDF misses (e.g., "authentication" ≈ "login" ≈ "session").
	vec := BuildVectorIndex(sym, tf)

	return New(root, sym, dep, tf, vec), nil
}

// Root returns the tree root the indexes were built from ("" for wrapped
// index sets that never rebuild).
func (ix *Index) Root() string { return ix.root }

// Sym returns the current symbol index (may be nil).
func (ix *Index) Sym() *symindex.Index {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.sym
}

// Dep returns the current dependency graph (may be nil).
func (ix *Index) Dep() *depgraph.Graph {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.dep
}

// TFIDF returns the current TF-IDF content index (may be nil).
func (ix *Index) TFIDF() *tfidf.Index {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.tfidf
}

// Vec returns the current vector index (may be nil).
func (ix *Index) Vec() *vecindex.Index {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.vec
}

// MarkDirty records that a root-relative path changed on disk. The next
// query rebuilds the indexes before answering (no-op when root is unknown).
func (ix *Index) MarkDirty(relPath string) {
	if relPath == "" {
		return
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.dirty == nil {
		ix.dirty = make(map[string]bool)
	}
	ix.dirty[relPath] = true
}

// refreshLocked rebuilds all indexes when writes have been recorded since
// the last successful rebuild. Must be called with ix.mu held. Fail-open:
// on rebuild error the previous indexes keep serving and the dirty set is
// retained for a later retry (throttled by rebuildRetryInterval).
func (ix *Index) refreshLocked() {
	if len(ix.dirty) == 0 || ix.root == "" {
		return
	}
	if !ix.lastFail.IsZero() && time.Since(ix.lastFail) < rebuildRetryInterval {
		return
	}

	// A vanished root must not replace real indexes with empty ones:
	// symindex.Build swallows walk errors and would return an empty
	// index for a missing directory.
	if _, err := os.Stat(ix.root); err != nil {
		ix.lastFail = time.Now()
		fmt.Fprintf(os.Stderr, "codegraph: refresh root: %v\n", err)
		return
	}

	sym, err := symindex.Build(ix.root)
	if err != nil {
		ix.lastFail = time.Now()
		fmt.Fprintf(os.Stderr, "codegraph: refresh symbol index: %v\n", err)
		return
	}
	dep, err := depgraph.Build(ix.root, DefaultExtensions)
	if err != nil {
		ix.lastFail = time.Now()
		fmt.Fprintf(os.Stderr, "codegraph: refresh dependency graph: %v\n", err)
		return
	}
	tf, err := tfidf.Build(ix.root, DefaultExtensions)
	if err != nil {
		ix.lastFail = time.Now()
		fmt.Fprintf(os.Stderr, "codegraph: refresh tfidf index: %v\n", err)
		return
	}

	ix.sym, ix.dep, ix.tfidf = sym, dep, tf
	ix.vec = BuildVectorIndex(sym, tf)
	ix.dirty = nil
	ix.lastFail = time.Time{}
}

// BuildVectorIndex creates a vector index from the codebase vocabulary.
// Uses bag-of-words embedding over the symbol+term vocabulary, which
// captures richer relationships than pure TF-IDF matching.
func BuildVectorIndex(symIdx *symindex.Index, tfidfIdx *tfidf.Index) *vecindex.Index {
	// Build vocabulary from symbol names (the semantic backbone of the codebase)
	vocabSet := make(map[string]bool)
	if symIdx != nil {
		for _, sym := range symIdx.AllSymbols() {
			// Expand camelCase/PascalCase into components
			for _, word := range ExpandIdentifier(sym.Name) {
				w := strings.ToLower(word)
				if len(w) >= 3 {
					vocabSet[w] = true
				}
			}
		}
	}

	vocab := make([]string, 0, len(vocabSet))
	for w := range vocabSet {
		vocab = append(vocab, w)
	}
	sort.Strings(vocab)

	// Cap vocabulary at 2000 dimensions for performance
	if len(vocab) > 2000 {
		vocab = vocab[:2000]
	}

	embedFn := vecindex.BagOfWordsEmbed(vocab)
	idx := vecindex.New(vecindex.Config{
		Dimension: len(vocab),
		EmbedFunc: embedFn,
	})

	// Index each file's symbols as a document
	if symIdx != nil {
		for _, file := range symIdx.Files() {
			syms := symIdx.InFile(file)
			var content strings.Builder
			for _, sym := range syms {
				fmt.Fprintf(&content, "%s %s %s ", sym.Kind, sym.Name, sym.Parent)
			}
			if content.Len() > 0 {
				idx.AddText(file, content.String(), file)
			}
		}
	}

	return idx
}

// --- Query surface ---

// SearchSymbols finds symbols by name prefix, optionally filtered by kind.
func (ix *Index) SearchSymbols(query, kind string, limit int) (string, error) {
	if query == "" {
		return "", fmt.Errorf("query is required")
	}
	if limit <= 0 {
		limit = 20
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.refreshLocked()
	return ix.searchSymbolsLocked(query, kind, limit)
}

func (ix *Index) searchSymbolsLocked(query, kind string, limit int) (string, error) {
	if ix.sym == nil {
		return msgSymbolIndexUnavailable, nil
	}

	results := ix.sym.Search(query)

	filtered := make([]symindex.Symbol, 0, len(results))
	for _, sym := range results {
		if kind != "" && string(sym.Kind) != kind {
			continue
		}
		filtered = append(filtered, sym)
		if len(filtered) >= limit {
			break
		}
	}

	if len(filtered) == 0 {
		return fmt.Sprintf("No symbols found matching %q", query), nil
	}

	var sb strings.Builder
	for _, sym := range filtered {
		fmt.Fprintf(&sb, "%s %s (%s:%d)", sym.Kind, sym.Name, sym.File, sym.Line)
		if sym.Parent != "" {
			fmt.Fprintf(&sb, " [parent: %s]", sym.Parent)
		}
		if sym.Exported {
			sb.WriteString(" [exported]")
		}
		sb.WriteString("\n")
	}
	return sb.String(), nil
}

// GetDependencies renders a file's imports and reverse dependencies. limit
// caps each of the two lists independently; a non-positive limit falls back
// to defaultListLimit.
func (ix *Index) GetDependencies(file string, limit int) (string, error) {
	if file == "" {
		return "", fmt.Errorf("file is required")
	}
	if limit <= 0 {
		limit = defaultListLimit
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.refreshLocked()
	return ix.getDependenciesLocked(file, limit)
}

func (ix *Index) getDependenciesLocked(file string, limit int) (string, error) {
	if ix.dep == nil {
		return msgDepGraphUnavailable, nil
	}

	deps := ix.dep.Dependencies(file)
	dependents := ix.dep.Dependents(file)

	var sb strings.Builder
	fmt.Fprintf(&sb, "File: %s\n\n", file)

	fmt.Fprintf(&sb, "Imports (%d):\n", len(deps))
	for i, d := range deps {
		if i >= limit {
			fmt.Fprintf(&sb, "  ... and %d more\n", len(deps)-limit)
			break
		}
		fmt.Fprintf(&sb, "  %s\n", d)
	}

	fmt.Fprintf(&sb, "\nImported by (%d):\n", len(dependents))
	for i, d := range dependents {
		if i >= limit {
			fmt.Fprintf(&sb, "  ... and %d more\n", len(dependents)-limit)
			break
		}
		fmt.Fprintf(&sb, "  %s\n", d)
	}

	return sb.String(), nil
}

// SearchContent runs TF-IDF ranked content search across the corpus.
func (ix *Index) SearchContent(query string, limit int) (string, error) {
	if query == "" {
		return "", fmt.Errorf("query is required")
	}
	if limit <= 0 {
		limit = 10
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.refreshLocked()
	return ix.searchContentLocked(query, limit)
}

func (ix *Index) searchContentLocked(query string, limit int) (string, error) {
	if ix.tfidf == nil {
		return "Content search index not available", nil
	}

	results := ix.tfidf.SearchWithContext(query, limit)

	if len(results) == 0 {
		return fmt.Sprintf("No files found matching %q", query), nil
	}

	var sb strings.Builder
	for _, r := range results {
		fmt.Fprintf(&sb, "%.3f  %s", r.Score, r.Path)
		// Show matching terms so the model understands WHY this file matched
		if len(r.MatchingTerms) > 0 {
			fmt.Fprintf(&sb, "  (matched: %s)", strings.Join(r.MatchingTerms, ", "))
		}
		// Include exported symbols from the file for quick triage
		if ix.sym != nil {
			syms := ix.sym.InFile(r.Path)
			if len(syms) > 0 {
				var exported []string
				for _, sym := range syms {
					if sym.Exported && len(exported) < 5 {
						exported = append(exported, sym.Name)
					}
				}
				if len(exported) > 0 {
					fmt.Fprintf(&sb, "  [%s]", strings.Join(exported, ", "))
				}
			}
		}
		sb.WriteByte('\n')
	}
	return sb.String(), nil
}

// GetFileSymbols lists every symbol defined in one file. limit caps the
// listing; a non-positive limit falls back to defaultListLimit.
func (ix *Index) GetFileSymbols(file string, limit int) (string, error) {
	if file == "" {
		return "", fmt.Errorf("file is required")
	}
	if limit <= 0 {
		limit = defaultListLimit
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.refreshLocked()
	return ix.getFileSymbolsLocked(file, limit)
}

func (ix *Index) getFileSymbolsLocked(file string, limit int) (string, error) {
	if ix.sym == nil {
		return msgSymbolIndexUnavailable, nil
	}

	symbols := ix.sym.InFile(file)
	if len(symbols) == 0 {
		return fmt.Sprintf("No symbols found in %s", file), nil
	}

	var sb strings.Builder
	for i, sym := range symbols {
		if i >= limit {
			fmt.Fprintf(&sb, "  ... and %d more\n", len(symbols)-limit)
			break
		}
		fmt.Fprintf(&sb, "  L%-4d %s %s", sym.Line, sym.Kind, sym.Name)
		if sym.Exported {
			sb.WriteString(" [exported]")
		}
		sb.WriteString("\n")
	}
	return sb.String(), nil
}

// ImpactAnalysis renders the transitive dependent set of a file. limit caps
// the listing; a non-positive limit falls back to defaultListLimit.
func (ix *Index) ImpactAnalysis(file string, limit int) (string, error) {
	if file == "" {
		return "", fmt.Errorf("file is required")
	}
	if limit <= 0 {
		limit = defaultListLimit
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.refreshLocked()
	return ix.impactAnalysisLocked(file, limit)
}

func (ix *Index) impactAnalysisLocked(file string, limit int) (string, error) {
	if ix.dep == nil {
		return msgDepGraphUnavailable, nil
	}

	impact := ix.dep.ImpactSet(file)
	if len(impact) == 0 {
		return fmt.Sprintf("No files impacted by changes to %s", file), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Files impacted by changes to %s (%d):\n", file, len(impact))
	for i, f := range impact {
		if i >= limit {
			fmt.Fprintf(&sb, "  ... and %d more\n", len(impact)-limit)
			break
		}
		fmt.Fprintf(&sb, "  %s", f)
		// Show key exports so the model understands what each consumer provides
		if ix.sym != nil {
			syms := ix.sym.InFile(f)
			var exported []string
			for _, sym := range syms {
				if sym.Exported && len(exported) < 3 {
					exported = append(exported, sym.Name)
				}
			}
			if len(exported) > 0 {
				fmt.Fprintf(&sb, "  [%s]", strings.Join(exported, ", "))
			}
		}
		sb.WriteByte('\n')
	}
	return sb.String(), nil
}

// FindSymbolUsages renders where a symbol is defined and which files consume it.
func (ix *Index) FindSymbolUsages(symbol string, limit int) (string, error) {
	if symbol == "" {
		return "", fmt.Errorf("symbol is required")
	}
	if limit <= 0 {
		limit = 20
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.refreshLocked()
	return ix.findSymbolUsagesLocked(symbol, limit)
}

func (ix *Index) findSymbolUsagesLocked(symbol string, limit int) (string, error) {
	if ix.sym == nil {
		return msgSymbolIndexUnavailable, nil
	}

	// Find the definition(s) of this symbol
	defs := ix.sym.Lookup(symbol)

	var sb strings.Builder
	if len(defs) > 0 {
		sb.WriteString("Definitions:\n")
		for _, d := range defs {
			fmt.Fprintf(&sb, "  %s %s (%s:%d)", d.Kind, d.Name, d.File, d.Line)
			if d.Parent != "" {
				fmt.Fprintf(&sb, " [parent: %s]", d.Parent)
			}
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	// Build set of files where the symbol is defined to exclude them
	defFiles := make(map[string]bool)
	for _, d := range defs {
		defFiles[d.File] = true
	}

	// Search content for files that reference this symbol
	if ix.tfidf != nil {
		results := ix.tfidf.SearchWithContext(symbol, limit*2)
		sb.WriteString("Referenced in:\n")
		count := 0
		for _, r := range results {
			if defFiles[r.Path] {
				continue
			}
			fmt.Fprintf(&sb, "  %.3f  %s", r.Score, r.Path)
			// Show what this consumer file defines
			if syms := ix.sym.InFile(r.Path); len(syms) > 0 {
				var exported []string
				for _, sym := range syms {
					if sym.Exported && len(exported) < 3 {
						exported = append(exported, string(sym.Kind)+" "+sym.Name)
					}
				}
				if len(exported) > 0 {
					fmt.Fprintf(&sb, "  [defines: %s]", strings.Join(exported, ", "))
				}
			}
			// Show if this consumer has a direct dependency on a definition file
			if ix.dep != nil {
				deps := ix.dep.Dependencies(r.Path)
				for _, dep := range deps {
					if defFiles[dep] {
						fmt.Fprintf(&sb, " ←imports %s", filepath.Base(dep))
						break
					}
				}
			}
			sb.WriteByte('\n')
			count++
			if count >= limit {
				break
			}
		}
		if count == 0 {
			sb.WriteString("  (no consumer files found)\n")
		}
	} else {
		sb.WriteString("Content search index not available\n")
	}

	return sb.String(), nil
}

// TraceEntryPoints renders every dependency-graph root that can reach a file.
func (ix *Index) TraceEntryPoints(file string) (string, error) {
	if file == "" {
		return "", fmt.Errorf("file is required")
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.refreshLocked()
	return ix.traceEntryPointsLocked(file)
}

func (ix *Index) traceEntryPointsLocked(file string) (string, error) {
	if ix.dep == nil {
		return msgDepGraphUnavailable, nil
	}

	roots := ix.dep.Roots()
	rootSet := make(map[string]bool, len(roots))
	for _, r := range roots {
		rootSet[r] = true
	}

	// BFS backward from the target file, recording the parent at each step
	// so we can reconstruct paths from roots to the target.
	parent := map[string]string{file: ""}
	queue := []string{file}

	visited := map[string]bool{file: true}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		for _, dep := range ix.dep.Dependents(current) {
			if !visited[dep] {
				visited[dep] = true
				parent[dep] = current
				queue = append(queue, dep)
			}
		}
	}

	// Collect paths from roots that can reach this file
	type entryPath struct {
		root string
		path []string
	}
	var entries []entryPath
	for node := range visited {
		if rootSet[node] && node != file {
			// Reconstruct path from this root to the target
			var chain []string
			cur := node
			for cur != "" {
				chain = append(chain, cur)
				cur = parent[cur]
			}
			entries = append(entries, entryPath{root: node, path: chain})
		}
	}

	// Also check if the file itself is a root
	if rootSet[file] {
		entries = append(entries, entryPath{root: file, path: []string{file}})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].root < entries[j].root
	})

	if len(entries) == 0 {
		return fmt.Sprintf("No entry points found that can reach %s", file), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Entry points that reach %s (%d):\n\n", file, len(entries))
	for _, e := range entries {
		// Classify the entry point by name heuristics
		surface := ClassifyEntryPoint(e.root)
		fmt.Fprintf(&sb, "  %s", e.root)
		if surface != "" {
			fmt.Fprintf(&sb, " [%s]", surface)
		}
		// Show key symbols in the entry point
		if ix.sym != nil {
			syms := ix.sym.InFile(e.root)
			var names []string
			for _, sym := range syms {
				if sym.Exported && len(names) < 3 {
					names = append(names, sym.Name)
				}
			}
			if len(names) > 0 {
				fmt.Fprintf(&sb, " {%s}", strings.Join(names, ", "))
			}
		}
		sb.WriteString("\n")
		// Show the dependency chain
		if len(e.path) > 1 {
			sb.WriteString("    chain: ")
			for i, p := range e.path {
				if i > 0 {
					sb.WriteString(" → ")
				}
				sb.WriteString(filepath.Base(p))
			}
			sb.WriteString("\n")
		}
	}
	return sb.String(), nil
}

// SemanticSearch runs vector search by meaning, falling back to TF-IDF
// content search when the vector index is empty.
func (ix *Index) SemanticSearch(query string, limit int) (string, error) {
	if query == "" {
		return "", fmt.Errorf("query is required")
	}
	if limit <= 0 {
		limit = 10
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.refreshLocked()
	return ix.semanticSearchLocked(query, limit)
}

func (ix *Index) semanticSearchLocked(query string, limit int) (string, error) {
	if ix.vec == nil || ix.vec.Count() == 0 {
		// Fall back to TF-IDF if vector index not available
		if ix.tfidf != nil {
			return ix.searchContentLocked(query, limit)
		}
		return "Semantic search index not available", nil
	}

	results, err := ix.vec.SearchText(query, limit)
	if err != nil {
		return "", fmt.Errorf("semantic search: %w", err)
	}

	if len(results) == 0 {
		return fmt.Sprintf("No files found semantically matching %q", query), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Semantic matches for %q (%d results):\n", query, len(results))
	for _, r := range results {
		fmt.Fprintf(&sb, "  %.3f  %s", r.Score, r.Document.Path)
		// Show what this file defines
		if ix.sym != nil {
			syms := ix.sym.InFile(r.Document.Path)
			var exported []string
			for _, sym := range syms {
				if sym.Exported && len(exported) < 4 {
					exported = append(exported, string(sym.Kind)+" "+sym.Name)
				}
			}
			if len(exported) > 0 {
				fmt.Fprintf(&sb, "  [%s]", strings.Join(exported, ", "))
			}
		}
		sb.WriteByte('\n')
	}
	return sb.String(), nil
}

// GetCallGraph renders the AST-parsed callers/callees of a symbol.
func (ix *Index) GetCallGraph(symbol, direction string, limit int) (string, error) {
	if symbol == "" {
		return "", fmt.Errorf("symbol is required")
	}
	if direction == "" {
		direction = "both"
	}
	if limit <= 0 {
		limit = 20
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.refreshLocked()
	return ix.getCallGraphLocked(symbol, direction, limit)
}

func (ix *Index) getCallGraphLocked(symbol, direction string, limit int) (string, error) {
	if ix.sym == nil {
		return msgSymbolIndexUnavailable, nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "# Call Graph: %s\n\n", symbol)

	if direction == "callers" || direction == "both" {
		callers := ix.sym.Callers(symbol)
		fmt.Fprintf(&sb, "## Callers (%d)\n", len(callers))
		for i, c := range callers {
			if i >= limit {
				fmt.Fprintf(&sb, "... and %d more\n", len(callers)-limit)
				break
			}
			fmt.Fprintf(&sb, "- %s (%s:%d)\n", c.Caller, c.File, c.Line)
		}
		if len(callers) == 0 {
			sb.WriteString("No callers found (may be an entry point or external caller)\n")
		}
		sb.WriteString("\n")
	}

	if direction == "callees" || direction == "both" {
		callees := ix.sym.Callees(symbol)
		fmt.Fprintf(&sb, "## Callees (%d)\n", len(callees))
		for i, c := range callees {
			if i >= limit {
				fmt.Fprintf(&sb, "... and %d more\n", len(callees)-limit)
				break
			}
			loc := ""
			if c.CalleePkg != "" {
				loc = c.CalleePkg + "."
			}
			fmt.Fprintf(&sb, "- %s%s (%s:%d)\n", loc, c.Callee, c.File, c.Line)
		}
		if len(callees) == 0 {
			sb.WriteString("No callees found (leaf function)\n")
		}
		sb.WriteString("\n")
	}

	return sb.String(), nil
}

// GetInterfaceImplementations renders the types whose method sets satisfy an interface.
func (ix *Index) GetInterfaceImplementations(ifaceName string) (string, error) {
	if ifaceName == "" {
		return "", fmt.Errorf("interface is required")
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.refreshLocked()
	return ix.getInterfaceImplementationsLocked(ifaceName)
}

func (ix *Index) getInterfaceImplementationsLocked(ifaceName string) (string, error) {
	if ix.sym == nil {
		return msgSymbolIndexUnavailable, nil
	}

	implementors := ix.sym.Implementors(ifaceName)

	var sb strings.Builder
	fmt.Fprintf(&sb, "# Types implementing %s\n\n", ifaceName)

	if len(implementors) == 0 {
		sb.WriteString("No implementations found.\n")
		sb.WriteString("Note: Interface satisfaction is checked via AST method-set analysis for Go files.\n")
	} else {
		for _, typ := range implementors {
			// Find the type's location
			syms := ix.sym.Lookup(typ)
			for _, sym := range syms {
				if sym.Kind == symindex.KindType || sym.Kind == symindex.KindInterface {
					fmt.Fprintf(&sb, "- %s (%s:%d)\n", typ, sym.File, sym.Line)
					break
				}
			}
		}
	}

	return sb.String(), nil
}

// GetSymbolDetail renders signature, doc, and call counts for a symbol.
func (ix *Index) GetSymbolDetail(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("symbol is required")
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.refreshLocked()
	return ix.getSymbolDetailLocked(name)
}

func (ix *Index) getSymbolDetailLocked(name string) (string, error) {
	if ix.sym == nil {
		return msgSymbolIndexUnavailable, nil
	}

	syms := ix.sym.Lookup(name)
	if len(syms) == 0 {
		return fmt.Sprintf("Symbol %q not found", name), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "# Symbol: %s (%d definitions)\n\n", name, len(syms))

	for _, sym := range syms {
		fmt.Fprintf(&sb, "## %s %s\n", sym.Kind, sym.Name)
		fmt.Fprintf(&sb, "- **File:** %s:%d", sym.File, sym.Line)
		if sym.EndLine > 0 {
			fmt.Fprintf(&sb, "-%d", sym.EndLine)
		}
		sb.WriteString("\n")
		fmt.Fprintf(&sb, "- **Exported:** %v\n", sym.Exported)
		if sym.Parent != "" {
			fmt.Fprintf(&sb, "- **Receiver/Parent:** %s\n", sym.Parent)
		}
		if sym.Signature != "" {
			fmt.Fprintf(&sb, "- **Signature:** `%s`\n", sym.Signature)
		}
		if sym.TypeName != "" {
			fmt.Fprintf(&sb, "- **Type:** %s\n", sym.TypeName)
		}
		if sym.Doc != "" {
			fmt.Fprintf(&sb, "- **Doc:** %s\n", strings.TrimSpace(sym.Doc))
		}

		// Show callers/callees if available
		qualified := sym.Name
		if sym.Parent != "" {
			qualified = sym.Parent + "." + sym.Name
		}
		callers := ix.sym.Callers(sym.Name)
		callees := ix.sym.Callees(qualified)
		if len(callers) > 0 {
			fmt.Fprintf(&sb, "- **Called by:** %d callers\n", len(callers))
		}
		if len(callees) > 0 {
			fmt.Fprintf(&sb, "- **Calls:** %d callees\n", len(callees))
		}

		sb.WriteString("\n")
	}

	return sb.String(), nil
}

// ClassifyEntryPoint guesses the surface type from filename patterns.
func ClassifyEntryPoint(path string) string {
	base := strings.ToLower(filepath.Base(path))
	dir := strings.ToLower(filepath.Dir(path))

	// Check most specific directory patterns first, then fall back to name heuristics.
	switch {
	case strings.Contains(base, "mcp") || strings.Contains(dir, "mcp"):
		return "MCP"
	case strings.Contains(dir, "mobile") || strings.Contains(dir, "ios") ||
		strings.Contains(dir, "android"):
		return "Mobile"
	case strings.Contains(dir, "desktop") || strings.Contains(dir, "electron"):
		return "Desktop"
	case strings.Contains(base, "app.tsx") || strings.Contains(base, "app.jsx") ||
		strings.Contains(base, "index.html") || strings.Contains(dir, "pages") ||
		strings.Contains(dir, "components"):
		return "Web"
	case strings.Contains(base, "server") || strings.Contains(base, "handler") ||
		strings.Contains(base, "route") || strings.Contains(dir, "api"):
		return "API"
	case strings.Contains(base, "main") || strings.HasSuffix(base, "_cmd.go") || strings.Contains(dir, "cmd"):
		return "CLI"
	default:
		return ""
	}
}

// ExpandIdentifier splits camelCase/PascalCase/snake_case into words.
func ExpandIdentifier(name string) []string {
	// Split on underscores first
	parts := strings.Split(name, "_")
	var words []string
	for _, part := range parts {
		if part == "" {
			continue
		}
		// Split camelCase: insert boundary at lowercase→uppercase transition
		var current strings.Builder
		for i, r := range part {
			if i > 0 && r >= 'A' && r <= 'Z' {
				prev := rune(part[i-1])
				if prev >= 'a' && prev <= 'z' {
					if current.Len() > 0 {
						words = append(words, current.String())
						current.Reset()
					}
				}
			}
			current.WriteRune(r)
		}
		if current.Len() > 0 {
			words = append(words, current.String())
		}
	}
	return words
}
