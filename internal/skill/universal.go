// Package skill -- universal context layer.
//
// universal.go provides UniversalContext: a pair of merged markdown
// blobs (coding-standards + known-gotchas) that are injected into
// every agent system prompt in a stoke run. The blobs are layered:
//
//  1. Builtin defaults, embedded in the binary via //go:embed.
//  2. User overrides at $HOME/.stoke/{coding-standards,known-gotchas}.md.
//  3. Project overrides at <repoRoot>/.stoke/{coding-standards,known-gotchas}.md.
//
// Later layers are appended (not replaced); users extend rather than
// override. If the builtin files are missing at build time, the build
// fails -- embed requires them to exist.
package skill

import (
	_ "embed"
	"os"
	"path/filepath"
	"strings"

	"github.com/RelayOne/r1-agent/internal/r1dir"
)

//go:embed builtin/_universal/coding-standards.md
var embeddedCodingStandards string

//go:embed builtin/_universal/known-gotchas.md
var embeddedKnownGotchas string

// UniversalContext holds the merged coding-standards + known-gotchas
// content that gets injected into every agent prompt. Loaded once per
// stoke run via LoadUniversalContext; cheap to pass by value.
type UniversalContext struct {
	// CodingStandards is merged markdown from builtin + user + project.
	CodingStandards string
	// KnownGotchas is merged markdown from builtin + user + project.
	KnownGotchas string
	// Sources lists every file (short name or absolute path) that
	// contributed content, in application order. Useful for operator
	// visibility: they can confirm which overrides are live.
	Sources []string
}

// LoadUniversalContext reads the builtin embedded content, then layers
// any user overrides from $HOME/.stoke/ and <repoRoot>/.stoke/. Missing
// files are silently skipped. The returned context's Sources lists
// every file that contributed so the operator can verify what's active.
//
// repoRoot is optional; pass "" to skip the project-local layer.
func LoadUniversalContext(repoRoot string) UniversalContext {
	var u UniversalContext

	csParts := []string{strings.TrimRight(embeddedCodingStandards, " \t\n\r")}
	kgParts := []string{strings.TrimRight(embeddedKnownGotchas, " \t\n\r")}
	if strings.TrimSpace(embeddedCodingStandards) != "" {
		u.Sources = append(u.Sources, "builtin:coding-standards.md")
	}
	if strings.TrimSpace(embeddedKnownGotchas) != "" {
		u.Sources = append(u.Sources, "builtin:known-gotchas.md")
	}

	// Layer 2: user global. Probe both canonical `~/.r1/*.md` and legacy
	// `~/.stoke/*.md` per the dual-resolve rule (work-r1-rename.md §S1-5).
	// Layer order: canonical first, legacy second — matches the rest of
	// the skill loader's "later layers append" convention so canonical
	// wins only when present, and legacy remains a fallback that also
	// gets appended when both exist (user extends rather than replaces).
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		for _, dirName := range []string{r1dir.Canonical, r1dir.Legacy} {
			if cs, path, ok := readIfExists(filepath.Join(home, dirName, "coding-standards.md")); ok {
				csParts = append(csParts, cs)
				u.Sources = append(u.Sources, path)
			}
			if kg, path, ok := readIfExists(filepath.Join(home, dirName, "known-gotchas.md")); ok {
				kgParts = append(kgParts, kg)
				u.Sources = append(u.Sources, path)
			}
		}
	}

	// Layer 3: project-local. Same dual-probe: canonical then legacy.
	if repoRoot != "" {
		for _, dirName := range []string{r1dir.Canonical, r1dir.Legacy} {
			if cs, path, ok := readIfExists(filepath.Join(repoRoot, dirName, "coding-standards.md")); ok {
				csParts = append(csParts, cs)
				u.Sources = append(u.Sources, path)
			}
			if kg, path, ok := readIfExists(filepath.Join(repoRoot, dirName, "known-gotchas.md")); ok {
				kgParts = append(kgParts, kg)
				u.Sources = append(u.Sources, path)
			}
		}
	}

	u.CodingStandards = joinNonEmpty(csParts, "\n\n")
	u.KnownGotchas = joinNonEmpty(kgParts, "\n\n")
	return u
}

// readIfExists reads the file at path. Returns trimmed content, the
// path (for Sources), and true on success. Missing or unreadable files
// return false with no error surfaced to the caller.
func readIfExists(path string) (string, string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "", false
	}
	s := strings.TrimRight(string(b), " \t\n\r")
	if strings.TrimSpace(s) == "" {
		return "", "", false
	}
	return s, path, true
}

// joinNonEmpty joins only the non-empty segments so layers that don't
// contribute don't leave blank sections behind.
func joinNonEmpty(parts []string, sep string) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}

// PromptBlock returns the merged content formatted as a prompt-ready
// block, suitable for injection into any agent's system prompt. Format:
//
//	UNIVERSAL CODING STANDARDS (respect these in every file you write):
//	<coding-standards content>
//
//	KNOWN GOTCHAS (recurring LLM failure patterns — avoid these):
//	<known-gotchas content>
//
// Returns an empty string when both components are empty (loader
// couldn't find even the embedded defaults, which would be a build
// bug). Caller appends to system prompt, typically right after any
// role-specific instructions.
func (u UniversalContext) PromptBlock() string {
	cs := strings.TrimSpace(u.CodingStandards)
	kg := strings.TrimSpace(u.KnownGotchas)
	if cs == "" && kg == "" {
		return ""
	}
	var b strings.Builder
	if cs != "" {
		b.WriteString("UNIVERSAL CODING STANDARDS (respect these in every file you write):\n")
		b.WriteString(cs)
	}
	if kg != "" {
		if cs != "" {
			b.WriteString("\n\n")
		}
		b.WriteString("KNOWN GOTCHAS (recurring LLM failure patterns — avoid these):\n")
		b.WriteString(kg)
	}
	return b.String()
}

// ShortSources returns a human-friendly, comma-separated summary of
// which files contributed, for log lines. Collapses the two builtin
// entries into a single "builtin" token.
func (u UniversalContext) ShortSources() string {
	seen := map[string]bool{}
	out := make([]string, 0, len(u.Sources))
	home, _ := os.UserHomeDir()
	for _, s := range u.Sources {
		tok := s
		if strings.HasPrefix(s, sourceBuiltin+":") {
			tok = sourceBuiltin
		} else if home != "" && strings.HasPrefix(s, home+string(filepath.Separator)) {
			tok = "~" + strings.TrimPrefix(s, home)
		}
		if seen[tok] {
			continue
		}
		seen[tok] = true
		out = append(out, tok)
	}
	if len(out) == 0 {
		return "(none)"
	}
	return strings.Join(out, ", ")
}
