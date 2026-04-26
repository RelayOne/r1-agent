// lsp.go — registers the multi-language LSP client (T-R1P-020) as a
// first-class skill capability the agent can call out to at edit-time.
//
// The skill registry already injects markdown skill files by keyword
// match (see registry.go). For LSP we additionally expose a thin Go
// surface so that callers — the executor, the autofix loop, the verify
// pipeline — can launch a language server, ask it for diagnostics or
// completions, and shut it down without re-deriving language detection
// from scratch.
//
// This file deliberately does NOT import internal/lsp/client to avoid
// dragging the whole client into every consumer of skill. Instead it
// exports the language-id list and a tiny LSPLauncher struct that
// callers can hand to the client package via name.

package skill

import (
	"path/filepath"
	"sort"
	"strings"
)

// LSPLanguage describes one language the multi-language LSP client knows
// how to launch. The Binaries slice lists the candidate executables in
// preference order — the LSP client picks the first one available on
// PATH at launch time.
type LSPLanguage struct {
	// ID is the canonical lower-case language id used by both the
	// agent's skill matcher and the LSP `languageId` field.
	ID string
	// Aliases are alternate names callers may use (e.g. "py", "ts").
	Aliases []string
	// Extensions are the file extensions (with leading dot) that map to
	// this language. Used by LanguageForFile.
	Extensions []string
	// Binaries is the preference-ordered list of language-server binaries.
	// The first one on $PATH wins.
	Binaries []string
	// Description is one-line human-readable summary for diagnostics.
	Description string
}

// lspLanguages is the registry of supported languages. Keep this list in
// sync with internal/lsp/client/client.go LaunchByLanguage().
var lspLanguages = []LSPLanguage{
	{
		ID:          "go",
		Extensions:  []string{".go"},
		Binaries:    []string{"gopls"},
		Description: "Go via gopls",
	},
	{
		ID:          "python",
		Aliases:     []string{"py"},
		Extensions:  []string{".py", ".pyi"},
		Binaries:    []string{"pyright-langserver", "pylsp"},
		Description: "Python via pyright-langserver (preferred) or pylsp",
	},
	{
		ID:          "typescript",
		Aliases:     []string{"javascript", "ts", "js", "tsx", "jsx"},
		Extensions:  []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"},
		Binaries:    []string{"typescript-language-server"},
		Description: "TypeScript / JavaScript via typescript-language-server",
	},
	{
		ID:          "rust",
		Aliases:     []string{"rs"},
		Extensions:  []string{".rs"},
		Binaries:    []string{"rust-analyzer"},
		Description: "Rust via rust-analyzer",
	},
}

// LSPLanguages returns the registered LSP languages in stable order.
// The caller must not mutate the returned slice.
func LSPLanguages() []LSPLanguage {
	out := make([]LSPLanguage, len(lspLanguages))
	copy(out, lspLanguages)
	return out
}

// LSPLanguageIDs returns just the canonical language IDs.
func LSPLanguageIDs() []string {
	ids := make([]string, 0, len(lspLanguages))
	for _, l := range lspLanguages {
		ids = append(ids, l.ID)
	}
	sort.Strings(ids)
	return ids
}

// LSPLanguageFor resolves a language id (canonical or alias) to its
// LSPLanguage entry. Returns false when no match is found.
func LSPLanguageFor(id string) (LSPLanguage, bool) {
	key := strings.ToLower(strings.TrimSpace(id))
	for _, l := range lspLanguages {
		if l.ID == key {
			return l, true
		}
		for _, a := range l.Aliases {
			if a == key {
				return l, true
			}
		}
	}
	return LSPLanguage{}, false
}

// LSPLanguageForFile picks the LSPLanguage whose Extensions list
// contains the given path's extension. Returns false on no match.
func LSPLanguageForFile(path string) (LSPLanguage, bool) {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == "" {
		return LSPLanguage{}, false
	}
	for _, l := range lspLanguages {
		for _, e := range l.Extensions {
			if e == ext {
				return l, true
			}
		}
	}
	return LSPLanguage{}, false
}
