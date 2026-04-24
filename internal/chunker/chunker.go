// Package chunker implements semantic code chunking for context-aware splitting.
// Inspired by Aider's repo-map and claw-code's tree-sitter chunking:
//
// Instead of splitting files at arbitrary line boundaries, semantic chunking
// splits at meaningful boundaries (functions, classes, methods) to:
// - Preserve logical units of code
// - Enable targeted context injection (only relevant functions)
// - Reduce token waste from partial/broken constructs
//
// Uses lightweight regex-based parsing (no tree-sitter dependency) that works
// across Go, Python, TypeScript, Rust, and Java.
package chunker

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strings"
)

// Chunk is a semantically meaningful code block.
type Chunk struct {
	File      string `json:"file"`
	Name      string `json:"name"`       // function/class/method name
	Kind      string `json:"kind"`       // "function", "method", "class", "type", "block"
	StartLine int    `json:"start_line"` // 1-indexed
	EndLine   int    `json:"end_line"`   // 1-indexed, inclusive
	Content   string `json:"content"`
	Tokens    int    `json:"tokens"` // estimated token count
}

// Language detection by extension.
type Language string

const (
	LangGo         Language = "go"
	LangPython     Language = "python"
	LangTypeScript Language = "typescript"
	LangRust       Language = "rust"
	LangJava       Language = "java"
	LangUnknown    Language = "unknown"
)

// Canonical Chunk.Kind values for semantic code constructs.
const (
	KindFunction = "function"
	KindMethod   = "method"
	KindClass    = "class"
)

// DetectLanguage returns the language based on file extension.
func DetectLanguage(path string) Language {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return LangGo
	case ".py":
		return LangPython
	case ".ts", ".tsx", ".js", ".jsx":
		return LangTypeScript
	case ".rs":
		return LangRust
	case ".java":
		return LangJava
	default:
		return LangUnknown
	}
}

// ChunkFile splits source code into semantic chunks.
func ChunkFile(path, content string) []Chunk {
	lang := DetectLanguage(path)
	lines := strings.Split(content, "\n")

	// Go files: try AST-based chunking for precise boundaries.
	if lang == LangGo {
		if chunks := chunkGoAST(path, content, lines); len(chunks) > 0 {
			return chunks
		}
	}

	var patterns []*regexp.Regexp
	switch lang {
	case LangGo:
		patterns = goPatterns
	case LangPython:
		patterns = pythonPatterns
	case LangTypeScript:
		patterns = tsPatterns
	case LangRust:
		patterns = rustPatterns
	case LangJava:
		patterns = javaPatterns
	default:
		// Fallback: chunk by blank-line-separated blocks
		return chunkByBlankLines(path, lines)
	}

	return chunkBySyntax(path, lines, patterns, lang)
}

// ChunkFileWithBudget splits and filters chunks to fit a token budget.
func ChunkFileWithBudget(path, content string, maxTokens int) []Chunk {
	chunks := ChunkFile(path, content)

	result := make([]Chunk, 0, len(chunks))
	used := 0
	for _, c := range chunks {
		est := estimateTokens(c.Content)
		c.Tokens = est
		if used+est > maxTokens {
			break
		}
		result = append(result, c)
		used += est
	}
	return result
}

// FilterByName returns chunks matching any of the given names.
func FilterByName(chunks []Chunk, names ...string) []Chunk {
	nameSet := make(map[string]bool)
	for _, n := range names {
		nameSet[n] = true
	}
	var result []Chunk
	for _, c := range chunks {
		if nameSet[c.Name] {
			result = append(result, c)
		}
	}
	return result
}

// FilterByKind returns chunks of the given kind.
func FilterByKind(chunks []Chunk, kind string) []Chunk {
	var result []Chunk
	for _, c := range chunks {
		if c.Kind == kind {
			result = append(result, c)
		}
	}
	return result
}

// Render produces a compact display of chunks with line numbers.
func Render(chunks []Chunk) string {
	var b strings.Builder
	for i, c := range chunks {
		if i > 0 {
			b.WriteString("\n---\n")
		}
		fmt.Fprintf(&b, "// %s %s (%s:%d-%d)\n", c.Kind, c.Name, c.File, c.StartLine, c.EndLine)
		b.WriteString(c.Content)
		if !strings.HasSuffix(c.Content, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// --- Language-specific patterns ---

var goPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^func\s+(\w+)`),                       // function
	regexp.MustCompile(`^func\s+\([^)]+\)\s+(\w+)`),           // method
	regexp.MustCompile(`^type\s+(\w+)\s+(struct|interface)\b`), // type
}

var pythonPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^class\s+(\w+)`),      // class
	regexp.MustCompile(`^def\s+(\w+)`),         // function
	regexp.MustCompile(`^\s+def\s+(\w+)`),      // method (indented)
}

var tsPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^(?:export\s+)?(?:async\s+)?function\s+(\w+)`), // function
	regexp.MustCompile(`^(?:export\s+)?class\s+(\w+)`),                 // class
	regexp.MustCompile(`^(?:export\s+)?interface\s+(\w+)`),             // interface
	regexp.MustCompile(`^(?:export\s+)?const\s+(\w+)\s*=`),             // const arrow fn
}

var rustPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^(?:pub\s+)?fn\s+(\w+)`),              // function
	regexp.MustCompile(`^(?:pub\s+)?struct\s+(\w+)`),           // struct
	regexp.MustCompile(`^(?:pub\s+)?enum\s+(\w+)`),             // enum
	regexp.MustCompile(`^(?:pub\s+)?trait\s+(\w+)`),            // trait
	regexp.MustCompile(`^impl(?:<[^>]+>)?\s+(\w+)`),            // impl block
}

var javaPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^(?:public|private|protected)?\s*class\s+(\w+)`),     // class
	regexp.MustCompile(`^(?:public|private|protected)?\s*interface\s+(\w+)`),  // interface
	regexp.MustCompile(`^\s+(?:public|private|protected)?\s*\w+\s+(\w+)\s*\(`), // method
}

// chunkGoAST uses go/parser for precise function/type boundaries.
func chunkGoAST(path, content string, lines []string) []Chunk {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, content, parser.ParseComments)
	if err != nil {
		return nil
	}

	type boundary struct {
		startLine int
		endLine   int
		name      string
		kind      string
	}

	var bounds []boundary
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			start := fset.Position(d.Pos()).Line
			end := fset.Position(d.End()).Line
			kind := KindFunction
			name := d.Name.Name
			if d.Recv != nil {
				kind = KindMethod
			}
			bounds = append(bounds, boundary{startLine: start, endLine: end, name: name, kind: kind})
		case *ast.GenDecl:
			if d.Tok == token.TYPE {
				for _, spec := range d.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					start := fset.Position(d.Pos()).Line
					end := fset.Position(d.End()).Line
					bounds = append(bounds, boundary{startLine: start, endLine: end, name: ts.Name.Name, kind: "type"})
				}
			}
		}
	}

	if len(bounds) == 0 {
		return nil
	}

	chunks := make([]Chunk, 0, len(bounds))
	for _, b := range bounds {
		startIdx := b.startLine - 1
		endIdx := b.endLine - 1
		if startIdx < 0 {
			startIdx = 0
		}
		if endIdx >= len(lines) {
			endIdx = len(lines) - 1
		}
		c := strings.Join(lines[startIdx:endIdx+1], "\n")
		chunks = append(chunks, Chunk{
			File:      filepath.Base(path),
			Name:      b.name,
			Kind:      b.kind,
			StartLine: b.startLine,
			EndLine:   b.endLine,
			Content:   c,
			Tokens:    estimateTokens(c),
		})
	}
	return chunks
}

func chunkBySyntax(path string, lines []string, patterns []*regexp.Regexp, lang Language) []Chunk {
	type boundary struct {
		line int
		name string
		kind string
	}

	var boundaries []boundary

	for i, line := range lines {
		for _, pat := range patterns {
			if m := pat.FindStringSubmatch(line); m != nil {
				kind := detectKind(line, lang)
				boundaries = append(boundaries, boundary{
					line: i,
					name: m[1],
					kind: kind,
				})
				break
			}
		}
	}

	if len(boundaries) == 0 {
		return chunkByBlankLines(path, lines)
	}

	chunks := make([]Chunk, 0, len(boundaries))
	for i, b := range boundaries {
		endLine := len(lines) - 1
		if i+1 < len(boundaries) {
			endLine = boundaries[i+1].line - 1
			// Walk back over blank lines
			for endLine > b.line && strings.TrimSpace(lines[endLine]) == "" {
				endLine--
			}
		}

		content := strings.Join(lines[b.line:endLine+1], "\n")
		chunks = append(chunks, Chunk{
			File:      filepath.Base(path),
			Name:      b.name,
			Kind:      b.kind,
			StartLine: b.line + 1,
			EndLine:   endLine + 1,
			Content:   content,
			Tokens:    estimateTokens(content),
		})
	}

	return chunks
}

func chunkByBlankLines(path string, lines []string) []Chunk {
	var chunks []Chunk
	start := 0
	blockNum := 0

	for i := 0; i <= len(lines); i++ {
		isBlank := i == len(lines) || strings.TrimSpace(lines[i]) == ""
		if isBlank && i > start {
			// Check if we have meaningful content
			content := strings.Join(lines[start:i], "\n")
			if strings.TrimSpace(content) != "" {
				blockNum++
				chunks = append(chunks, Chunk{
					File:      filepath.Base(path),
					Name:      fmt.Sprintf("block_%d", blockNum),
					Kind:      "block",
					StartLine: start + 1,
					EndLine:   i,
					Content:   content,
					Tokens:    estimateTokens(content),
				})
			}
			start = i + 1
		} else if isBlank {
			start = i + 1
		}
	}
	return chunks
}

func detectKind(line string, lang Language) string {
	lower := strings.TrimSpace(line)
	switch lang {
	case LangGo:
		if strings.HasPrefix(lower, "type ") {
			return "type"
		}
		if strings.Contains(lower, ") ") && strings.HasPrefix(lower, "func (") {
			return KindMethod
		}
		return KindFunction
	case LangPython:
		if strings.HasPrefix(lower, "class ") {
			return KindClass
		}
		if strings.HasPrefix(lower, "def ") {
			return KindFunction
		}
		return KindMethod
	case LangTypeScript:
		if strings.Contains(lower, "class ") {
			return KindClass
		}
		if strings.Contains(lower, "interface ") {
			return "interface"
		}
		return KindFunction
	case LangRust:
		if strings.Contains(lower, "struct ") {
			return "struct"
		}
		if strings.Contains(lower, "enum ") {
			return "enum"
		}
		if strings.Contains(lower, "trait ") {
			return "trait"
		}
		if strings.HasPrefix(lower, "impl") {
			return "impl"
		}
		return KindFunction
	case LangJava:
		if strings.Contains(lower, "class ") {
			return KindClass
		}
		if strings.Contains(lower, "interface ") {
			return "interface"
		}
		return KindMethod
	}
	return "block"
}

func estimateTokens(content string) int {
	// ~3.3 chars per token for code
	if len(content) == 0 {
		return 0
	}
	est := float64(len(content)) / 3.3
	if est < 1 {
		return 1
	}
	return int(est + 0.5)
}
