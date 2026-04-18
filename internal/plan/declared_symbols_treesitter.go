package plan

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/javascript"
	"github.com/smacker/go-tree-sitter/python"
	"github.com/smacker/go-tree-sitter/typescript/tsx"
	"github.com/smacker/go-tree-sitter/typescript/typescript"
)

// H-28 — Tree-sitter backed variant of the H-27 declared-symbol gate.
// Built as a direct A/B comparison: regex (H-27) catches ~50% false
// positives; tree-sitter walks the AST and extracts only real
// declarations (function names, class names, exported consts, object
// methods, arrow-function-assigned consts, etc), so in principle
// precision should be higher.
//
// Enable by setting STOKE_H27_TREESITTER=1 at launch. When set, the
// quality-signal sweep calls treeSitterExtractSymbols instead of the
// symindex regex path. Outputs are merged back into the same finding
// stream, so downstream logic (reviewer prompt prepend, commit gate)
// doesn't change.
//
// Language coverage: TypeScript (ts/tsx), JavaScript (js/jsx/mjs/cjs),
// Python. Go falls through to the stdlib go/parser path already in
// symindex (tree-sitter-go adds no precision over go/parser). Other
// languages (Rust, Java, Kotlin, Swift, Ruby, PHP, C#, Elixir, C/C++,
// Scala) stay on the regex path — adding them to tree-sitter would
// multiply cgo build time for marginal gain when the cohort runs on
// TS/JS ~95% of the time.

// treeSitterEnabled returns true when the operator opted into the
// tree-sitter extractor via environment variable. Keeping this a
// runtime check (vs build tag) means both regex and tree-sitter
// variants ship in the same binary — operators can A/B without
// rebuilding.
func treeSitterEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("STOKE_H27_TREESITTER")))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

// ScanDeclaredSymbolsNotImplementedTreeSitter is the tree-sitter
// variant of ScanDeclaredSymbolsNotImplemented. Same inputs, same
// output type — swappable at the quality-sweep call site. Falls
// through to the regex variant for files in languages tree-sitter
// doesn't cover so precision doesn't regress on mixed-language repos.
func ScanDeclaredSymbolsNotImplementedTreeSitter(repoRoot, sowProse string, changedFiles []string) []QualityFinding {
	if repoRoot == "" || strings.TrimSpace(sowProse) == "" || len(changedFiles) == 0 {
		return nil
	}
	tsFiles := make([]string, 0, len(changedFiles))
	fallbackFiles := make([]string, 0, len(changedFiles))
	for _, f := range changedFiles {
		if treeSitterHasParser(f) {
			tsFiles = append(tsFiles, f)
		} else if looksLikeSource(f) {
			fallbackFiles = append(fallbackFiles, f)
		}
	}
	if len(tsFiles) == 0 && len(fallbackFiles) == 0 {
		return nil
	}

	declared := ExtractDeclaredSymbols(sowProse)
	if len(declared) == 0 {
		return nil
	}

	present := make(map[string]bool, 128)
	// Tree-sitter path for supported files.
	for _, rel := range tsFiles {
		body, err := os.ReadFile(filepath.Join(repoRoot, rel))
		if err != nil {
			continue
		}
		for _, sym := range treeSitterExtractSymbols(rel, body) {
			if sym == "" {
				continue
			}
			present[sym] = true
			present[strings.ToLower(sym)] = true
		}
	}
	// Regex path via symindex for everything else.
	if len(fallbackFiles) > 0 {
		if ExtractDeclaredSymbolsFallback != nil {
			for _, sym := range ExtractDeclaredSymbolsFallback(repoRoot, fallbackFiles) {
				present[sym] = true
				present[strings.ToLower(sym)] = true
			}
		}
	}

	var out []QualityFinding
	for _, d := range declared {
		if present[d] || present[strings.ToLower(d)] {
			continue
		}
		out = append(out, QualityFinding{
			Severity: SevBlocking,
			Kind:     "declared-symbol-not-implemented-ts",
			File:     "(sow)",
			Line:     1,
			Detail: "[tree-sitter] SOW prose declares symbol `" + d + "` but no matching function, class, method, type, interface, or exported const with that name was found in the changed source files. (H-28 tree-sitter extractor, A/B vs H-27 regex variant.)",
		})
	}
	return out
}

// ExtractDeclaredSymbolsFallback is a package-var hook so the symindex-
// based regex extractor can be swapped in without an import cycle.
// Set from declared_symbols.go at init time. Present to keep H-28's
// coverage symmetric with H-27 when a file is outside tree-sitter's
// supported set.
var ExtractDeclaredSymbolsFallback func(repoRoot string, files []string) []string

// treeSitterHasParser reports whether we have a tree-sitter parser
// for the file extension. Keeping the list explicit so adding a new
// language is a one-liner + an import.
func treeSitterHasParser(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".py":
		return true
	}
	return false
}

// treeSitterLanguage returns the sitter.Language* for the file ext,
// or nil when unsupported. Small closed enum to avoid a reflection
// or map-based dispatch on the hot path.
func treeSitterLanguage(path string) *sitter.Language {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".ts":
		return typescript.GetLanguage()
	case ".tsx", ".jsx":
		return tsx.GetLanguage()
	case ".js", ".mjs", ".cjs":
		return javascript.GetLanguage()
	case ".py":
		return python.GetLanguage()
	}
	return nil
}

// treeSitterExtractSymbols parses source via tree-sitter and returns
// the set of declared identifier names (functions, classes, methods,
// exported consts, type aliases, interfaces). Silent on parse errors
// — returns whatever names the partial parse yielded.
//
// The precision win over regex: arrow-function-assigned consts
// (`export const foo = () => ...`) and class methods inside bodies
// are reliably captured where the regex patterns in symindex miss or
// under-capture them.
func treeSitterExtractSymbols(path string, source []byte) []string {
	lang := treeSitterLanguage(path)
	if lang == nil {
		return nil
	}
	parser := sitter.NewParser()
	parser.SetLanguage(lang)
	tree, err := parser.ParseCtx(context.Background(), nil, source)
	if err != nil || tree == nil {
		return nil
	}
	defer tree.Close()

	var out []string
	walk(tree.RootNode(), source, &out)
	return dedupStrings(out)
}

// walk traverses the AST collecting declared identifier names. The
// node-type set covers TS/JS/Python; adding a language means adding
// its declaration node-type names here (or leaving them to fall
// through to the generic identifier-under-declaration pattern).
func walk(n *sitter.Node, src []byte, out *[]string) {
	if n == nil {
		return
	}
	kind := n.Type()
	switch kind {
	// JavaScript / TypeScript
	case "function_declaration",
		"generator_function_declaration",
		"class_declaration",
		"abstract_class_declaration",
		"interface_declaration",
		"type_alias_declaration",
		"enum_declaration",
		"method_definition",
		"method_signature",
		"abstract_method_signature":
		if nameNode := n.ChildByFieldName("name"); nameNode != nil {
			*out = append(*out, string(src[nameNode.StartByte():nameNode.EndByte()]))
		}
	case "variable_declarator":
		// `const foo = () => ...` — capture `foo` only when the value
		// is a function-ish thing (arrow, function_expression, class
		// expression). Otherwise a plain const wouldn't be a symbol
		// the gate expects.
		name := n.ChildByFieldName("name")
		value := n.ChildByFieldName("value")
		if name != nil && value != nil {
			vkind := value.Type()
			if vkind == "arrow_function" || vkind == "function" ||
				vkind == "function_expression" || vkind == "class" ||
				vkind == "class_expression" {
				*out = append(*out, string(src[name.StartByte():name.EndByte()]))
			}
		}
	case "export_statement":
		// `export default function Foo()` / `export default class Foo`
		// / `export default () => ...` — the inner declaration gets
		// visited as we recurse; capture an inline `export { a, b }`
		// here by walking its export_clause children.
		for i := 0; i < int(n.NamedChildCount()); i++ {
			c := n.NamedChild(i)
			if c != nil && c.Type() == "export_clause" {
				for j := 0; j < int(c.NamedChildCount()); j++ {
					spec := c.NamedChild(j)
					if spec != nil && spec.Type() == "export_specifier" {
						if nm := spec.ChildByFieldName("name"); nm != nil {
							*out = append(*out, string(src[nm.StartByte():nm.EndByte()]))
						}
					}
				}
			}
		}
	// Python
	case "function_definition", "class_definition":
		if nameNode := n.ChildByFieldName("name"); nameNode != nil {
			*out = append(*out, string(src[nameNode.StartByte():nameNode.EndByte()]))
		}
	}
	for i := 0; i < int(n.NamedChildCount()); i++ {
		walk(n.NamedChild(i), src, out)
	}
}

// dedupStrings preserves first-seen order while removing duplicates.
// Used for the returned symbol list; extraction order isn't load-
// bearing but stable output helps tests.
func dedupStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
