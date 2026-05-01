//go:build !cgo

package plan

// ExtractDeclaredSymbolsFallback is a package-var hook so the symindex-
// based regex extractor can be swapped in without an import cycle.
var ExtractDeclaredSymbolsFallback func(repoRoot string, files []string) []string

// treeSitterEnabled reports false for static builds because the
// tree-sitter extractor requires cgo-backed parsers.
func treeSitterEnabled() bool {
	return false
}

// ScanDeclaredSymbolsNotImplementedTreeSitter falls back to the
// regex-backed symbol scan when the binary is built without cgo.
func ScanDeclaredSymbolsNotImplementedTreeSitter(repoRoot, sowProse string, changedFiles []string) []QualityFinding {
	return ScanDeclaredSymbolsNotImplemented(repoRoot, sowProse, changedFiles)
}
