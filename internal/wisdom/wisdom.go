// Package wisdom accumulates learnings across tasks in a build session.
//
// After each task completes (success or failure), the orchestrator records
// patterns, decisions, and gotchas. These are injected into subsequent
// task prompts so the same mistakes are not repeated.
package wisdom

import (
	"fmt"
	"strings"
	"sync"
)

// Category classifies a learning.
type Category int

const (
	Gotcha   Category = iota // failure or surprising behavior worth avoiding
	Decision                 // architectural or implementation choice made
	Pattern                  // recurring codebase convention discovered
)

func (c Category) String() string {
	switch c {
	case Gotcha:
		return "gotcha"
	case Decision:
		return "decision"
	case Pattern:
		return "pattern"
	default:
		return "unknown"
	}
}

// ParseCategory converts a string to a Category. Returns Pattern as default.
func ParseCategory(s string) Category {
	switch strings.ToLower(s) {
	case "gotcha":
		return Gotcha
	case "decision":
		return Decision
	case "pattern":
		return Pattern
	default:
		return Pattern
	}
}

// Learning is a single piece of knowledge extracted from a completed task.
type Learning struct {
	TaskID      string
	Category    Category
	Description string
	File        string // optional: relevant file path
}

// Store holds accumulated learnings for a build session.
type Store struct {
	mu        sync.Mutex
	learnings []Learning
}

// NewStore creates an empty wisdom store.
func NewStore() *Store {
	return &Store{}
}

// Record adds a learning to the store.
func (s *Store) Record(taskID string, l Learning) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l.TaskID = taskID
	s.learnings = append(s.learnings, l)
}

// Learnings returns a copy of all recorded learnings.
func (s *Store) Learnings() []Learning {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Learning, len(s.learnings))
	copy(out, s.learnings)
	return out
}

// maxPromptLen is the maximum character length for ForPrompt output.
const maxPromptLen = 500

// ForPrompt formats accumulated learnings as a markdown section suitable
// for injection into a task prompt. Output is capped at 500 characters.
// Gotchas are prioritized over decisions, which are prioritized over patterns.
func (s *Store) ForPrompt() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.learnings) == 0 {
		return ""
	}

	// Partition by category so gotchas come first.
	var gotchas, decisions, patterns []Learning
	for _, l := range s.learnings {
		switch l.Category {
		case Gotcha:
			gotchas = append(gotchas, l)
		case Decision:
			decisions = append(decisions, l)
		case Pattern:
			patterns = append(patterns, l)
		}
	}

	ordered := make([]Learning, 0, len(s.learnings))
	ordered = append(ordered, gotchas...)
	ordered = append(ordered, decisions...)
	ordered = append(ordered, patterns...)

	header := "## Learnings from previous tasks\n"
	var b strings.Builder
	b.WriteString(header)

	for _, l := range ordered {
		line := formatLine(l)
		// Check if adding this line would exceed the limit.
		// +1 for the newline.
		if b.Len()+len(line)+1 > maxPromptLen {
			break
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}

	return b.String()
}

// formatLine renders a single learning as a markdown bullet.
func formatLine(l Learning) string {
	suffix := ""
	if l.File != "" {
		suffix = fmt.Sprintf(" (%s)", l.File)
	}
	return fmt.Sprintf("- [%s] Task %s: %s%s", l.Category, l.TaskID, l.Description, suffix)
}
