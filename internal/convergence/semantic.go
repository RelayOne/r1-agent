// Package convergence - semantic validation layer.
//
// This file implements semantic code analysis that goes beyond regex pattern
// matching. Instead of searching for TODO comments (which only catches honest
// incompleteness), it maps intent against actual code flow to find the
// dangerous gaps: code that looks complete but doesn't work.
//
// The key insight: most incomplete code has NO syntactic marker. Only mapping
// the intention context against real code flow reveals the gaps.
//
// For Go files, uses real go/parser AST for:
//   - Call graph reachability — are symbols reachable from entry points?
//   - Interface satisfaction — do types implement required interfaces?
//   - Typed symbol extraction — accurate function signatures
//
// For non-Go files, falls back to regex-based extraction.
//
// Three analyses are performed:
//  1. Symbol reachability — are new symbols actually called from entry points?
//  2. Criteria mapping — does the semantic diff match the acceptance criteria?
//  3. Cross-file wiring — do new types/functions connect to existing infrastructure?
package convergence

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/RelayOne/r1-agent/internal/goast"
)

// symbolKind classifies extracted code symbols.
type symbolKind string

const (
	skFunction  symbolKind = "function"
	skMethod    symbolKind = "method"
	skType      symbolKind = "type"
	skInterface symbolKind = "interface"
	skClass     symbolKind = "class"
	skVariable  symbolKind = "variable"
	skExport    symbolKind = "export"
)

// codeSymbol is a symbol extracted from source code.
type codeSymbol struct {
	Name     string
	Kind     symbolKind
	File     string
	Line     int
	Exported bool
}

// langExtractor holds patterns for extracting symbols from a language.
type langExtractor struct {
	Extensions []string
	Patterns   []struct {
		Kind  symbolKind
		Regex *regexp.Regexp
		Name  int // capture group index for name
	}
}

var langExtractors = []langExtractor{
	{
		Extensions: []string{".go"},
		Patterns: []struct {
			Kind  symbolKind
			Regex *regexp.Regexp
			Name  int
		}{
			{skFunction, regexp.MustCompile(`^func\s+(\w+)\s*\(`), 1},
			{skMethod, regexp.MustCompile(`^func\s+\(\w+\s+\*?(\w+)\)\s+(\w+)\s*\(`), 2},
			{skType, regexp.MustCompile(`^type\s+(\w+)\s+struct\b`), 1},
			{skInterface, regexp.MustCompile(`^type\s+(\w+)\s+interface\b`), 1},
		},
	},
	{
		Extensions: []string{".ts", ".tsx", ".js", ".jsx"},
		Patterns: []struct {
			Kind  symbolKind
			Regex *regexp.Regexp
			Name  int
		}{
			{skFunction, regexp.MustCompile(`^(?:export\s+)?(?:async\s+)?function\s+(\w+)`), 1},
			{skClass, regexp.MustCompile(`^(?:export\s+)?class\s+(\w+)`), 1},
			{skInterface, regexp.MustCompile(`^(?:export\s+)?interface\s+(\w+)`), 1},
			{skType, regexp.MustCompile(`^(?:export\s+)?type\s+(\w+)\s*[={]`), 1},
			{skExport, regexp.MustCompile(`^export\s+(?:default\s+)?(?:const|let|var|function|class)\s+(\w+)`), 1},
		},
	},
	{
		Extensions: []string{".py"},
		Patterns: []struct {
			Kind  symbolKind
			Regex *regexp.Regexp
			Name  int
		}{
			{skFunction, regexp.MustCompile(`^def\s+(\w+)\s*\(`), 1},
			{skClass, regexp.MustCompile(`^class\s+(\w+)`), 1},
		},
	},
	{
		Extensions: []string{".rs"},
		Patterns: []struct {
			Kind  symbolKind
			Regex *regexp.Regexp
			Name  int
		}{
			{skFunction, regexp.MustCompile(`^(?:pub\s+)?fn\s+(\w+)`), 1},
			{skType, regexp.MustCompile(`^(?:pub\s+)?struct\s+(\w+)`), 1},
			{skInterface, regexp.MustCompile(`^(?:pub\s+)?trait\s+(\w+)`), 1},
		},
	},
	{
		Extensions: []string{".java"},
		Patterns: []struct {
			Kind  symbolKind
			Regex *regexp.Regexp
			Name  int
		}{
			{skClass, regexp.MustCompile(`^(?:public|private|protected)?\s*(?:abstract\s+)?class\s+(\w+)`), 1},
			{skInterface, regexp.MustCompile(`^(?:public\s+)?interface\s+(\w+)`), 1},
			{skMethod, regexp.MustCompile(`^\s+(?:public|private|protected)\s+\w+\s+(\w+)\s*\(`), 1},
		},
	},
}

// extractFileSymbols extracts symbols from a single file's content.
// For Go files, uses real AST parsing via goast package.
// For other languages, falls back to regex-based extraction.
func extractFileSymbols(path string, content []byte) []codeSymbol {
	ext := filepath.Ext(path)

	// Go files: use real AST
	if ext == ".go" {
		return extractGoFileSymbols(path, content)
	}

	// Non-Go: regex fallback
	var extractor *langExtractor
	for i := range langExtractors {
		for _, e := range langExtractors[i].Extensions {
			if e == ext {
				extractor = &langExtractors[i]
				break
			}
		}
		if extractor != nil {
			break
		}
	}
	if extractor == nil {
		return nil
	}

	lines := strings.Split(string(content), "\n")
	var symbols []codeSymbol
	for lineNum, line := range lines {
		for _, pat := range extractor.Patterns {
			m := pat.Regex.FindStringSubmatch(line)
			if m == nil || pat.Name >= len(m) {
				continue
			}
			name := m[pat.Name]
			exported := false
			if len(name) > 0 {
				switch ext {
				case ".ts", ".tsx", ".js", ".jsx":
					exported = strings.HasPrefix(strings.TrimSpace(line), "export")
				case ".py":
					exported = !strings.HasPrefix(name, "_")
				default:
					exported = true
				}
			}
			symbols = append(symbols, codeSymbol{
				Name:     name,
				Kind:     pat.Kind,
				File:     path,
				Line:     lineNum + 1,
				Exported: exported,
			})
		}
	}
	return symbols
}

// extractGoFileSymbols uses goast for real AST-based extraction.
func extractGoFileSymbols(path string, content []byte) []codeSymbol {
	fa, err := goast.AnalyzeSource(content, path)
	if err != nil {
		// Fall back to regex if AST parse fails
		return extractGoFileSymbolsRegex(path, content)
	}

	symbols := make([]codeSymbol, 0, len(fa.Symbols))
	for _, s := range fa.Symbols {
		kind := goastKindToSymKind(s.Kind)
		symbols = append(symbols, codeSymbol{
			Name:     s.Name,
			Kind:     kind,
			File:     path,
			Line:     s.Line,
			Exported: s.Exported,
		})
	}
	return symbols
}

func goastKindToSymKind(k goast.SymbolKind) symbolKind {
	switch k {
	case goast.KindFunction:
		return skFunction
	case goast.KindMethod:
		return skMethod
	case goast.KindStruct, goast.KindType:
		return skType
	case goast.KindInterface:
		return skInterface
	case goast.KindVariable, goast.KindConstant:
		return skVariable
	case goast.KindField:
		return skVariable
	}
	return skFunction
}

// extractGoFileSymbolsRegex is the legacy fallback for Go files when AST fails.
func extractGoFileSymbolsRegex(path string, content []byte) []codeSymbol {
	lines := strings.Split(string(content), "\n")
	var symbols []codeSymbol
	goLang := &langExtractors[0] // Go is first
	for lineNum, line := range lines {
		for _, pat := range goLang.Patterns {
			m := pat.Regex.FindStringSubmatch(line)
			if m == nil || pat.Name >= len(m) {
				continue
			}
			name := m[pat.Name]
			exported := len(name) > 0 && name[0] >= 'A' && name[0] <= 'Z'
			symbols = append(symbols, codeSymbol{
				Name: name, Kind: pat.Kind, File: path,
				Line: lineNum + 1, Exported: exported,
			})
		}
	}
	return symbols
}

// referenceCount counts how many times a symbol name appears across all files
// (excluding its own definition file). This is a proxy for "is it wired in?"
func referenceCount(name string, files []FileInput, defFile string) int {
	count := 0
	for _, f := range files {
		if f.Path == defFile {
			continue
		}
		// Use word boundary matching to avoid false positives
		content := string(f.Content)
		re := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`)
		count += len(re.FindAllString(content, -1))
	}
	return count
}

// SemanticAnalysis performs deep code analysis on the provided files.
// It extracts symbols, builds a reference graph, and checks for unreachable
// code, unwired types, and unmapped criteria.
//
// For Go files, uses real AST call graph for reachability analysis.
// For non-Go files, falls back to regex-based reference counting.
//
// Unlike regex rules which operate on individual files, semantic analysis
// operates across the entire file set to find cross-file gaps.
func SemanticAnalysis(files []FileInput, criteria []string) []Finding {
	if len(files) == 0 {
		return nil
	}

	var findings []Finding

	// Phase 1: Extract all symbols from all files
	allSymbols := make(map[string][]codeSymbol) // file -> symbols
	var allSymList []codeSymbol
	for _, f := range files {
		syms := extractFileSymbols(f.Path, f.Content)
		allSymbols[f.Path] = syms
		allSymList = append(allSymList, syms...)
	}

	// Phase 1b: Build goast Analysis for Go files (real call graph)
	var goAnalysis *goast.Analysis
	hasGoFiles := false
	for _, f := range files {
		if filepath.Ext(f.Path) == ".go" && !isTestFile(f.Path) {
			hasGoFiles = true
			break
		}
	}
	if hasGoFiles {
		goAnalysis = buildGoAnalysisFromFiles(files)
	}

	// Phase 2: Check exported symbol reachability
	// For Go files with AST: use call graph reachability from entry points
	// For other files: fall back to reference counting
	if goAnalysis != nil {
		findings = append(findings, checkReachabilityAST(goAnalysis, files)...)
	} else {
		findings = append(findings, checkReachabilityRegex(allSymList, files)...)
	}

	// Phase 3: Check type wiring
	// New types/classes/interfaces should be instantiated somewhere
	for _, sym := range allSymList {
		if sym.Kind != skType && sym.Kind != skClass && sym.Kind != skInterface {
			continue
		}
		if !sym.Exported {
			continue
		}
		if isTestFile(sym.File) {
			continue
		}
		if len(sym.Name) <= 2 {
			continue
		}

		totalRefs := 0
		for _, f := range files {
			content := string(f.Content)
			re := regexp.MustCompile(`\b` + regexp.QuoteMeta(sym.Name) + `\b`)
			matches := re.FindAllString(content, -1)
			if f.Path == sym.File {
				totalRefs += len(matches) - 1
			} else {
				totalRefs += len(matches)
			}
		}

		if totalRefs == 0 {
			findings = append(findings, Finding{
				RuleID:   "cross-file-wiring",
				Category: CatConsistency,
				Severity: SevMajor,
				File:     sym.File,
				Line:     sym.Line,
				Description: fmt.Sprintf("Type %q defined but never instantiated or referenced — not wired into the system",
					sym.Name),
				Suggestion: fmt.Sprintf("Use %s somewhere: create instances, pass it as a parameter, or return it from a function", sym.Name),
				Evidence:   fmt.Sprintf("type %s (0 usage references)", sym.Name),
			})
		}
	}

	// Phase 4: Semantic criteria mapping
	if len(criteria) > 0 {
		findings = append(findings, mapCriteriaToCode(criteria, files, allSymList)...)
	}

	return findings
}

// buildGoAnalysisFromFiles constructs a goast.Analysis from FileInput content.
func buildGoAnalysisFromFiles(files []FileInput) *goast.Analysis {
	a := &goast.Analysis{}
	for _, f := range files {
		if filepath.Ext(f.Path) != ".go" {
			continue
		}
		if isTestFile(f.Path) {
			continue
		}
		fa, err := goast.AnalyzeSource(f.Content, f.Path)
		if err != nil {
			continue
		}
		a.Files = append(a.Files, fa)
		a.AllSymbols = append(a.AllSymbols, fa.Symbols...)
		a.AllCalls = append(a.AllCalls, fa.Calls...)
	}
	if len(a.Files) == 0 {
		return nil
	}
	return a
}

// checkReachabilityAST uses goast call graph to find truly unreachable symbols.
// More accurate than regex reference counting because it traces actual call edges.
func checkReachabilityAST(a *goast.Analysis, files []FileInput) []Finding {
	dead := a.DeadSymbols()
	findings := make([]Finding, 0, len(dead))

	for _, s := range dead {
		if isTestFile(s.File) {
			continue
		}
		if isEntryPointGo(s) {
			continue
		}
		if len(s.Name) <= 2 {
			continue
		}

		// Also check regex references for non-Go callers
		refs := referenceCount(s.Name, files, s.File)
		if refs > 0 {
			continue // referenced somewhere, just not in Go call graph
		}

		findings = append(findings, Finding{
			RuleID:   "unreachable-symbol",
			Category: CatConsistency,
			Severity: SevBlocking,
			File:     s.File,
			Line:     s.Line,
			Description: fmt.Sprintf("Exported %s %q defined but unreachable from any entry point (AST call graph verified) — dead code",
				s.Kind, s.Name),
			Suggestion: fmt.Sprintf("Either wire %s into the call chain from an entry point, or remove it if unused", s.Name),
			Evidence:   fmt.Sprintf("%s %s (AST: not in call graph from entry points)", s.Kind, s.Name),
		})
	}
	return findings
}

// isEntryPointGo checks if a goast symbol is an entry point.
func isEntryPointGo(s goast.Symbol) bool {
	if s.Name == "main" || s.Name == "init" {
		return true
	}
	if strings.HasPrefix(s.Name, "Test") || strings.HasPrefix(s.Name, "Benchmark") ||
		strings.HasPrefix(s.Name, "Example") || strings.HasPrefix(s.Name, "Fuzz") {
		return true
	}
	lname := strings.ToLower(s.Name)
	patterns := []string{"handler", "middleware", "controller", "route",
		"setup", "teardown", "mount", "unmount", "render",
		"run", "start", "stop", "serve", "new", "default"}
	for _, p := range patterns {
		if strings.Contains(lname, p) {
			return true
		}
	}
	return s.Kind == goast.KindMethod
}

// checkReachabilityRegex uses reference counting for non-Go files.
func checkReachabilityRegex(allSymList []codeSymbol, files []FileInput) []Finding {
	var findings []Finding
	for _, sym := range allSymList {
		if !sym.Exported {
			continue
		}
		if isTestFile(sym.File) {
			continue
		}
		if isEntryPoint(sym.Name, sym.Kind) {
			continue
		}
		if len(sym.Name) <= 2 {
			continue
		}

		refs := referenceCount(sym.Name, files, sym.File)
		if refs == 0 {
			findings = append(findings, Finding{
				RuleID:   "unreachable-symbol",
				Category: CatConsistency,
				Severity: SevBlocking,
				File:     sym.File,
				Line:     sym.Line,
				Description: fmt.Sprintf("Exported %s %q defined but never referenced in any other file — dead code",
					sym.Kind, sym.Name),
				Suggestion: fmt.Sprintf("Either wire %s into the call chain from an entry point, or remove it if unused", sym.Name),
				Evidence:   fmt.Sprintf("%s %s (0 cross-file references)", sym.Kind, sym.Name),
			})
		}
	}
	return findings
}

// mapCriteriaToCode checks each acceptance criterion against actual code
// symbols and content. Instead of naive keyword matching, it:
// 1. Extracts semantic concepts from the criterion
// 2. Searches for symbols that relate to those concepts
// 3. Checks for behavioral evidence (test assertions, API calls, config)
func mapCriteriaToCode(criteria []string, files []FileInput, symbols []codeSymbol) []Finding {
	var findings []Finding

	// Build a content index for semantic search
	allContent := &strings.Builder{}
	for _, f := range files {
		if !isTestFile(f.Path) {
			allContent.Write(f.Content)
			allContent.WriteByte('\n')
		}
	}
	codeContent := strings.ToLower(allContent.String())

	// Build test content separately — we need to verify tests exist for criteria
	testContent := &strings.Builder{}
	for _, f := range files {
		if isTestFile(f.Path) {
			testContent.Write(f.Content)
			testContent.WriteByte('\n')
		}
	}
	testStr := strings.ToLower(testContent.String())

	// Collect all symbol names for matching
	symbolNames := make(map[string]bool)
	for _, s := range symbols {
		symbolNames[strings.ToLower(s.Name)] = true
	}

	for i, criterion := range criteria {
		concepts := extractConcepts(criterion)
		if len(concepts) == 0 {
			continue
		}

		// Score how well the codebase addresses this criterion
		codeScore := 0
		testScore := 0

		for _, concept := range concepts {
			lc := strings.ToLower(concept)
			// Check if concept appears in code
			if strings.Contains(codeContent, lc) {
				codeScore++
			}
			// Check if concept appears in tests
			if strings.Contains(testStr, lc) {
				testScore++
			}
			// Check if any symbol name contains the concept
			for name := range symbolNames {
				if strings.Contains(name, lc) || strings.Contains(lc, name) {
					codeScore++
					break
				}
			}
		}

		// If less than 30% of concepts have evidence in code, flag it
		threshold := len(concepts) * 3 / 10
		if threshold < 1 {
			threshold = 1
		}
		if codeScore < threshold {
			findings = append(findings, Finding{
				RuleID:   "criteria-semantic",
				Category: CatCompleteness,
				Severity: SevBlocking,
				Description: fmt.Sprintf("Criterion %d has weak code evidence: %q — only %d/%d concepts found in code",
					i+1, criterion, codeScore, len(concepts)),
				Suggestion: "Verify this criterion is actually implemented, not just mentioned in comments",
				Evidence:   fmt.Sprintf("concepts: %v", concepts),
			})
		}

		// If criterion has code evidence but no test evidence, flag it
		if codeScore >= threshold && testScore == 0 && len(concepts) > 0 {
			findings = append(findings, Finding{
				RuleID:   "criteria-semantic",
				Category: CatTestCoverage,
				Severity: SevMajor,
				Description: fmt.Sprintf("Criterion %d has code but no test evidence: %q",
					i+1, criterion),
				Suggestion: "Add tests that specifically verify this acceptance criterion",
				Evidence:   fmt.Sprintf("code_score=%d, test_score=%d", codeScore, testScore),
			})
		}
	}

	return findings
}

// extractConcepts breaks a criterion into semantic concepts.
// More sophisticated than extractKeywords — it handles compound terms,
// domain-specific patterns, and filters noise words.
func extractConcepts(criterion string) []string {
	// Normalize
	criterion = strings.ToLower(criterion)

	// Remove common noise
	noise := map[string]bool{
		"the": true, "a": true, "an": true, "is": true, "are": true,
		"should": true, "must": true, "will": true, "can": true, "be": true,
		"to": true, "and": true, "or": true, "in": true, "on": true,
		"of": true, "for": true, "with": true, "that": true, "this": true,
		"it": true, "by": true, "from": true, "as": true, "at": true,
		"has": true, "have": true, "had": true, "not": true, "no": true,
		"all": true, "any": true, "each": true, "every": true, "when": true,
		"if": true, "then": true, "than": true, "so": true, "up": true,
		"out": true, "into": true, "does": true, "do": true, "did": true,
		"been": true, "being": true, "was": true, "were": true,
	}

	// Split on word boundaries and punctuation
	words := regexp.MustCompile(`[a-z0-9]+`).FindAllString(criterion, -1)

	concepts := make([]string, 0, len(words))
	for _, w := range words {
		if len(w) <= 2 {
			continue
		}
		if noise[w] {
			continue
		}
		concepts = append(concepts, w)
	}

	// Also extract compound terms (bigrams) for domain-specific concepts
	for i := 0; i < len(words)-1; i++ {
		if noise[words[i]] || noise[words[i+1]] {
			continue
		}
		if len(words[i]) <= 2 || len(words[i+1]) <= 2 {
			continue
		}
		bigram := words[i] + words[i+1]
		concepts = append(concepts, bigram)
	}

	return concepts
}

// isEntryPoint returns true for symbols that are invoked by the runtime
// (main, init, test functions, HTTP handlers, etc.) and don't need
// explicit caller references.
func isEntryPoint(name string, kind symbolKind) bool {
	// Go entry points
	if name == "main" || name == "init" {
		return true
	}
	// Test functions
	if strings.HasPrefix(name, "Test") || strings.HasPrefix(name, "Benchmark") ||
		strings.HasPrefix(name, "Example") || strings.HasPrefix(name, "Fuzz") {
		return true
	}
	// Common framework callbacks/handlers
	lname := strings.ToLower(name)
	frameworkPatterns := []string{
		"handler", "middleware", "controller", "route",
		"setup", "teardown", "beforeeach", "aftereach",
		"mount", "unmount", "render", "component",
		"model", "update", "view", // Elm/Bubble Tea architecture
		"run", "start", "stop", "serve",
		"new", "default",
	}
	for _, p := range frameworkPatterns {
		if strings.Contains(lname, p) {
			return true
		}
	}

	// Methods are typically called via their receiver, not directly
	if kind == skMethod {
		return true
	}

	return false
}
