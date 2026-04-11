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
	Name            string            `json:"name"`
	Description     string            `json:"description"`
	Keywords        []string          `json:"keywords"`          // trigger words for auto-injection
	Triggers        []string          `json:"triggers"`          // explicit trigger phrases from YAML frontmatter
	AllowedTools    []string          `json:"allowed_tools"`     // tool whitelist from YAML frontmatter
	Content         string            `json:"content"`           // the markdown template
	Gotchas         string            `json:"gotchas"`           // extracted "Gotchas" section for compressed injection
	References      map[string]string `json:"references"`        // filename → content for progressive disclosure
	Source          string            `json:"source"`            // "project", "user", or "builtin"
	Path            string            `json:"path"`              // file path
	Priority        int               `json:"priority"`          // higher = matched first
	EstTokens       int               `json:"est_tokens"`        // estimated token count for budgeting
	EstGotchaTokens int               `json:"est_gotcha_tokens"` // token count of just the Gotchas section
}

// Registry manages skill discovery, loading, and matching.
type Registry struct {
	mu             sync.RWMutex
	skills         map[string]*Skill
	dirs           []string // search directories in priority order
	builtinsLoaded bool     // true after LoadBuiltins() has been called
	skillIndex     *SkillIndex // multi-axis semantic index, rebuilt on Load/LoadBuiltins
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
				// Skill directory: look for SKILL.md (Trail of Bits) or index.md (legacy)
				candidates := []string{
					filepath.Join(dir, name, "SKILL.md"),
					filepath.Join(dir, name, "index.md"),
				}
				var content []byte
				var skillPath string
				for _, c := range candidates {
					if data, err := os.ReadFile(c); err == nil {
						content = data
						skillPath = c
						break
					}
				}
				if skillPath == "" {
					continue
				}
				// Overwrite builtins but not higher-priority project/user skills
				if existing, exists := r.skills[name]; exists && existing.Source != "builtin" {
					continue
				}
				r.skills[name] = parseSkill(name, string(content), source, skillPath, len(r.dirs)-i)

				// Load progressive disclosure references from references/ subdir
				refsDir := filepath.Join(dir, name, "references")
				if refEntries, refErr := os.ReadDir(refsDir); refErr == nil {
					for _, ref := range refEntries {
						if !ref.IsDir() && strings.HasSuffix(ref.Name(), ".md") {
							if data, err := os.ReadFile(filepath.Join(refsDir, ref.Name())); err == nil {
								key := strings.TrimSuffix(ref.Name(), ".md")
								r.skills[name].References[key] = string(data)
							}
						}
					}
				}
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
	// Rebuild the multi-axis index so SearchSkills works against the
	// freshly loaded skill set.
	r.buildIndexLocked()
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

// InjectionTier classifies how much of a skill to include.
type InjectionTier int

const (
	TierFull    InjectionTier = iota // entire content
	TierGotchas                      // gotchas section only
	TierName                         // just name + description
)

// SkillSelection represents a chosen skill with how it should be rendered.
type SkillSelection struct {
	Skill  *Skill
	Tier   InjectionTier
	Reason string // for audit/debug: why was this selected
}

// InjectPromptBudgeted selects skills for the given prompt + repo profile, respecting
// the token budget. Returns the augmented prompt with skills injected.
//
// Selection priority (in order, until budget exhausted):
//  1. Always-on skills (name "agent-discipline" or keyword "always")
//  2. Repo-stack-matched skills (top 2, full content) — passed via stackMatches
//  3. Keyword-matched skills (top 3, gotchas only)
//
// The returned prompt has skills wrapped in <skills>...</skills> XML tags.
func (r *Registry) InjectPromptBudgeted(prompt string, stackMatches []string, tokenBudget int) (string, []SkillSelection) {
	if tokenBudget <= 0 {
		tokenBudget = 3000
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	used := 0
	var selected []SkillSelection
	seen := make(map[string]bool)

	// Helper to add a skill if it fits in budget
	add := func(s *Skill, tier InjectionTier, reason string) {
		if seen[s.Name] {
			return
		}
		cost := s.EstTokens
		if tier == TierGotchas {
			cost = s.EstGotchaTokens
		} else if tier == TierName {
			cost = (len(s.Name) + len(s.Description)) / 4
			if cost == 0 {
				cost = 1
			}
		}
		if cost == 0 {
			return
		}
		if used+cost > tokenBudget {
			return
		}
		used += cost
		seen[s.Name] = true
		selected = append(selected, SkillSelection{Skill: s, Tier: tier, Reason: reason})
	}

	// Tier 1: always-on
	for _, s := range r.skills {
		if s.Name == "agent-discipline" || hasKeyword(s.Keywords, "always") {
			add(s, TierFull, "always-on")
		}
	}

	// Tier 2: repo stack matches (top 2, full content)
	stackCount := 0
	for _, name := range stackMatches {
		if stackCount >= 2 {
			break
		}
		if s := r.skills[name]; s != nil {
			add(s, TierFull, "repo-stack")
			stackCount++
		}
	}

	// Tier 3: keyword matches (top 3, gotchas only)
	matches := r.matchInternal(prompt)
	keywordCount := 0
	for _, s := range matches {
		if keywordCount >= 3 {
			break
		}
		if s.EstGotchaTokens == 0 {
			continue // skip skills without gotchas section in tier 3
		}
		add(s, TierGotchas, "keyword-match")
		keywordCount++
	}

	if len(selected) == 0 {
		return prompt, nil
	}

	var sb strings.Builder
	sb.WriteString("<skills>\n")
	for _, sel := range selected {
		sb.WriteString(fmt.Sprintf("## Skill: %s\n\n", sel.Skill.Name))
		switch sel.Tier {
		case TierFull:
			sb.WriteString(sel.Skill.Content)
		case TierGotchas:
			sb.WriteString("(gotchas only)\n\n")
			sb.WriteString(sel.Skill.Gotchas)
		case TierName:
			sb.WriteString(sel.Skill.Description)
		}
		sb.WriteString("\n\n---\n\n")
	}
	sb.WriteString("</skills>\n\n")
	sb.WriteString(prompt)

	return sb.String(), selected
}

// matchInternal is the unlocked version of Match. Caller must hold r.mu.RLock().
func (r *Registry) matchInternal(text string) []*Skill {
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

// parseSkill extracts metadata from skill content. Supports both YAML frontmatter
// (Trail of Bits / Claude Code SKILL.md format) and the legacy HTML comment format.
func parseSkill(name, content, source, path string, priority int) *Skill {
	s := &Skill{
		Name:       name,
		Content:    content,
		Source:     source,
		Path:       path,
		Priority:   priority,
		References: make(map[string]string),
	}

	body := content

	// Parse YAML frontmatter if present (--- delimited block at start)
	if strings.HasPrefix(content, "---\n") {
		end := strings.Index(content[4:], "\n---\n")
		if end > 0 {
			frontmatter := content[4 : 4+end]
			body = content[4+end+5:]
			parseFrontmatter(frontmatter, s)
		}
	}

	// Extract description from first blockquote (if not set by frontmatter)
	if s.Description == "" {
		for _, line := range strings.Split(body, "\n") {
			if strings.HasPrefix(line, "> ") {
				s.Description = strings.TrimPrefix(line, "> ")
				break
			}
		}
	}

	// Extract keywords from HTML comment (legacy format, still supported)
	if len(s.Keywords) == 0 {
		for _, line := range strings.Split(body, "\n") {
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
	}

	// Fallback: derive keywords from name + description words
	if len(s.Keywords) == 0 {
		s.Keywords = append(s.Keywords, name)
		for _, w := range strings.Fields(s.Description) {
			if len(w) > 4 {
				s.Keywords = append(s.Keywords, strings.ToLower(w))
			}
		}
	}

	// Triggers (explicit) merge into keywords for matching
	for _, t := range s.Triggers {
		s.Keywords = append(s.Keywords, strings.ToLower(t))
	}

	// Extract Gotchas section for compressed injection
	s.Gotchas = extractSection(body, "Gotchas")

	// Estimate tokens (rough: 1 token ≈ 4 characters for English)
	s.EstTokens = len(body) / 4
	if s.EstTokens == 0 && len(body) > 0 {
		s.EstTokens = 1
	}
	s.EstGotchaTokens = len(s.Gotchas) / 4

	return s
}

// parseFrontmatter is a minimal YAML frontmatter parser for SKILL.md files.
// It handles the fields we care about: name, description, triggers, allowed-tools, keywords.
func parseFrontmatter(fm string, s *Skill) {
	var inList string
	for _, line := range strings.Split(fm, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			inList = ""
			continue
		}

		if inList != "" {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "- ") {
				v := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
				v = strings.Trim(v, `"'`)
				switch inList {
				case "triggers":
					s.Triggers = append(s.Triggers, v)
				case "allowed-tools":
					s.AllowedTools = append(s.AllowedTools, v)
				case "keywords":
					s.Keywords = append(s.Keywords, v)
				}
				continue
			}
			inList = ""
		}

		idx := strings.Index(line, ":")
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		val = strings.Trim(val, `"'`)

		switch key {
		case "name":
			if val != "" {
				s.Name = val
			}
		case "description":
			if val != "" {
				s.Description = val
			}
		case "triggers":
			if val != "" {
				// Inline list: triggers: [foo, bar]
				inline := strings.TrimSpace(val)
				if strings.HasPrefix(inline, "[") && strings.HasSuffix(inline, "]") {
					inner := strings.TrimSuffix(strings.TrimPrefix(inline, "["), "]")
					for _, v := range strings.Split(inner, ",") {
						v = strings.TrimSpace(v)
						v = strings.Trim(v, `"'`)
						if v != "" {
							s.Triggers = append(s.Triggers, v)
						}
					}
				}
			} else {
				inList = "triggers"
			}
		case "allowed-tools":
			if val != "" {
				// Inline list
				inline := strings.TrimSpace(val)
				if strings.HasPrefix(inline, "[") && strings.HasSuffix(inline, "]") {
					inner := strings.TrimSuffix(strings.TrimPrefix(inline, "["), "]")
					for _, v := range strings.Split(inner, ",") {
						v = strings.TrimSpace(v)
						v = strings.Trim(v, `"'`)
						if v != "" {
							s.AllowedTools = append(s.AllowedTools, v)
						}
					}
				}
			} else {
				inList = "allowed-tools"
			}
		case "keywords":
			if val != "" {
				// Inline list
				inline := strings.TrimSpace(val)
				if strings.HasPrefix(inline, "[") && strings.HasSuffix(inline, "]") {
					inner := strings.TrimSuffix(strings.TrimPrefix(inline, "["), "]")
					for _, v := range strings.Split(inner, ",") {
						v = strings.TrimSpace(v)
						v = strings.Trim(v, `"'`)
						if v != "" {
							s.Keywords = append(s.Keywords, v)
						}
					}
				}
			} else {
				inList = "keywords"
			}
		}
	}
}

// extractSection finds a section by H2 header name and returns its body up to
// the next H2 or end of document. Returns empty string if not found.
func extractSection(body, name string) string {
	lines := strings.Split(body, "\n")
	var out []string
	inSection := false
	headerLower := strings.ToLower("## " + name)
	for _, line := range lines {
		if strings.HasPrefix(strings.ToLower(line), headerLower) {
			inSection = true
			continue
		}
		if inSection {
			if strings.HasPrefix(line, "## ") {
				break
			}
			out = append(out, line)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func hasKeyword(keywords []string, target string) bool {
	for _, k := range keywords {
		if k == target {
			return true
		}
	}
	return false
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
