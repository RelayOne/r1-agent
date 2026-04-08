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
	"time"
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
	TaskID         string
	Category       Category
	Description    string
	File           string // optional: relevant file path
	FailurePattern string // optional: failure fingerprint hash for cross-task dedup

	// Temporal validity: when this learning is considered active.
	// ValidFrom defaults to the time the learning was recorded.
	// ValidUntil is nil for learnings that never expire.
	ValidFrom  time.Time  `json:"valid_from,omitempty"`
	ValidUntil *time.Time `json:"valid_until,omitempty"` // nil = no expiry
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

// Record adds a learning to the store. Sets ValidFrom to now if not already set.
func (s *Store) Record(taskID string, l Learning) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l.TaskID = taskID
	if l.ValidFrom.IsZero() {
		l.ValidFrom = time.Now()
	}
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

// FindByPattern returns the first learning whose FailurePattern matches the
// given hash, or nil if no match. This enables cross-task failure prevention:
// if task B is about to hit a pattern that task A already failed on, we can
// inject a proactive warning into the prompt.
func (s *Store) FindByPattern(hash string) *Learning {
	if hash == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.learnings {
		if s.learnings[i].FailurePattern == hash {
			l := s.learnings[i]
			return &l
		}
	}
	return nil
}

// AsOf returns all learnings that were valid at the given time.
// A learning is valid at time t if: ValidFrom <= t AND (ValidUntil is nil OR ValidUntil > t).
func (s *Store) AsOf(t time.Time) []Learning {
	s.mu.Lock()
	defer s.mu.Unlock()

	var result []Learning
	for _, l := range s.learnings {
		if l.ValidFrom.After(t) {
			continue // not yet valid at time t
		}
		if l.ValidUntil != nil && !l.ValidUntil.After(t) {
			continue // already expired at time t
		}
		result = append(result, l)
	}
	return result
}

// Invalidate sets ValidUntil on a learning identified by its index, without
// rewriting history (append-only constraint preserved). The learning remains
// in the store but is excluded from AsOf queries after endedAt.
func (s *Store) Invalidate(taskID, description string, endedAt time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.learnings {
		if s.learnings[i].TaskID == taskID && s.learnings[i].Description == description {
			s.learnings[i].ValidUntil = &endedAt
			return true
		}
	}
	return false
}

// formatLine renders a single learning as a markdown bullet.
func formatLine(l Learning) string {
	suffix := ""
	if l.File != "" {
		suffix = fmt.Sprintf(" (%s)", l.File)
	}
	return fmt.Sprintf("- [%s] Task %s: %s%s", l.Category, l.TaskID, l.Description, suffix)
}
