// Package skill implements a reusable workflow pattern system.
// Inspired by OmX's skill system: reusable markdown-based workflow templates
// that are auto-injected by keyword match. Skills are stored as markdown files
// in .stoke/skills/ (project) and ~/.stoke/skills/ (user), with project skills
// taking priority.
//
// Key patterns from OmX:
// - 36 built-in skills (ralph, team, deep-interview, build-fix, tdd, etc.)
// - Skills are directories with an index.md and optional config
// - Keyword-triggered auto-injection into prompts
// - Skill-scoped MCP servers (cleared after execution)
package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Skill is a reusable workflow pattern.
type Skill struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Keywords    []string `json:"keywords"`    // trigger words for auto-injection
	Content     string   `json:"content"`     // the markdown template
	Source      string   `json:"source"`      // "project" or "user"
	Path        string   `json:"path"`        // file path
	Priority    int      `json:"priority"`    // higher = matched first
}

// Registry manages skill discovery, loading, and matching.
type Registry struct {
	mu             sync.RWMutex
	skills         map[string]*Skill
	dirs           []string // search directories in priority order
	builtinsLoaded bool     // true after LoadBuiltins() has been called
}

// NewRegistry creates a skill registry that searches the given directories.
// Directories are searched in order; first match wins.
func NewRegistry(dirs ...string) *Registry {
	return &Registry{
		skills: make(map[string]*Skill),
		dirs:   dirs,
	}
}

// DefaultRegistry creates a registry with project and user skill directories,
// and loads built-in skills embedded in the binary.
func DefaultRegistry(projectRoot string) *Registry {
	home, _ := os.UserHomeDir()
	dirs := []string{
		filepath.Join(projectRoot, ".stoke", "skills"), // project (highest priority)
	}
	if home != "" {
		dirs = append(dirs, filepath.Join(home, ".stoke", "skills")) // user
	}
	r := NewRegistry(dirs...)
	// Load embedded built-in skills (lowest priority, won't overwrite project/user).
	_ = r.LoadBuiltins()
	return r
}

// Load discovers and loads all skills from registered directories.
// Project skills take highest priority, followed by user skills, then builtins.
// Builtins are automatically reloaded after clearing so they remain available.
func (r *Registry) Load() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.skills = make(map[string]*Skill)

	// Reload builtins first (lowest priority, will be overwritten by project/user).
	if r.builtinsLoaded {
		r.loadBuiltinsLocked()
	}

	for i, dir := range r.dirs {
		source := "user"
		if i == 0 {
			source = "project"
		}

		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("read skills dir %s: %w", dir, err)
		}

		for _, entry := range entries {
			name := entry.Name()

			if entry.IsDir() {
				// Skill directory: look for index.md
				indexPath := filepath.Join(dir, name, "index.md")
				content, err := os.ReadFile(indexPath)
				if err != nil {
					continue
				}
				// Overwrite builtins but not higher-priority project/user skills
				if existing, exists := r.skills[name]; exists && existing.Source != "builtin" {
					continue
				}
				r.skills[name] = parseSkill(name, string(content), source, indexPath, len(r.dirs)-i)
			} else if strings.HasSuffix(name, ".md") {
				// Skill file: name is filename without extension
				skillName := strings.TrimSuffix(name, ".md")
				content, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil {
					continue
				}
				if existing, exists := r.skills[skillName]; exists && existing.Source != "builtin" {
					continue
				}
				r.skills[skillName] = parseSkill(skillName, string(content), source, filepath.Join(dir, name), len(r.dirs)-i)
			}
		}
	}
	return nil
}

// Get returns a skill by name.
func (r *Registry) Get(name string) *Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.skills[name]
}

// List returns all loaded skills sorted by name.
func (r *Registry) List() []*Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*Skill
	for _, s := range r.skills {
		result = append(result, s)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}

// Match finds skills whose keywords appear in the given text.
// Returns matches sorted by priority (highest first).
func (r *Registry) Match(text string) []*Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()

	lower := strings.ToLower(text)
	var matches []*Skill
	for _, s := range r.skills {
		for _, kw := range s.Keywords {
			if strings.Contains(lower, strings.ToLower(kw)) {
				matches = append(matches, s)
				break
			}
		}
	}

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].Priority > matches[j].Priority
	})
	return matches
}

// MatchOne returns the best matching skill for the given text, or nil.
func (r *Registry) MatchOne(text string) *Skill {
	matches := r.Match(text)
	if len(matches) > 0 {
		return matches[0]
	}
	return nil
}

// InjectPrompt augments a prompt with matching skill content.
// Returns the original prompt with skill instructions prepended.
func (r *Registry) InjectPrompt(prompt string) string {
	matches := r.Match(prompt)
	if len(matches) == 0 {
		return prompt
	}

	var sb strings.Builder
	for _, s := range matches {
		sb.WriteString(fmt.Sprintf("## Skill: %s\n\n", s.Name))
		sb.WriteString(s.Content)
		sb.WriteString("\n\n---\n\n")
	}
	sb.WriteString(prompt)
	return sb.String()
}

// truncateToFirstSection returns content up to the first ## heading after the
// initial # heading. This extracts the "gotchas only" portion of a skill.
func truncateToFirstSection(content string) string {
	lines := strings.Split(content, "\n")
	pastTitle := false
	var sb strings.Builder
	for _, line := range lines {
		if strings.HasPrefix(line, "# ") && !pastTitle {
			pastTitle = true
			sb.WriteString(line)
			sb.WriteByte('\n')
			continue
		}
		if pastTitle && strings.HasPrefix(line, "## ") {
			break
		}
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n") + "\n"
}

// estimateTokens returns a rough token count using ~4 chars per token.
func estimateTokens(s string) int {
	n := len(s) / 4
	if n == 0 && len(s) > 0 {
		return 1
	}
	return n
}

// InjectPromptBudgeted augments a prompt with matching skill content,
// respecting a token budget. Skills are prioritized by match quality and
// truncated to fit within budget. Returns the augmented prompt and the
// number of skills injected.
func (r *Registry) InjectPromptBudgeted(prompt string, budgetTokens int) (string, int) {
	matches := r.Match(prompt)
	if len(matches) == 0 {
		return prompt, 0
	}

	remaining := budgetTokens
	var parts []string
	count := 0

	for _, s := range matches {
		if remaining <= 0 {
			break
		}

		header := fmt.Sprintf("## Skill: %s\n\n", s.Name)
		separator := "\n\n---\n\n"
		overhead := estimateTokens(header) + estimateTokens(separator)

		fullContent := s.Content
		fullTokens := estimateTokens(fullContent) + overhead

		if fullTokens <= remaining {
			parts = append(parts, header+fullContent+separator)
			remaining -= fullTokens
			count++
			continue
		}

		// Try truncated version (first section only)
		truncated := truncateToFirstSection(s.Content)
		truncTokens := estimateTokens(truncated) + overhead

		if truncTokens <= remaining {
			parts = append(parts, header+truncated+separator)
			remaining -= truncTokens
			count++
		}
	}

	if count == 0 {
		return prompt, 0
	}

	var sb strings.Builder
	for _, p := range parts {
		sb.WriteString(p)
	}
	sb.WriteString(prompt)
	return sb.String(), count
}

// Add registers a new skill. If project dir exists, saves to project skills.
func (r *Registry) Add(name, description, content string, keywords []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.dirs) == 0 {
		return fmt.Errorf("no skill directories configured")
	}

	dir := r.dirs[0] // project dir
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	// Build markdown content with frontmatter
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# %s\n\n", name))
	if description != "" {
		sb.WriteString(fmt.Sprintf("> %s\n\n", description))
	}
	if len(keywords) > 0 {
		sb.WriteString(fmt.Sprintf("<!-- keywords: %s -->\n\n", strings.Join(keywords, ", ")))
	}
	sb.WriteString(content)

	path := filepath.Join(dir, name+".md")
	if err := os.WriteFile(path, []byte(sb.String()), 0644); err != nil {
		return err
	}

	r.skills[name] = &Skill{
		Name:        name,
		Description: description,
		Keywords:    keywords,
		Content:     sb.String(),
		Source:      "project",
		Path:        path,
		Priority:    len(r.dirs),
	}
	return nil
}

// Remove deletes a skill by name from the first directory that contains it.
func (r *Registry) Remove(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	s, exists := r.skills[name]
	if !exists {
		return fmt.Errorf("skill %q not found", name)
	}
	if err := os.Remove(s.Path); err != nil && !os.IsNotExist(err) {
		return err
	}
	delete(r.skills, name)
	return nil
}

// SuggestSimilar returns skill names similar to the input (Levenshtein distance ≤ 2).
// Inspired by claw-code-parity's slash command suggestions.
func (r *Registry) SuggestSimilar(name string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var suggestions []string
	for n := range r.skills {
		if levenshtein(name, n) <= 2 {
			suggestions = append(suggestions, n)
		}
	}
	sort.Strings(suggestions)
	return suggestions
}

// --- Internal ---

// parseSkill extracts metadata from markdown content.
func parseSkill(name, content, source, path string, priority int) *Skill {
	s := &Skill{
		Name:     name,
		Content:  content,
		Source:   source,
		Path:     path,
		Priority: priority,
	}

	// Extract description from first blockquote
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "> ") {
			s.Description = strings.TrimPrefix(line, "> ")
			break
		}
	}

	// Extract keywords from HTML comment
	for _, line := range strings.Split(content, "\n") {
		if strings.Contains(line, "<!-- keywords:") {
			kw := strings.TrimPrefix(line, "<!-- keywords:")
			kw = strings.TrimSuffix(kw, "-->")
			kw = strings.TrimSpace(kw)
			for _, k := range strings.Split(kw, ",") {
				k = strings.TrimSpace(k)
				if k != "" {
					s.Keywords = append(s.Keywords, k)
				}
			}
			break
		}
	}

	// If no keywords, use the name and words from description
	if len(s.Keywords) == 0 {
		s.Keywords = append(s.Keywords, name)
		if s.Description != "" {
			words := strings.Fields(s.Description)
			for _, w := range words {
				if len(w) > 4 {
					s.Keywords = append(s.Keywords, strings.ToLower(w))
				}
			}
		}
	}

	return s
}

// levenshtein computes the edit distance between two strings.
func levenshtein(a, b string) int {
	la, lb := len(a), len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}

	// Use single row optimization
	prev := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}

	for i := 1; i <= la; i++ {
		curr := make([]int, lb+1)
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min3(curr[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev = curr
	}
	return prev[lb]
}

func min3(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}
