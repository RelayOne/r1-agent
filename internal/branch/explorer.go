// Package branch implements conversation branching for exploring multiple solution paths.
// Inspired by claw-code's conversation forking and OpenHands' multi-path exploration:
//
// When an agent hits an ambiguous choice (two possible fix approaches, different
// architectures), branching creates parallel conversation forks:
// - Each branch gets its own message history and state
// - Branches can be scored and the best one selected
// - Failed branches are discarded without polluting the main conversation
//
// This is the "explore/exploit" pattern: invest in exploration when uncertain,
// then exploit the best result.
package branch

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// Message is a conversation turn.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Branch represents a conversation fork.
type Branch struct {
	ID          string    `json:"id"`
	ParentID    string    `json:"parent_id,omitempty"`
	Name        string    `json:"name"`
	Messages    []Message `json:"messages"`
	Score       float64   `json:"score"`
	Status      string    `json:"status"` // "active", "completed", "failed", "selected"
	CreatedAt   time.Time `json:"created_at"`
	CompletedAt time.Time `json:"completed_at,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// Explorer manages conversation branches.
type Explorer struct {
	mu       sync.Mutex
	branches map[string]*Branch
	trunk    []Message // shared message history before the fork
	nextID   int
}

// NewExplorer creates an explorer with shared trunk messages.
func NewExplorer(trunk []Message) *Explorer {
	return &Explorer{
		branches: make(map[string]*Branch),
		trunk:    trunk,
	}
}

// Fork creates a new branch from the trunk.
func (e *Explorer) Fork(name string) *Branch {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.nextID++
	id := fmt.Sprintf("branch-%d", e.nextID)

	// Copy trunk messages into the branch
	msgs := make([]Message, len(e.trunk))
	copy(msgs, e.trunk)

	b := &Branch{
		ID:        id,
		Name:      name,
		Messages:  msgs,
		Status:    "active",
		CreatedAt: time.Now(),
		Metadata:  make(map[string]any),
	}
	e.branches[id] = b
	return b
}

// ForkFrom creates a branch that extends another branch.
func (e *Explorer) ForkFrom(parentID, name string) (*Branch, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	parent, ok := e.branches[parentID]
	if !ok {
		return nil, fmt.Errorf("parent branch %s not found", parentID)
	}

	e.nextID++
	id := fmt.Sprintf("branch-%d", e.nextID)

	msgs := make([]Message, len(parent.Messages))
	copy(msgs, parent.Messages)

	b := &Branch{
		ID:        id,
		ParentID:  parentID,
		Name:      name,
		Messages:  msgs,
		Status:    "active",
		CreatedAt: time.Now(),
		Metadata:  make(map[string]any),
	}
	e.branches[id] = b
	return b, nil
}

// Append adds a message to a branch.
func (e *Explorer) Append(branchID string, msg Message) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	b, ok := e.branches[branchID]
	if !ok {
		return fmt.Errorf("branch %s not found", branchID)
	}
	if b.Status != "active" {
		return fmt.Errorf("branch %s is %s, not active", branchID, b.Status)
	}
	b.Messages = append(b.Messages, msg)
	return nil
}

// Complete marks a branch as completed with a score.
func (e *Explorer) Complete(branchID string, score float64) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	b, ok := e.branches[branchID]
	if !ok {
		return fmt.Errorf("branch %s not found", branchID)
	}
	b.Status = "completed"
	b.Score = score
	b.CompletedAt = time.Now()
	return nil
}

// Fail marks a branch as failed.
func (e *Explorer) Fail(branchID string, reason string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	b, ok := e.branches[branchID]
	if !ok {
		return fmt.Errorf("branch %s not found", branchID)
	}
	b.Status = "failed"
	b.Metadata["failure_reason"] = reason
	b.CompletedAt = time.Now()
	return nil
}

// Best returns the completed branch with the highest score.
func (e *Explorer) Best() *Branch {
	e.mu.Lock()
	defer e.mu.Unlock()

	var best *Branch
	for _, b := range e.branches {
		if b.Status == "completed" {
			if best == nil || b.Score > best.Score {
				best = b
			}
		}
	}
	return best
}

// Select marks the best branch as selected and returns it.
func (e *Explorer) Select() *Branch {
	best := e.Best()
	if best == nil {
		return nil
	}
	e.mu.Lock()
	best.Status = "selected"
	e.mu.Unlock()
	return best
}

// Get retrieves a branch by ID.
func (e *Explorer) Get(id string) *Branch {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.branches[id]
}

// Active returns all active branches.
func (e *Explorer) Active() []*Branch {
	e.mu.Lock()
	defer e.mu.Unlock()

	var active []*Branch
	for _, b := range e.branches {
		if b.Status == "active" {
			active = append(active, b)
		}
	}
	return active
}

// All returns all branches.
func (e *Explorer) All() []*Branch {
	e.mu.Lock()
	defer e.mu.Unlock()

	var all []*Branch
	for _, b := range e.branches {
		all = append(all, b)
	}
	return all
}

// Count returns branch counts by status.
func (e *Explorer) Count() map[string]int {
	e.mu.Lock()
	defer e.mu.Unlock()

	counts := map[string]int{}
	for _, b := range e.branches {
		counts[b.Status]++
	}
	return counts
}

// Summary produces a human-readable summary.
func (e *Explorer) Summary() string {
	e.mu.Lock()
	defer e.mu.Unlock()

	var b strings.Builder
	fmt.Fprintf(&b, "Branches: %d total\n", len(e.branches))
	for _, br := range e.branches {
		status := br.Status
		if br.Score > 0 {
			fmt.Fprintf(&b, "  [%s] %s (score: %.1f, %d msgs)\n", status, br.Name, br.Score, len(br.Messages))
		} else {
			fmt.Fprintf(&b, "  [%s] %s (%d msgs)\n", status, br.Name, len(br.Messages))
		}
	}
	return b.String()
}

// Prune removes failed branches to free memory.
func (e *Explorer) Prune() int {
	e.mu.Lock()
	defer e.mu.Unlock()

	pruned := 0
	for id, b := range e.branches {
		if b.Status == "failed" {
			delete(e.branches, id)
			pruned++
		}
	}
	return pruned
}
