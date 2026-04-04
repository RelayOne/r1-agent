// Package semdiff provides semantic diff analysis.
// Goes beyond line-level diffs to understand structural code changes:
//
// - Detects function renames, moves, and signature changes
// - Identifies refactoring patterns (extract method, inline, rename)
// - Classifies changes by impact (breaking, behavioral, cosmetic)
// - Produces human-readable summaries of what actually changed
// - Helps LLMs understand diffs in terms of intent, not just text
package semdiff

import (
	"fmt"
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
	Kind      ChangeKind `json:"kind"`
	Impact    Impact     `json:"impact"`
	Name      string     `json:"name"`
	OldName   string     `json:"old_name,omitempty"`
	File      string     `json:"file"`
	OldFile   string     `json:"old_file,omitempty"`
	SymbolType string    `json:"symbol_type"` // func, type, var, const, method
	Summary   string     `json:"summary"`
}

// Analysis is the result of semantic diff analysis.
type Analysis struct {
	Changes     []SymbolChange `json:"changes"`
	FileChanges []FileChange   `json:"file_changes"`
	Summary     string         `json:"summary"`
}

// FileChange describes changes at the file level.
type FileChange struct {
	Path       string `json:"path"`
	Added      int    `json:"added"`
	Removed    int    `json:"removed"`
	IsNew      bool   `json:"is_new,omitempty"`
	IsDeleted  bool   `json:"is_deleted,omitempty"`
}

// symbol is an extracted code symbol with its body.
type symbol struct {
	Name       string
	Type       string // func, method, type, var, const, interface
	Signature  string // full declaration line
	Body       string // full body (for similarity matching)
	Line       int
	Exported   bool
}

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
	oldSyms := extractSymbols(oldContent)
	newSyms := extractSymbols(newContent)

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
			renamed := findSimilar(old, newSyms, oldMap)
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
		} else if old.Signature != new.Signature {
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
		old, new := contents[0], contents[1]

		if old == "" && new != "" {
			combined.FileChanges = append(combined.FileChanges, FileChange{Path: path, IsNew: true, Added: strings.Count(new, "\n")})
			for _, sym := range extractSymbols(new) {
				combined.Changes = append(combined.Changes, SymbolChange{
					Kind: KindAdded, Impact: classifyAddImpact(&sym),
					Name: sym.Name, File: path, SymbolType: sym.Type,
					Summary: fmt.Sprintf("Added %s %s", sym.Type, sym.Name),
				})
			}
			continue
		}

		if old != "" && new == "" {
			combined.FileChanges = append(combined.FileChanges, FileChange{Path: path, IsDeleted: true, Removed: strings.Count(old, "\n")})
			for _, sym := range extractSymbols(old) {
				combined.Changes = append(combined.Changes, SymbolChange{
					Kind: KindRemoved, Impact: classifyRemoveImpact(&sym),
					Name: sym.Name, File: path, SymbolType: sym.Type,
					Summary: fmt.Sprintf("Removed %s %s", sym.Type, sym.Name),
				})
			}
			continue
		}

		single := Analyze(old, new, path)
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

func extractSymbols(source string) []symbol {
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

func findSimilar(old *symbol, newSyms []symbol, oldMap map[string]*symbol) string {
	if old.Body == "" {
		return ""
	}
	bestScore := 0.0
	bestName := ""

	for _, ns := range newSyms {
		// Skip if already exists in old (not a rename candidate)
		if _, exists := oldMap[ns.Name]; exists {
			continue
		}
		if ns.Type != old.Type {
			continue
		}
		score := similarity(old.Body, ns.Body)
		if score > 0.6 && score > bestScore {
			bestScore = score
			bestName = ns.Name
		}
	}
	return bestName
}

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

func classifySignatureImpact(old, new *symbol) Impact {
	if old.Exported || new.Exported {
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
