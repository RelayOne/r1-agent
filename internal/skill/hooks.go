// Package skill -- per-agent / per-scenario / per-phase hooks.
//
// hooks.go provides HookSet: a registry of narrow, per-role markdown
// hook files that get injected into specific LLM call sites on top of
// the universal context. Where UniversalContext carries project-wide
// baseline rules, hooks are targeted — "when the Phase-2 repair loop
// dispatches a worker, inject THIS additional guidance".
//
// Layering (same semantics as UniversalContext — later layers append,
// they do not replace):
//
//  1. Builtin defaults, embedded via //go:embed builtin/_hooks/**/*.md.
//  2. User overrides at $HOME/.stoke/hooks/<kind>/<name>.md.
//  3. Project overrides at <repoRoot>/.stoke/hooks/<kind>/<name>.md.
//
// Three kinds of hooks:
//
//   - agents   — per-role (worker-task-normal, judge-task-reviewer, ...)
//   - scenarios — situational (retry-attempt, fix-dag-session, ...)
//   - phases   — per phase transition (phase-1-4-integration-review, ...)
//
// At each LLM call site the caller resolves which (agent, scenario,
// phase) hooks apply and calls HookSet.PromptBlock(selectors...) to
// produce a single merged prompt-ready string. Empty when no hooks
// contribute content.
package skill

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Hook kind labels. These appear in HookSet.Kind and at several
// lookup / switch sites; centralizing them avoids drift between the
// definition and the switch arms.
const (
	hookKindAgents    = "agents"
	hookKindScenarios = "scenarios"
	hookKindPhases    = "phases"

	// sourceBuiltin marks hooks / skills loaded from the embedded
	// defaults rather than from disk. Used as both a path prefix
	// token and a short-label in HookSet.ShortSources, and as the
	// Source tag on embedded skill registry entries.
	sourceBuiltin = "builtin"
)

//go:embed all:builtin/_hooks
var embeddedHooksFS embed.FS

// Hook is one hook file's merged content addressable by (kind, name).
type Hook struct {
	Kind    string   // "agents", "scenarios", "phases"
	Name    string   // e.g. "worker-task-normal"
	Content string   // merged markdown after builtin + user + project layers
	Sources []string // paths that contributed (builtin:..., abs path, ...)
}

// HookSelector picks a hook from the set. Zero-value fields are ignored.
type HookSelector struct {
	Kind string
	Name string
}

// HookSet is the full loaded registry. Cheap to pass by value.
type HookSet struct {
	// hooks is keyed "<kind>/<name>".
	hooks map[string]Hook
	// Sources lists every path (builtin + layered) that contributed
	// content to any hook, for startup logging.
	Sources []string
	// Counts per kind — used by the startup log line.
	AgentCount    int
	ScenarioCount int
	PhaseCount    int
}

// LoadHookSet reads builtin (embedded) hooks, then layers user and
// project overrides. Never fails catastrophically — missing files at
// user/project layer are silent.
func LoadHookSet(repoRoot string) HookSet {
	h := HookSet{hooks: map[string]Hook{}}

	// Layer 1: builtin. Per-entry errors are tolerated — a single
	// unreadable embedded file must not blow up hook loading; the
	// docstring promises best-effort with never-catastrophic failure.
	// When WalkDir surfaces a per-path error, d is nil, so the d==nil
	// guard implicitly covers walkErr != nil without a bare
	// return-nil-after-nonnil-err pattern.
	_ = fs.WalkDir(embeddedHooksFS, "builtin/_hooks", func(p string, d fs.DirEntry, _ error) error {
		if d == nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(p, ".md") {
			return nil
		}
		rel := strings.TrimPrefix(p, "builtin/_hooks/")
		kind, name, ok := splitKindName(rel)
		if !ok {
			return nil
		}
		b, _ := embeddedHooksFS.ReadFile(p)
		if len(b) == 0 {
			return nil
		}
		content := strings.TrimRight(string(b), " \t\n\r")
		if strings.TrimSpace(content) == "" {
			return nil
		}
		key := kind + "/" + name
		src := "builtin:" + rel
		hk := h.hooks[key]
		hk.Kind = kind
		hk.Name = name
		hk.Content = appendLayer(hk.Content, content)
		hk.Sources = append(hk.Sources, src)
		h.hooks[key] = hk
		h.Sources = append(h.Sources, src)
		return nil
	})

	// Layer 2: user global.
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		h.mergeLayerFromDir(filepath.Join(home, ".stoke", "hooks"))
	}

	// Layer 3: project-local.
	if repoRoot != "" {
		h.mergeLayerFromDir(filepath.Join(repoRoot, ".stoke", "hooks"))
	}

	// Count hooks per kind.
	for _, hk := range h.hooks {
		switch hk.Kind {
		case hookKindAgents:
			h.AgentCount++
		case hookKindScenarios:
			h.ScenarioCount++
		case hookKindPhases:
			h.PhaseCount++
		}
	}
	return h
}

// mergeLayerFromDir walks a .stoke/hooks directory and appends each
// <kind>/<name>.md file's content onto the matching hook, or creates
// one if it wasn't in the builtin layer.
func (h *HookSet) mergeLayerFromDir(root string) {
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return
	}
	// Per-entry walk errors and per-file read errors are both
	// tolerated: user/project layer files are best-effort, and a
	// single unreadable hook file must not abort the whole load.
	// Walk surfaces per-path errors via a nil DirEntry, so the
	// d==nil guard implicitly handles the error case.
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, _ error) error {
		if d == nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(p, ".md") {
			return nil
		}
		// filepath.Rel only fails when one of its inputs is not
		// absolute in a platform-specific way; within a walk whose
		// root is a resolved directory this error class is
		// vanishingly rare and effectively means "skip this path".
		rel, _ := filepath.Rel(root, p)
		if rel == "" || rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		kind, name, ok := splitKindName(rel)
		if !ok {
			return nil
		}
		b, _ := os.ReadFile(p)
		if len(b) == 0 {
			return nil
		}
		content := strings.TrimRight(string(b), " \t\n\r")
		if strings.TrimSpace(content) == "" {
			return nil
		}
		key := kind + "/" + name
		hk := h.hooks[key]
		hk.Kind = kind
		hk.Name = name
		hk.Content = appendLayer(hk.Content, content)
		hk.Sources = append(hk.Sources, p)
		h.hooks[key] = hk
		h.Sources = append(h.Sources, p)
		return nil
	})
}

// splitKindName splits "agents/worker-task-normal.md" into
// ("agents", "worker-task-normal"). Returns false for anything that
// doesn't match the <kind>/<name>.md shape at the top level.
func splitKindName(rel string) (string, string, bool) {
	// Strip trailing ".md".
	if !strings.HasSuffix(rel, ".md") {
		return "", "", false
	}
	rel = strings.TrimSuffix(rel, ".md")
	parts := strings.Split(rel, "/")
	if len(parts) != 2 {
		return "", "", false
	}
	kind := parts[0]
	name := parts[1]
	if kind == "" || name == "" {
		return "", "", false
	}
	switch kind {
	case hookKindAgents, hookKindScenarios, hookKindPhases:
		return kind, name, true
	}
	return "", "", false
}

// appendLayer concatenates two layers of hook content with a blank
// line separator, skipping blanks.
func appendLayer(a, b string) string {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	switch {
	case a == "" && b == "":
		return ""
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "\n\n" + b
}

// Get returns the hook at (kind, name) if present. Empty Hook{} if not.
func (h HookSet) Get(kind, name string) Hook {
	if h.hooks == nil {
		return Hook{}
	}
	return h.hooks[kind+"/"+name]
}

// PromptBlock returns a formatted prompt-injectable string that
// concatenates one or more hooks. Empty selectors or missing hooks are
// silently skipped. Returns an empty string when no hooks contribute.
//
// Format:
//
//	AGENT HOOKS (role-specific guidance for this call):
//	<content>
//
//	SCENARIO HOOKS (situation-specific guidance):
//	<content>
//
//	PHASE HOOKS (phase-specific guidance):
//	<content>
func (h HookSet) PromptBlock(selections ...HookSelector) string {
	if len(selections) == 0 || len(h.hooks) == 0 {
		return ""
	}
	// Group by kind, preserving caller order within a kind but
	// keeping kinds in a stable (agents, scenarios, phases) sequence.
	grouped := map[string][]string{}
	order := []string{hookKindAgents, hookKindScenarios, hookKindPhases}
	for _, sel := range selections {
		if sel.Kind == "" || sel.Name == "" {
			continue
		}
		hk := h.Get(sel.Kind, sel.Name)
		if strings.TrimSpace(hk.Content) == "" {
			continue
		}
		grouped[sel.Kind] = append(grouped[sel.Kind], hk.Content)
	}
	if len(grouped) == 0 {
		return ""
	}
	var b strings.Builder
	for _, kind := range order {
		parts := grouped[kind]
		if len(parts) == 0 {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		switch kind {
		case hookKindAgents:
			b.WriteString("AGENT HOOKS (role-specific guidance for this call):\n")
		case hookKindScenarios:
			b.WriteString("SCENARIO HOOKS (situation-specific guidance):\n")
		case hookKindPhases:
			b.WriteString("PHASE HOOKS (phase-specific guidance):\n")
		}
		b.WriteString(strings.Join(parts, "\n\n"))
	}
	return b.String()
}

// Names returns a sorted list of "<kind>/<name>" keys for every
// loaded hook. Useful for coverage tests.
func (h HookSet) Names() []string {
	out := make([]string, 0, len(h.hooks))
	for k := range h.hooks {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ShortSources returns a compact, human-friendly summary of which
// directories contributed hook content, for startup log lines.
func (h HookSet) ShortSources() string {
	seen := map[string]bool{}
	out := make([]string, 0, len(h.Sources))
	home, _ := os.UserHomeDir()
	for _, s := range h.Sources {
		var tok string
		switch {
		case strings.HasPrefix(s, sourceBuiltin+":"):
			tok = sourceBuiltin
		case home != "" && strings.HasPrefix(s, home+string(filepath.Separator)):
			tok = "~/.stoke/hooks/"
		default:
			// Collapse to the hooks dir portion.
			if idx := strings.Index(s, ".stoke/hooks/"); idx >= 0 {
				tok = s[:idx] + ".stoke/hooks/"
			} else {
				tok = filepath.Dir(s)
			}
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

// LoadHooks is a convenience wrapper over LoadHookSet.
func LoadHooks(repoRoot string) HookSet { return LoadHookSet(repoRoot) }

// ConcatPromptBlocks joins multiple prompt blocks (e.g. the universal
// context block + hook block) with a blank line separator, skipping
// empty segments. Callers pass the result to the existing
// UniversalPromptBlock field on plan Input structs — no signature
// changes needed downstream.
func ConcatPromptBlocks(parts ...string) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, strings.TrimSpace(p))
		}
	}
	return strings.Join(out, "\n\n")
}
