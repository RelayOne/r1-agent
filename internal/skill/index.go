package skill

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ericmacdougall/stoke/internal/tfidf"
)

// SkillIndex provides multi-axis semantic search over loaded skills.
// Built once at startup (or on Load()), it indexes every skill's
// content via TF-IDF so callers can query by topic, language, work
// type, or freeform text and get ranked results.
//
// The index is internal to the registry — callers use
// Registry.SearchSkills(query, topK) which delegates here.
type SkillIndex struct {
	tfidfIdx *tfidf.Index
	// byTopic maps topic tags (extracted from skill keywords) to
	// skill names for fast categorical lookup.
	byTopic map[string][]string
	// byLanguage maps language tags to skill names.
	byLanguage map[string][]string
	// byWorkType maps work-type tags (testing, deployment, security,
	// etc.) to skill names.
	byWorkType map[string][]string
	// nameToSkill maps skill name back to the Skill for retrieval.
	nameToSkill map[string]*Skill
}

// Known work-type keywords that classify skills by what kind of work
// they're about. Skills get tagged by scanning their keywords for
// these patterns.
var workTypeKeywords = map[string][]string{
	"testing":    {"test", "vitest", "jest", "pytest", "testing", "tdd", "coverage", "e2e", "playwright", "cypress", "detox"},
	"deployment": {"deploy", "docker", "kubernetes", "k8s", "ci", "cd", "pipeline", "eas", "vercel", "heroku"},
	"security":   {"security", "auth", "jwt", "oauth", "rbac", "xss", "csrf", "injection", "secrets"},
	"database":   {"postgres", "mysql", "sqlite", "redis", "mongo", "migration", "schema", "orm", "prisma"},
	"frontend":   {"react", "vue", "svelte", "next", "remix", "astro", "css", "tailwind", "component", "ui"},
	"mobile":     {"react-native", "expo", "ios", "android", "mobile", "native"},
	"api":        {"api", "rest", "graphql", "endpoint", "route", "middleware", "cors"},
	"devops":     {"monorepo", "pnpm", "npm", "yarn", "turbo", "nx", "lerna", "workspace"},
	"quality":    {"lint", "eslint", "prettier", "format", "code-quality", "review", "refactor"},
	"repair":     {"repair", "fix", "debug", "error", "failure", "retry", "recovery"},
}

// Known language keywords.
var languageKeywords = map[string][]string{
	"typescript": {"typescript", "ts", "tsx", "tsc", "tsconfig"},
	"javascript": {"javascript", "js", "jsx", "node", "npm", "pnpm"},
	"go":         {"go", "golang", "goroutine", "go.mod"},
	"rust":       {"rust", "cargo", "crate", "tokio"},
	"python":     {"python", "pip", "pytest", "django", "flask", "fastapi"},
}

// BuildIndex creates the multi-axis index from the registry's loaded
// skills. Called automatically by Load() and LoadBuiltins().
func (r *Registry) BuildIndex() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buildIndexLocked()
}

func (r *Registry) buildIndexLocked() {
	idx := &SkillIndex{
		tfidfIdx:    tfidf.NewIndex(),
		byTopic:     make(map[string][]string),
		byLanguage:  make(map[string][]string),
		byWorkType:  make(map[string][]string),
		nameToSkill: make(map[string]*Skill),
	}

	for name, s := range r.skills {
		idx.nameToSkill[name] = s

		// Index full content for TF-IDF semantic search.
		idx.tfidfIdx.AddDocument(name, s.Content)

		// Classify by keywords into categorical axes.
		allKeywords := strings.ToLower(strings.Join(s.Keywords, " ") + " " + s.Name + " " + s.Description)

		for lang, patterns := range languageKeywords {
			for _, p := range patterns {
				if strings.Contains(allKeywords, p) {
					idx.byLanguage[lang] = appendUnique(idx.byLanguage[lang], name)
					break
				}
			}
		}

		for wt, patterns := range workTypeKeywords {
			for _, p := range patterns {
				if strings.Contains(allKeywords, p) {
					idx.byWorkType[wt] = appendUnique(idx.byWorkType[wt], name)
					break
				}
			}
		}

		// Topic = each keyword is a topic.
		for _, kw := range s.Keywords {
			kw = strings.ToLower(strings.TrimSpace(kw))
			if kw != "" {
				idx.byTopic[kw] = appendUnique(idx.byTopic[kw], name)
			}
		}
	}

	// Compute IDF after all documents added.
	// tfidf.Index.computeIDF is called internally by Search().

	r.skillIndex = idx
}

// SearchSkills finds the top-K most relevant skills for the given
// query text. Uses TF-IDF for ranking, then enriches with categorical
// matches. Returns SkillMatch objects with the skill reference, match
// score, and which axes matched.
func (r *Registry) SearchSkills(query string, topK int) []SkillMatch {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.skillIndex == nil {
		return nil
	}
	if topK <= 0 {
		topK = 5
	}

	idx := r.skillIndex

	// TF-IDF search for semantic relevance.
	tfidfResults := idx.tfidfIdx.Search(query, topK*2) // oversample then merge

	// Categorical matches for the query terms.
	queryLower := strings.ToLower(query)
	catMatches := map[string][]string{} // skill name -> list of matching axes

	for lang, patterns := range languageKeywords {
		for _, p := range patterns {
			if strings.Contains(queryLower, p) {
				for _, sn := range idx.byLanguage[lang] {
					catMatches[sn] = append(catMatches[sn], "lang:"+lang)
				}
				break
			}
		}
	}
	for wt, patterns := range workTypeKeywords {
		for _, p := range patterns {
			if strings.Contains(queryLower, p) {
				for _, sn := range idx.byWorkType[wt] {
					catMatches[sn] = append(catMatches[sn], "work:"+wt)
				}
				break
			}
		}
	}

	// Merge TF-IDF scores with categorical boosts.
	scored := map[string]float64{}
	for _, r := range tfidfResults {
		scored[r.Path] = r.Score
	}
	for sn, axes := range catMatches {
		scored[sn] += float64(len(axes)) * 0.1 // small categorical boost
	}

	// Sort by score descending.
	type entry struct {
		name  string
		score float64
	}
	entries := make([]entry, 0, len(scored))
	for name, score := range scored {
		entries = append(entries, entry{name, score})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].score > entries[j].score
	})

	// Build results.
	results := make([]SkillMatch, 0, topK)
	for i, e := range entries {
		if i >= topK {
			break
		}
		s := idx.nameToSkill[e.name]
		if s == nil {
			continue
		}
		results = append(results, SkillMatch{
			Skill:      s,
			Score:      e.score,
			MatchAxes:  catMatches[e.name],
			Reason:     fmt.Sprintf("score=%.3f axes=%v", e.score, catMatches[e.name]),
		})
	}
	return results
}

// SkillMatch is a search result from SearchSkills.
type SkillMatch struct {
	Skill     *Skill
	Score     float64
	MatchAxes []string // e.g. ["lang:typescript", "work:testing"]
	Reason    string
}

// FormatSkillReferences renders a list of skill matches as a compact
// reference block suitable for injecting into a lead-dev briefing or
// task prompt. Shows the skill name, description, and gotchas — NOT
// the full content. The full content is available via read_file if
// the agent needs it.
func FormatSkillReferences(matches []SkillMatch) string {
	if len(matches) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("RELEVANT SKILLS (read these for conventions and gotchas):\n")
	for _, m := range matches {
		fmt.Fprintf(&b, "\n  📋 %s — %s\n", m.Skill.Name, m.Skill.Description)
		if m.Skill.Gotchas != "" {
			// Indent gotchas for readability.
			for _, line := range strings.Split(strings.TrimSpace(m.Skill.Gotchas), "\n") {
				fmt.Fprintf(&b, "    %s\n", line)
			}
		}
	}
	b.WriteString("\n")
	return b.String()
}

// appendUnique appends s to slice only if not already present.
func appendUnique(slice []string, s string) []string {
	for _, existing := range slice {
		if existing == s {
			return slice
		}
	}
	return append(slice, s)
}
