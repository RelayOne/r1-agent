// Package memory provides persistent cross-session knowledge storage.
// Inspired by Devin's long-term learning and wisdom package:
//
// AI coding tools make the same mistakes across sessions. This package:
// - Persists learnings, patterns, and anti-patterns across sessions
// - Categorizes knowledge (gotcha, pattern, preference, codebase fact)
// - Retrieves relevant knowledge based on current task context
// - Decays stale knowledge over time (what was true 6 months ago may not be now)
// - Supports both project-level and user-level memory
package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Category classifies a memory entry.
type Category string

const (
	CatGotcha     Category = "gotcha"     // things that trip up agents
	CatPattern    Category = "pattern"    // successful patterns to reuse
	CatPreference Category = "preference" // user coding preferences
	CatFact       Category = "fact"       // codebase-specific facts
	CatAntiPattern Category = "anti_pattern" // things that don't work
	CatFix        Category = "fix"        // specific fixes that worked
)

// Entry is a single memory item.
type Entry struct {
	ID          string    `json:"id"`
	Category    Category  `json:"category"`
	Content     string    `json:"content"`
	Context     string    `json:"context,omitempty"`     // what triggered this learning
	File        string    `json:"file,omitempty"`        // related file pattern
	Tags        []string  `json:"tags,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	LastUsed    time.Time `json:"last_used"`
	UseCount    int       `json:"use_count"`
	Confidence  float64   `json:"confidence"`  // 0-1, decays over time
	Source      string    `json:"source,omitempty"` // "agent", "user", "auto"
}

// Store manages persistent memory.
type Store struct {
	mu       sync.RWMutex
	entries  []Entry
	path     string
	nextID   int
	maxAge   time.Duration // entries older than this decay faster
}

// Config for memory store.
type Config struct {
	Path   string        // file path for persistence
	MaxAge time.Duration // max age before decay (default 90 days)
}

// NewStore creates or loads a memory store.
func NewStore(cfg Config) (*Store, error) {
	if cfg.MaxAge == 0 {
		cfg.MaxAge = 90 * 24 * time.Hour
	}

	s := &Store{
		path:   cfg.Path,
		maxAge: cfg.MaxAge,
		nextID: 1,
	}

	if cfg.Path != "" {
		if err := s.load(); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("load memory: %w", err)
		}
	}

	return s, nil
}

// Remember adds a new memory entry.
func (s *Store) Remember(cat Category, content string, tags ...string) *Entry {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry := Entry{
		ID:         fmt.Sprintf("mem-%d", s.nextID),
		Category:   cat,
		Content:    content,
		Tags:       tags,
		CreatedAt:  time.Now(),
		LastUsed:   time.Now(),
		UseCount:   0,
		Confidence: 1.0,
		Source:     "agent",
	}
	s.nextID++
	s.entries = append(s.entries, entry)
	return &s.entries[len(s.entries)-1]
}

// RememberWithContext adds a memory with task context.
func (s *Store) RememberWithContext(cat Category, content, context, file string, tags ...string) *Entry {
	s.Remember(cat, content, tags...)
	s.mu.Lock()
	defer s.mu.Unlock()
	// Find the entry we just added (last one)
	idx := len(s.entries) - 1
	s.entries[idx].Context = context
	s.entries[idx].File = file
	return &s.entries[idx]
}

// Recall retrieves relevant memories for a given context.
func (s *Store) Recall(query string, limit int) []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 {
		limit = 10
	}

	type scored struct {
		entry Entry
		score float64
	}

	queryLower := strings.ToLower(query)
	queryWords := strings.Fields(queryLower)

	var matches []scored
	for _, e := range s.entries {
		score := s.relevanceScore(e, queryWords)
		if score > 0 {
			matches = append(matches, scored{entry: e, score: score})
		}
	}

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].score > matches[j].score
	})

	if len(matches) > limit {
		matches = matches[:limit]
	}

	result := make([]Entry, len(matches))
	for i, m := range matches {
		result[i] = m.entry
	}
	return result
}

// RecallByCategory returns all memories of a given category.
func (s *Store) RecallByCategory(cat Category) []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []Entry
	for _, e := range s.entries {
		if e.Category == cat {
			result = append(result, e)
		}
	}
	return result
}

// RecallForFile returns memories related to a specific file pattern.
func (s *Store) RecallForFile(file string) []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []Entry
	for _, e := range s.entries {
		if e.File != "" && (strings.Contains(file, e.File) || strings.Contains(e.File, filepath.Base(file))) {
			result = append(result, e)
		}
	}
	return result
}

// MarkUsed increments usage count and refreshes last-used time.
func (s *Store) MarkUsed(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.entries {
		if s.entries[i].ID == id {
			s.entries[i].UseCount++
			s.entries[i].LastUsed = time.Now()
			return
		}
	}
}

// Forget removes a memory entry.
func (s *Store) Forget(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.entries {
		if s.entries[i].ID == id {
			s.entries = append(s.entries[:i], s.entries[i+1:]...)
			return
		}
	}
}

// ForPrompt generates a memory injection for an LLM prompt.
func (s *Store) ForPrompt(query string, budget int) string {
	memories := s.Recall(query, 20)
	if len(memories) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("## Relevant learnings from previous sessions\n\n")
	tokens := 15 // header tokens

	// Prioritize: gotchas > anti-patterns > fixes > patterns > facts > preferences
	sort.Slice(memories, func(i, j int) bool {
		return categoryPriority(memories[i].Category) < categoryPriority(memories[j].Category)
	})

	for _, m := range memories {
		line := fmt.Sprintf("- [%s] %s", m.Category, m.Content)
		if m.File != "" {
			line += fmt.Sprintf(" (re: %s)", m.File)
		}
		line += "\n"

		lineTokens := (len(line) + 3) / 4
		if budget > 0 && tokens+lineTokens > budget {
			break
		}
		b.WriteString(line)
		tokens += lineTokens
	}

	return b.String()
}

// Decay reduces confidence of old, unused entries.
func (s *Store) Decay() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	decayed := 0
	now := time.Now()
	for i := range s.entries {
		age := now.Sub(s.entries[i].LastUsed)
		if age > s.maxAge {
			// Decay proportional to age beyond maxAge
			factor := 1.0 - (age.Hours()-s.maxAge.Hours())/(s.maxAge.Hours()*2)
			if factor < 0.1 {
				factor = 0.1
			}
			if s.entries[i].Confidence > factor {
				s.entries[i].Confidence = factor
				decayed++
			}
		}
	}
	return decayed
}

// Prune removes entries with very low confidence.
func (s *Store) Prune(minConfidence float64) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	var kept []Entry
	removed := 0
	for _, e := range s.entries {
		if e.Confidence >= minConfidence {
			kept = append(kept, e)
		} else {
			removed++
		}
	}
	s.entries = kept
	return removed
}

// Count returns the number of memory entries.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries)
}

// Save persists to disk.
func (s *Store) Save() error {
	if s.path == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(s.entries, "", "  ")
	if err != nil {
		return err
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &s.entries); err != nil {
		return err
	}
	// Find max ID for nextID
	for _, e := range s.entries {
		var num int
		fmt.Sscanf(e.ID, "mem-%d", &num)
		if num >= s.nextID {
			s.nextID = num + 1
		}
	}
	return nil
}

func (s *Store) relevanceScore(e Entry, queryWords []string) float64 {
	score := 0.0
	contentLower := strings.ToLower(e.Content)
	contextLower := strings.ToLower(e.Context)
	fileLower := strings.ToLower(e.File)
	tagsLower := strings.ToLower(strings.Join(e.Tags, " "))

	for _, word := range queryWords {
		if strings.Contains(contentLower, word) {
			score += 2.0
		}
		if strings.Contains(contextLower, word) {
			score += 1.0
		}
		if strings.Contains(fileLower, word) {
			score += 1.5
		}
		if strings.Contains(tagsLower, word) {
			score += 1.0
		}
	}

	// Boost by confidence and recency
	score *= e.Confidence

	// Boost frequently used entries
	if e.UseCount > 0 {
		score *= 1.0 + float64(e.UseCount)*0.1
	}

	return score
}

func categoryPriority(c Category) int {
	switch c {
	case CatGotcha:
		return 0
	case CatAntiPattern:
		return 1
	case CatFix:
		return 2
	case CatPattern:
		return 3
	case CatFact:
		return 4
	case CatPreference:
		return 5
	}
	return 6
}
