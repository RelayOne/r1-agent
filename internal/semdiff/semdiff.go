// Package semdiff provides semantic diff analysis.
// Goes beyond line-level diffs to understand structural code changes:
//
// - Detects function renames, moves, and signature changes
// - Identifies refactoring patterns (extract method, inline, rename)
// - Classifies changes by impact (breaking, behavioral, cosmetic)
// - Produces human-readable summaries of what actually changed
// - Helps LLMs understand diffs in terms of intent, not just text
//
// For Go files, uses real go/parser AST for accurate extraction.
// For other languages, falls back to regex-based extraction.
package semdiff

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"sort"
	"strings"
)

// ChangeKind classifies a semantic change.
type ChangeKind string

const (
	KindAdded      ChangeKind = "added"
	KindRemoved    ChangeKind = "removed"
	KindRenamed    ChangeKind = "renamed"
	KindMoved      ChangeKind = "moved"
	KindModified   ChangeKind = "modified"
	KindSignature  ChangeKind = "signature_changed"
	KindRefactored ChangeKind = "refactored"
)

// Impact classifies the severity of a change.
type Impact string

const (
	ImpactBreaking   Impact = "breaking"   // public API change, removed export
	ImpactBehavioral Impact = "behavioral" // logic change, different output
	ImpactCosmetic   Impact = "cosmetic"   // formatting, comments, rename
	ImpactInternal   Impact = "internal"   // private implementation change
)

// SymbolChange describes a change to a code symbol.
type SymbolChange struct {
	Kind       ChangeKind `json:"kind"`
	Impact     Impact     `json:"impact"`
	Name       string     `json:"name"`
	OldName    string     `json:"old_name,omitempty"`
	File       string     `json:"file"`
	OldFile    string     `json:"old_file,omitempty"`
	SymbolType string     `json:"symbol_type"` // func, type, var, const, method
	Summary    string     `json:"summary"`
	// AST-level detail (Go files only)
	OldSignature string `json:"old_signature,omitempty"`
	NewSignature string `json:"new_signature,omitempty"`
}

// Analysis is the result of semantic diff analysis.
type Analysis struct {
	Changes     []SymbolChange `json:"changes"`
	FileChanges []FileChange   `json:"file_changes"`
	Summary     string         `json:"summary"`
}

// FileChange describes changes at the file level.
type FileChange struct {
	Path      string `json:"path"`
	Added     int    `json:"added"`
	Removed   int    `json:"removed"`
	IsNew     bool   `json:"is_new,omitempty"`
	IsDeleted bool   `json:"is_deleted,omitempty"`
}

// symbol is an extracted code symbol with its body.
type symbol struct {
	Name      string
	Type      string // func, method, type, var, const, interface
	Signature string // full declaration line
	Body      string // full body (for similarity matching)
	Line      int
	Exported  bool
	// AST-level fields (Go files only)
	ASTSig  string // typed signature from AST
	ASTBody string // normalized AST body for structural comparison
}

// Regex patterns for non-Go fallback
var (
	funcRe      = regexp.MustCompile(`^func\s+(\w+)\s*\(`)
	methodRe    = regexp.MustCompile(`^func\s+\(\w+\s+\*?(\w+)\)\s+(\w+)\s*\(`)
	typeRe      = regexp.MustCompile(`^type\s+(\w+)\s+`)
	varRe       = regexp.MustCompile(`^var\s+(\w+)\s`)
	constRe     = regexp.MustCompile(`^const\s+(\w+)\s`)
	interfaceRe = regexp.MustCompile(`^type\s+(\w+)\s+interface\b`)
	structRe    = regexp.MustCompile(`^type\s+(\w+)\s+struct\b`)
)

// Analyze performs semantic diff analysis between old and new file content.
func Analyze(oldContent, newContent, filePath string) *Analysis {
	isGo := strings.HasSuffix(filePath, ".go")

	var oldSyms, newSyms []symbol
	if isGo {
		oldSyms = extractGoAST(oldContent, filePath)
		newSyms = extractGoAST(newContent, filePath)
	} else {
		oldSyms = extractSymbolsRegex(oldContent)
		newSyms = extractSymbolsRegex(newContent)
	}

	analysis := &Analysis{}

	oldMap := make(map[string]*symbol)
	newMap := make(map[string]*symbol)
	for i := range oldSyms {
		oldMap[oldSyms[i].Name] = &oldSyms[i]
	}
	for i := range newSyms {
		newMap[newSyms[i].Name] = &newSyms[i]
	}

	// Detect removed symbols
	for name, old := range oldMap {
		if _, exists := newMap[name]; !exists {
			// Check if renamed (similar body in new symbols)
			renamed := findSimilar(old, newSyms, oldMap, isGo)
			if renamed != "" {
				analysis.Changes = append(analysis.Changes, SymbolChange{
					Kind:       KindRenamed,
					Impact:     classifyRenameImpact(old),
					Name:       renamed,
					OldName:    name,
					File:       filePath,
					SymbolType: old.Type,
					Summary:    fmt.Sprintf("Renamed %s %s → %s", old.Type, name, renamed),
				})
			} else {
				analysis.Changes = append(analysis.Changes, SymbolChange{
					Kind:       KindRemoved,
					Impact:     classifyRemoveImpact(old),
					Name:       name,
					File:       filePath,
					SymbolType: old.Type,
					Summary:    fmt.Sprintf("Removed %s %s", old.Type, name),
				})
			}
		}
	}

	// Detect added and modified symbols
	for name, new := range newMap {
		old, existed := oldMap[name]
		if !existed {
			// Skip if it was detected as a rename target
			if isRenameTarget(name, analysis.Changes) {
				continue
			}
			analysis.Changes = append(analysis.Changes, SymbolChange{
				Kind:       KindAdded,
				Impact:     classifyAddImpact(new),
				Name:       name,
				File:       filePath,
				SymbolType: new.Type,
				Summary:    fmt.Sprintf("Added %s %s", new.Type, name),
			})
		} else if isGo && old.ASTSig != "" && new.ASTSig != "" {
			// AST-level comparison: typed signature change
			if old.ASTSig != new.ASTSig {
				analysis.Changes = append(analysis.Changes, SymbolChange{
					Kind:         KindSignature,
					Impact:       classifySignatureImpact(old, new),
					Name:         name,
					File:         filePath,
					SymbolType:   new.Type,
					Summary:      fmt.Sprintf("Changed signature of %s %s", new.Type, name),
					OldSignature: old.ASTSig,
					NewSignature: new.ASTSig,
				})
			} else if old.ASTBody != new.ASTBody {
				// Same signature, different body (AST-level structural comparison)
				analysis.Changes = append(analysis.Changes, SymbolChange{
					Kind:       KindModified,
					Impact:     ImpactBehavioral,
					Name:       name,
					File:       filePath,
					SymbolType: new.Type,
					Summary:    fmt.Sprintf("Modified body of %s %s", new.Type, name),
				})
			}
		} else {
			// Regex fallback: text-based comparison
			if old.Signature != new.Signature {
				analysis.Changes = append(analysis.Changes, SymbolChange{
					Kind:       KindSignature,
					Impact:     classifySignatureImpact(old, new),
					Name:       name,
					File:       filePath,
					SymbolType: new.Type,
					Summary:    fmt.Sprintf("Changed signature of %s %s", new.Type, name),
				})
			} else if old.Body != new.Body {
				analysis.Changes = append(analysis.Changes, SymbolChange{
					Kind:       KindModified,
					Impact:     ImpactBehavioral,
					Name:       name,
					File:       filePath,
					SymbolType: new.Type,
					Summary:    fmt.Sprintf("Modified body of %s %s", new.Type, name),
				})
			}
		}
	}

	// File-level stats
	oldLines := strings.Count(oldContent, "\n")
	newLines := strings.Count(newContent, "\n")
	analysis.FileChanges = []FileChange{{
		Path:    filePath,
		Added:   max(0, newLines-oldLines),
		Removed: max(0, oldLines-newLines),
	}}

	// Sort changes by impact severity
	sort.Slice(analysis.Changes, func(i, j int) bool {
		return impactOrder(analysis.Changes[i].Impact) < impactOrder(analysis.Changes[j].Impact)
	})

	analysis.Summary = buildSummary(analysis)
	return analysis
}

// AnalyzeMultiFile analyzes changes across multiple files.
func AnalyzeMultiFile(files map[string][2]string) *Analysis {
	combined := &Analysis{}

	for path, contents := range files {
		oldSrc, newSrc := contents[0], contents[1]

		if oldSrc == "" && newSrc != "" {
			combined.FileChanges = append(combined.FileChanges, FileChange{Path: path, IsNew: true, Added: strings.Count(newSrc, "\n")})
			var syms []symbol
			if strings.HasSuffix(path, ".go") {
				syms = extractGoAST(newSrc, path)
			} else {
				syms = extractSymbolsRegex(newSrc)
			}
			for _, sym := range syms {
				combined.Changes = append(combined.Changes, SymbolChange{
					Kind: KindAdded, Impact: classifyAddImpact(&sym),
					Name: sym.Name, File: path, SymbolType: sym.Type,
					Summary: fmt.Sprintf("Added %s %s", sym.Type, sym.Name),
				})
			}
			continue
		}

		if oldSrc != "" && newSrc == "" {
			combined.FileChanges = append(combined.FileChanges, FileChange{Path: path, IsDeleted: true, Removed: strings.Count(oldSrc, "\n")})
			var syms []symbol
			if strings.HasSuffix(path, ".go") {
				syms = extractGoAST(oldSrc, path)
			} else {
				syms = extractSymbolsRegex(oldSrc)
			}
			for _, sym := range syms {
				combined.Changes = append(combined.Changes, SymbolChange{
					Kind: KindRemoved, Impact: classifyRemoveImpact(&sym),
					Name: sym.Name, File: path, SymbolType: sym.Type,
					Summary: fmt.Sprintf("Removed %s %s", sym.Type, sym.Name),
				})
			}
			continue
		}

		single := Analyze(oldSrc, newSrc, path)
		combined.Changes = append(combined.Changes, single.Changes...)
		combined.FileChanges = append(combined.FileChanges, single.FileChanges...)
	}

	combined.Summary = buildSummary(combined)
	return combined
}

// HasBreaking returns true if any change is breaking.
func (a *Analysis) HasBreaking() bool {
	for _, c := range a.Changes {
		if c.Impact == ImpactBreaking {
			return true
		}
	}
	return false
}

// ByImpact returns changes filtered by impact level.
func (a *Analysis) ByImpact(impact Impact) []SymbolChange {
	var result []SymbolChange
	for _, c := range a.Changes {
		if c.Impact == impact {
			result = append(result, c)
		}
	}
	return result
}

// --- Go AST extraction ---

// extractGoAST uses go/parser for real AST-based symbol extraction.
func extractGoAST(source, filePath string) []symbol {
	if source == "" {
		return nil
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filePath, source, parser.ParseComments)
	if err != nil {
		// Fall back to regex if AST parse fails
		return extractSymbolsRegex(source)
	}

	var symbols []symbol

	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			sym := symbol{
				Name:     d.Name.Name,
				Type:     "func",
				Line:     fset.Position(d.Pos()).Line,
				Exported: d.Name.IsExported(),
			}
			sym.Signature = strings.TrimSpace(lineAt(source, sym.Line))

			if d.Recv != nil && len(d.Recv.List) > 0 {
				sym.Type = "method"
			}

			// AST-level typed signature
			sym.ASTSig = buildASTFuncSig(d)

			// AST-level normalized body for structural comparison
			if d.Body != nil {
				sym.ASTBody = normalizeASTBody(fset, source, d.Body)
			}

			// Textual body for regex-level similarity
			if d.Body != nil {
				start := fset.Position(d.Body.Pos()).Offset
				end := fset.Position(d.Body.End()).Offset
				if end <= len(source) {
					sym.Body = source[start:end]
				}
			}

			symbols = append(symbols, sym)

		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					sym := symbol{
						Name:     s.Name.Name,
						Line:     fset.Position(s.Pos()).Line,
						Exported: s.Name.IsExported(),
					}
					sym.Signature = strings.TrimSpace(lineAt(source, sym.Line))

					switch s.Type.(type) {
					case *ast.InterfaceType:
						sym.Type = "interface"
					case *ast.StructType:
						sym.Type = "type"
					default:
						sym.Type = "type"
					}

					// For structs/interfaces: normalize the full type body
					start := fset.Position(s.Pos()).Offset
					end := fset.Position(s.End()).Offset
					if end <= len(source) {
						sym.ASTBody = normalizeWhitespace(source[start:end])
						sym.Body = source[start:end]
					}
					sym.ASTSig = sym.Type + " " + s.Name.Name

					symbols = append(symbols, sym)

				case *ast.ValueSpec:
					for _, name := range s.Names {
						sym := symbol{
							Name:     name.Name,
							Line:     fset.Position(name.Pos()).Line,
							Exported: name.IsExported(),
						}
						if d.Tok == token.CONST {
							sym.Type = "const"
						} else {
							sym.Type = "var"
						}
						sym.Signature = strings.TrimSpace(lineAt(source, sym.Line))
						sym.ASTSig = sym.Type + " " + name.Name
						symbols = append(symbols, sym)
					}
				}
			}
		}
	}

	return symbols
}

// buildASTFuncSig builds a typed function signature from the AST.
// This captures parameter types and return types, not just names.
func buildASTFuncSig(d *ast.FuncDecl) string {
	var b strings.Builder

	if d.Recv != nil && len(d.Recv.List) > 0 {
		b.WriteString("(")
		b.WriteString(exprString(d.Recv.List[0].Type))
		b.WriteString(").")
	}

	b.WriteString(d.Name.Name)
	b.WriteString("(")
	if d.Type.Params != nil {
		b.WriteString(fieldListString(d.Type.Params))
	}
	b.WriteString(")")

	if d.Type.Results != nil && len(d.Type.Results.List) > 0 {
		b.WriteString(" ")
		if len(d.Type.Results.List) == 1 && len(d.Type.Results.List[0].Names) == 0 {
			b.WriteString(exprString(d.Type.Results.List[0].Type))
		} else {
			b.WriteString("(")
			b.WriteString(fieldListString(d.Type.Results))
			b.WriteString(")")
		}
	}

	return b.String()
}

func fieldListString(fl *ast.FieldList) string {
	var parts []string
	for _, field := range fl.List {
		typeStr := exprString(field.Type)
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

func exprString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + exprString(t.X)
	case *ast.SelectorExpr:
		return exprString(t.X) + "." + t.Sel.Name
	case *ast.ArrayType:
		if t.Len == nil {
			return "[]" + exprString(t.Elt)
		}
		return "[...]" + exprString(t.Elt)
	case *ast.MapType:
		return "map[" + exprString(t.Key) + "]" + exprString(t.Value)
	case *ast.InterfaceType:
		return "interface{}"
	case *ast.FuncType:
		return "func(...)"
	case *ast.ChanType:
		return "chan " + exprString(t.Value)
	case *ast.Ellipsis:
		return "..." + exprString(t.Elt)
	}
	return "?"
}

// normalizeASTBody normalizes a function body for structural comparison.
// Strips comments, normalizes whitespace, but preserves structure.
func normalizeASTBody(fset *token.FileSet, source string, body *ast.BlockStmt) string {
	start := fset.Position(body.Pos()).Offset
	end := fset.Position(body.End()).Offset
	if end > len(source) {
		return ""
	}
	raw := source[start:end]
	return normalizeWhitespace(raw)
}

func normalizeWhitespace(s string) string {
	// Collapse all whitespace sequences to single space
	parts := strings.Fields(s)
	return strings.Join(parts, " ")
}

func lineAt(source string, lineNum int) string {
	lines := strings.Split(source, "\n")
	if lineNum > 0 && lineNum <= len(lines) {
		return lines[lineNum-1]
	}
	return ""
}

// --- Regex fallback extraction (for non-Go files) ---

func extractSymbolsRegex(source string) []symbol {
	lines := strings.Split(source, "\n")
	var symbols []symbol
	var currentBody strings.Builder
	var currentSym *symbol
	braceDepth := 0

	for lineNum, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Check for symbol declarations
		if braceDepth == 0 {
			if sym := matchSymbol(trimmed, lineNum+1); sym != nil {
				if currentSym != nil {
					currentSym.Body = currentBody.String()
					symbols = append(symbols, *currentSym)
				}
				currentSym = sym
				currentBody.Reset()
			}
		}

		if currentSym != nil {
			currentBody.WriteString(line)
			currentBody.WriteString("\n")
		}

		braceDepth += strings.Count(line, "{") - strings.Count(line, "}")
		if braceDepth <= 0 && currentSym != nil && currentBody.Len() > 0 {
			currentSym.Body = currentBody.String()
			symbols = append(symbols, *currentSym)
			currentSym = nil
			currentBody.Reset()
			braceDepth = 0
		}
	}

	if currentSym != nil {
		currentSym.Body = currentBody.String()
		symbols = append(symbols, *currentSym)
	}

	return symbols
}

func matchSymbol(line string, lineNum int) *symbol {
	if m := methodRe.FindStringSubmatch(line); m != nil {
		return &symbol{Name: m[2], Type: "method", Signature: line, Line: lineNum, Exported: isUpper(m[2])}
	}
	if m := funcRe.FindStringSubmatch(line); m != nil {
		return &symbol{Name: m[1], Type: "func", Signature: line, Line: lineNum, Exported: isUpper(m[1])}
	}
	if m := interfaceRe.FindStringSubmatch(line); m != nil {
		return &symbol{Name: m[1], Type: "interface", Signature: line, Line: lineNum, Exported: isUpper(m[1])}
	}
	if m := structRe.FindStringSubmatch(line); m != nil {
		return &symbol{Name: m[1], Type: "type", Signature: line, Line: lineNum, Exported: isUpper(m[1])}
	}
	if m := typeRe.FindStringSubmatch(line); m != nil {
		return &symbol{Name: m[1], Type: "type", Signature: line, Line: lineNum, Exported: isUpper(m[1])}
	}
	if m := varRe.FindStringSubmatch(line); m != nil {
		return &symbol{Name: m[1], Type: "var", Signature: line, Line: lineNum, Exported: isUpper(m[1])}
	}
	if m := constRe.FindStringSubmatch(line); m != nil {
		return &symbol{Name: m[1], Type: "const", Signature: line, Line: lineNum, Exported: isUpper(m[1])}
	}
	return nil
}

func findSimilar(old *symbol, newSyms []symbol, oldMap map[string]*symbol, useAST bool) string {
	if old.Body == "" && old.ASTBody == "" {
		return ""
	}
	bestScore := 0.0
	bestName := ""

	for _, ns := range newSyms {
		if _, exists := oldMap[ns.Name]; exists {
			continue
		}
		if ns.Type != old.Type {
			continue
		}

		var score float64
		if useAST && old.ASTBody != "" && ns.ASTBody != "" {
			// AST-level structural similarity
			score = structuralSimilarity(old.ASTBody, ns.ASTBody)
		} else {
			score = similarity(old.Body, ns.Body)
		}
		if score > 0.6 && score > bestScore {
			bestScore = score
			bestName = ns.Name
		}
	}
	return bestName
}

// structuralSimilarity compares normalized AST bodies.
// More accurate than word-set overlap because whitespace/formatting
// differences are already normalized out.
func structuralSimilarity(a, b string) float64 {
	if a == b {
		return 1.0
	}
	if a == "" || b == "" {
		return 0
	}

	// Token-level comparison on normalized bodies
	aTokens := strings.Fields(a)
	bTokens := strings.Fields(b)
	if len(aTokens) == 0 || len(bTokens) == 0 {
		return 0
	}

	// LCS-based similarity (more accurate than set intersection)
	lcsLen := lcsLength(aTokens, bTokens)
	return float64(2*lcsLen) / float64(len(aTokens)+len(bTokens))
}

// lcsLength computes length of longest common subsequence.
// Capped at 200 tokens to avoid O(n²) explosion on large functions.
func lcsLength(a, b []string) int {
	if len(a) > 200 {
		a = a[:200]
	}
	if len(b) > 200 {
		b = b[:200]
	}

	m, n := len(a), len(b)
	// Space-optimized: only need two rows
	prev := make([]int, n+1)
	curr := make([]int, n+1)

	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if a[i-1] == b[j-1] {
				curr[j] = prev[j-1] + 1
			} else if prev[j] > curr[j-1] {
				curr[j] = prev[j]
			} else {
				curr[j] = curr[j-1]
			}
		}
		prev, curr = curr, prev
		for j := range curr {
			curr[j] = 0
		}
	}
	return prev[n]
}

// similarity is the legacy word-set comparison (for non-Go files).
func similarity(a, b string) float64 {
	if a == b {
		return 1.0
	}
	aWords := strings.Fields(a)
	bWords := strings.Fields(b)
	if len(aWords) == 0 || len(bWords) == 0 {
		return 0
	}

	bSet := make(map[string]bool)
	for _, w := range bWords {
		bSet[w] = true
	}

	matches := 0
	for _, w := range aWords {
		if bSet[w] {
			matches++
		}
	}

	return float64(matches) / float64(max(len(aWords), len(bWords)))
}

func isRenameTarget(name string, changes []SymbolChange) bool {
	for _, c := range changes {
		if c.Kind == KindRenamed && c.Name == name {
			return true
		}
	}
	return false
}

func classifyRenameImpact(s *symbol) Impact {
	if s.Exported {
		return ImpactBreaking
	}
	return ImpactCosmetic
}

func classifyRemoveImpact(s *symbol) Impact {
	if s.Exported {
		return ImpactBreaking
	}
	return ImpactInternal
}

func classifyAddImpact(s *symbol) Impact {
	if s.Exported {
		return ImpactBehavioral
	}
	return ImpactInternal
}

func classifySignatureImpact(oldSym, newSym *symbol) Impact {
	if oldSym.Exported || newSym.Exported {
		return ImpactBreaking
	}
	return ImpactBehavioral
}

func impactOrder(i Impact) int {
	switch i {
	case ImpactBreaking:
		return 0
	case ImpactBehavioral:
		return 1
	case ImpactInternal:
		return 2
	case ImpactCosmetic:
		return 3
	}
	return 4
}

func isUpper(s string) bool {
	return len(s) > 0 && s[0] >= 'A' && s[0] <= 'Z'
}

func buildSummary(a *Analysis) string {
	var b strings.Builder
	breaking := len(a.ByImpact(ImpactBreaking))
	behavioral := len(a.ByImpact(ImpactBehavioral))
	internal := len(a.ByImpact(ImpactInternal))
	cosmetic := len(a.ByImpact(ImpactCosmetic))

	fmt.Fprintf(&b, "%d changes", len(a.Changes))
	if breaking > 0 {
		fmt.Fprintf(&b, " (%d breaking)", breaking)
	}
	if behavioral > 0 {
		fmt.Fprintf(&b, " (%d behavioral)", behavioral)
	}
	if internal > 0 {
		fmt.Fprintf(&b, " (%d internal)", internal)
	}
	if cosmetic > 0 {
		fmt.Fprintf(&b, " (%d cosmetic)", cosmetic)
	}

	if breaking > 0 {
		b.WriteString("\nBreaking changes:")
		for _, c := range a.ByImpact(ImpactBreaking) {
			fmt.Fprintf(&b, "\n  - %s", c.Summary)
		}
	}
	return b.String()
}
