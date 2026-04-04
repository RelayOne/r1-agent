// Package depgraph extracts import/dependency graphs from source code.
// Inspired by Aider's repo-map and claw-code's dependency analysis:
//
// Understanding the dependency structure enables:
// - Targeted context injection (only include files the agent actually needs)
// - Impact analysis (which files are affected by a change)
// - Build order optimization
// - Circular dependency detection
//
// Supports Go, Python, TypeScript, and Rust import patterns.
package depgraph

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Node is a file/module in the dependency graph.
type Node struct {
	Path    string   `json:"path"`
	Imports []string `json:"imports"`
}

// Edge is a directed dependency.
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Graph holds the full dependency structure.
type Graph struct {
	Nodes map[string]*Node `json:"nodes"`
	Edges []Edge           `json:"edges"`
}

// Build scans a directory and constructs the dependency graph.
func Build(root string, extensions []string) (*Graph, error) {
	g := &Graph{
		Nodes: make(map[string]*Node),
	}

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}

		// Skip vendor, node_modules, etc.
		rel, _ := filepath.Rel(root, path)
		if shouldSkip(rel) {
			return nil
		}

		ext := filepath.Ext(path)
		if !matchExt(ext, extensions) {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil // skip unreadable files
		}

		imports := extractImports(string(data), ext)
		node := &Node{
			Path:    rel,
			Imports: imports,
		}
		g.Nodes[rel] = node

		for _, imp := range imports {
			g.Edges = append(g.Edges, Edge{From: rel, To: imp})
		}
		return nil
	})

	return g, err
}

// Dependents returns all files that import the given path (reverse deps).
func (g *Graph) Dependents(path string) []string {
	var deps []string
	for _, e := range g.Edges {
		if e.To == path || strings.HasSuffix(e.To, "/"+filepath.Base(path)) {
			deps = append(deps, e.From)
		}
	}
	sort.Strings(deps)
	return deps
}

// Dependencies returns all files that the given path imports.
func (g *Graph) Dependencies(path string) []string {
	node, ok := g.Nodes[path]
	if !ok {
		return nil
	}
	result := make([]string, len(node.Imports))
	copy(result, node.Imports)
	sort.Strings(result)
	return result
}

// ImpactSet returns all files transitively affected by changing the given file.
// Uses BFS to find the full transitive closure of reverse dependencies.
func (g *Graph) ImpactSet(path string) []string {
	// Build reverse adjacency
	rev := make(map[string][]string)
	for _, e := range g.Edges {
		rev[e.To] = append(rev[e.To], e.From)
		// Also match by basename for cross-package imports
		base := filepath.Base(e.To)
		rev[base] = append(rev[base], e.From)
	}

	visited := make(map[string]bool)
	queue := []string{path}
	visited[path] = true

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		for _, dep := range rev[current] {
			if !visited[dep] {
				visited[dep] = true
				queue = append(queue, dep)
			}
		}
		// Also check by basename
		for _, dep := range rev[filepath.Base(current)] {
			if !visited[dep] {
				visited[dep] = true
				queue = append(queue, dep)
			}
		}
	}

	delete(visited, path) // exclude the source file itself
	var result []string
	for f := range visited {
		result = append(result, f)
	}
	sort.Strings(result)
	return result
}

// DetectCycles finds circular dependencies using DFS.
func (g *Graph) DetectCycles() [][]string {
	// Build adjacency list
	adj := make(map[string][]string)
	for _, e := range g.Edges {
		adj[e.From] = append(adj[e.From], e.To)
	}

	var cycles [][]string
	visited := make(map[string]int) // 0=unvisited, 1=in-stack, 2=done
	var stack []string

	var dfs func(node string)
	dfs = func(node string) {
		if visited[node] == 2 {
			return
		}
		if visited[node] == 1 {
			// Found cycle - extract it
			cycle := []string{node}
			for i := len(stack) - 1; i >= 0; i-- {
				if stack[i] == node {
					break
				}
				cycle = append(cycle, stack[i])
			}
			// Reverse
			for l, r := 0, len(cycle)-1; l < r; l, r = l+1, r-1 {
				cycle[l], cycle[r] = cycle[r], cycle[l]
			}
			cycles = append(cycles, cycle)
			return
		}

		visited[node] = 1
		stack = append(stack, node)

		for _, next := range adj[node] {
			// Only follow edges to nodes we know about
			if _, ok := g.Nodes[next]; ok {
				dfs(next)
			}
		}

		stack = stack[:len(stack)-1]
		visited[node] = 2
	}

	for node := range g.Nodes {
		dfs(node)
	}

	return cycles
}

// Roots returns nodes with no incoming edges (entry points).
func (g *Graph) Roots() []string {
	hasIncoming := make(map[string]bool)
	for _, e := range g.Edges {
		if _, ok := g.Nodes[e.To]; ok {
			hasIncoming[e.To] = true
		}
	}

	var roots []string
	for path := range g.Nodes {
		if !hasIncoming[path] {
			roots = append(roots, path)
		}
	}
	sort.Strings(roots)
	return roots
}

// Leaves returns nodes with no outgoing edges (no imports).
func (g *Graph) Leaves() []string {
	var leaves []string
	for path, node := range g.Nodes {
		if len(node.Imports) == 0 {
			leaves = append(leaves, path)
		}
	}
	sort.Strings(leaves)
	return leaves
}

// Stats returns graph statistics.
func (g *Graph) Stats() string {
	return fmt.Sprintf("%d nodes, %d edges, %d roots, %d leaves",
		len(g.Nodes), len(g.Edges), len(g.Roots()), len(g.Leaves()))
}

// --- Import extraction ---

var (
	goImportSingle  = regexp.MustCompile(`import\s+"([^"]+)"`)
	goImportBlock   = regexp.MustCompile(`import\s*\(\s*((?:[^)]+))\)`)
	goImportLine    = regexp.MustCompile(`"([^"]+)"`)
	pyImport        = regexp.MustCompile(`^(?:from\s+(\S+)\s+)?import\s+(.+)`)
	tsImport        = regexp.MustCompile(`(?:import|from)\s+['"]([^'"]+)['"]`)
	tsRequire       = regexp.MustCompile(`require\s*\(\s*['"]([^'"]+)['"]\s*\)`)
	rustUse         = regexp.MustCompile(`^use\s+(\w+(?:::\w+)*)`)
	rustMod         = regexp.MustCompile(`^mod\s+(\w+)\s*;`)
)

func extractImports(content, ext string) []string {
	switch ext {
	case ".go":
		return extractGoImports(content)
	case ".py":
		return extractPyImports(content)
	case ".ts", ".tsx", ".js", ".jsx":
		return extractTSImports(content)
	case ".rs":
		return extractRustImports(content)
	default:
		return nil
	}
}

func extractGoImports(content string) []string {
	var imports []string

	// Single imports
	for _, m := range goImportSingle.FindAllStringSubmatch(content, -1) {
		imports = append(imports, m[1])
	}

	// Block imports
	for _, m := range goImportBlock.FindAllStringSubmatch(content, -1) {
		for _, line := range goImportLine.FindAllStringSubmatch(m[1], -1) {
			imports = append(imports, line[1])
		}
	}

	return dedup(imports)
}

func extractPyImports(content string) []string {
	var imports []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if m := pyImport.FindStringSubmatch(line); m != nil {
			if m[1] != "" {
				imports = append(imports, m[1])
			} else {
				parts := strings.Split(m[2], ",")
				for _, p := range parts {
					p = strings.TrimSpace(p)
					if p != "" {
						imports = append(imports, strings.Split(p, " ")[0])
					}
				}
			}
		}
	}
	return dedup(imports)
}

func extractTSImports(content string) []string {
	var imports []string
	for _, m := range tsImport.FindAllStringSubmatch(content, -1) {
		imports = append(imports, m[1])
	}
	for _, m := range tsRequire.FindAllStringSubmatch(content, -1) {
		imports = append(imports, m[1])
	}
	return dedup(imports)
}

func extractRustImports(content string) []string {
	var imports []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if m := rustUse.FindStringSubmatch(line); m != nil {
			imports = append(imports, m[1])
		}
		if m := rustMod.FindStringSubmatch(line); m != nil {
			imports = append(imports, m[1])
		}
	}
	return dedup(imports)
}

func shouldSkip(path string) bool {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for _, p := range parts {
		if p == "vendor" || p == "node_modules" || p == ".git" || p == "target" || p == "__pycache__" {
			return true
		}
	}
	return false
}

func matchExt(ext string, allowed []string) bool {
	if len(allowed) == 0 {
		return true // allow all
	}
	for _, a := range allowed {
		if ext == a {
			return true
		}
	}
	return false
}

func dedup(s []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			result = append(result, v)
		}
	}
	return result
}
