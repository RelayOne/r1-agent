// Package consent implements a human-in-the-loop approval workflow.
// Inspired by OpenHands' confirmation prompts and claw-code's permission system:
//
// Dangerous operations (destructive git commands, file deletions, external API calls)
// require explicit human approval. This package:
// - Classifies operations by risk level
// - Enforces approval for high-risk actions
// - Supports auto-approve rules (patterns, specific ops)
// - Tracks approval history for audit
//
// This is the safety net: even autonomous agents should pause before
// irreversible actions.
package consent

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// Risk level for an operation.
type Risk int

const (
	RiskNone    Risk = 0 // no approval needed
	RiskLow     Risk = 1 // auto-approved with logging
	RiskMedium  Risk = 2 // approval needed unless auto-approved
	RiskHigh    Risk = 3 // always requires approval
	RiskBlocked Risk = 4 // never allowed
)

// Decision is the outcome of an approval request.
type Decision string

const (
	DecisionApproved Decision = "approved"
	DecisionDenied   Decision = "denied"
	DecisionPending  Decision = "pending"
	DecisionAuto     Decision = "auto_approved"
	DecisionBlocked  Decision = "blocked"
)

// Request is an approval request for a dangerous operation.
type Request struct {
	ID          string    `json:"id"`
	Operation   string    `json:"operation"`   // e.g., "git push --force"
	Category    string    `json:"category"`    // e.g., "git", "file", "network"
	Risk        Risk      `json:"risk"`
	Description string    `json:"description"` // human-readable explanation
	Context     string    `json:"context"`     // additional context
	Decision    Decision  `json:"decision"`
	Reason      string    `json:"reason,omitempty"` // reason for denial
	RequestedAt time.Time `json:"requested_at"`
	DecidedAt   time.Time `json:"decided_at,omitempty"`
}

// ApproveFunc is called to get human approval. Returns true if approved.
type ApproveFunc func(req *Request) bool

// Rule is an auto-approval or auto-deny rule.
type Rule struct {
	Pattern  string   `json:"pattern"`  // operation pattern (prefix match)
	Category string   `json:"category"` // category filter
	Decision Decision `json:"decision"` // auto-approve or auto-deny
	Reason   string   `json:"reason"`
}

// Workflow manages the approval process.
type Workflow struct {
	mu          sync.Mutex
	rules       []Rule
	history     []Request
	approveFn   ApproveFunc
	nextID      int
	classifiers []Classifier
}

// Classifier assigns risk levels to operations.
type Classifier struct {
	Category string
	Patterns []string
	Risk     Risk
}

// DefaultClassifiers returns standard risk classifications.
func DefaultClassifiers() []Classifier {
	return []Classifier{
		{Category: "git", Patterns: []string{"push --force", "reset --hard", "clean -f", "branch -D"}, Risk: RiskHigh},
		{Category: "git", Patterns: []string{"push", "merge", "rebase"}, Risk: RiskMedium},
		{Category: "file", Patterns: []string{"rm -rf", "rmdir", "unlink"}, Risk: RiskHigh},
		{Category: "file", Patterns: []string{"delete", "remove"}, Risk: RiskMedium},
		{Category: "network", Patterns: []string{"curl", "wget", "fetch"}, Risk: RiskLow},
		{Category: "exec", Patterns: []string{"exec", "eval", "spawn"}, Risk: RiskMedium},
	}
}

// NewWorkflow creates an approval workflow.
func NewWorkflow(approveFn ApproveFunc) *Workflow {
	return &Workflow{
		approveFn:   approveFn,
		classifiers: DefaultClassifiers(),
	}
}

// AddRule adds an auto-approval or auto-deny rule.
func (w *Workflow) AddRule(rule Rule) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.rules = append(w.rules, rule)
}

// Classify determines the risk level of an operation.
func (w *Workflow) Classify(operation, category string) Risk {
	w.mu.Lock()
	defer w.mu.Unlock()

	opLower := strings.ToLower(operation)
	maxRisk := RiskNone

	for _, c := range w.classifiers {
		if category != "" && c.Category != category {
			continue
		}
		for _, p := range c.Patterns {
			if strings.Contains(opLower, strings.ToLower(p)) {
				if c.Risk > maxRisk {
					maxRisk = c.Risk
				}
			}
		}
	}
	return maxRisk
}

// Check requests approval for an operation. Returns the decision.
func (w *Workflow) Check(operation, category, description string) Decision {
	risk := w.Classify(operation, category)

	w.mu.Lock()
	w.nextID++
	req := Request{
		ID:          fmt.Sprintf("req-%d", w.nextID),
		Operation:   operation,
		Category:    category,
		Risk:        risk,
		Description: description,
		Decision:    DecisionPending,
		RequestedAt: time.Now(),
	}
	w.mu.Unlock()

	// Check auto-rules first (they override risk classification)
	if d := w.matchRule(operation, category); d != "" {
		req.Decision = d
		req.DecidedAt = time.Now()
		w.record(req)
		return d
	}

	// No approval needed for no risk
	if risk == RiskNone {
		req.Decision = DecisionApproved
		w.record(req)
		return DecisionApproved
	}

	// Blocked operations
	if risk == RiskBlocked {
		req.Decision = DecisionBlocked
		req.Reason = "operation is blocked by policy"
		req.DecidedAt = time.Now()
		w.record(req)
		return DecisionBlocked
	}

	// Auto-approve low risk with logging
	if risk == RiskLow {
		req.Decision = DecisionAuto
		req.DecidedAt = time.Now()
		w.record(req)
		return DecisionAuto
	}

	// Request human approval
	if w.approveFn != nil {
		if w.approveFn(&req) {
			req.Decision = DecisionApproved
		} else {
			req.Decision = DecisionDenied
		}
		req.DecidedAt = time.Now()
	} else {
		req.Decision = DecisionDenied
		req.Reason = "no approval handler configured"
		req.DecidedAt = time.Now()
	}

	w.record(req)
	return req.Decision
}

// History returns the approval history.
func (w *Workflow) History() []Request {
	w.mu.Lock()
	defer w.mu.Unlock()
	result := make([]Request, len(w.history))
	copy(result, w.history)
	return result
}

// Stats returns approval statistics.
func (w *Workflow) Stats() map[Decision]int {
	w.mu.Lock()
	defer w.mu.Unlock()

	stats := make(map[Decision]int)
	for _, r := range w.history {
		stats[r.Decision]++
	}
	return stats
}

func (w *Workflow) matchRule(operation, category string) Decision {
	w.mu.Lock()
	defer w.mu.Unlock()

	opLower := strings.ToLower(operation)
	for _, rule := range w.rules {
		if rule.Category != "" && rule.Category != category {
			continue
		}
		if strings.Contains(opLower, strings.ToLower(rule.Pattern)) {
			return rule.Decision
		}
	}
	return ""
}

func (w *Workflow) record(req Request) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.history = append(w.history, req)
}
