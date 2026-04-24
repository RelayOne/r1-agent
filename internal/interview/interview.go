// Package interview implements a Socratic clarification phase before task execution.
// Inspired by OmX's $deep-interview skill: before executing a task, systematically
// clarify intent, boundaries, non-goals, and assumptions through structured questioning.
//
// The deep interview produces a ClarifiedScope that feeds into planning and execution,
// preventing wasted cycles on misunderstood requirements. Key insight from OmX:
// "clarifying intent, boundaries, and non-goals" before any code is written.
//
// Also incorporates OmX's $ralplan pattern: once scope is clear, produce an explicit
// plan with documented tradeoffs before approving execution.
package interview

import (
	"fmt"
	"strings"
	"time"
)

// Phase represents the current interview phase.
type Phase string

const (
	PhaseGoal        Phase = "goal"        // What are we trying to achieve?
	PhaseBoundary    Phase = "boundary"    // What's in scope and out of scope?
	PhaseConstraint  Phase = "constraint"  // What constraints exist?
	PhaseRisk        Phase = "risk"        // What could go wrong?
	PhaseVerify      Phase = "verify"      // How will we know it's done?
	PhaseApproval    Phase = "approval"    // User confirms scope
)

// Question is a single clarification question.
type Question struct {
	Phase    Phase  `json:"phase"`
	Question string `json:"question"`
	Why      string `json:"why"`      // why this question matters
	Default  string `json:"default"`  // suggested default if user doesn't answer
}

// Answer pairs a question with the user's response.
type Answer struct {
	Question Question `json:"question"`
	Response string   `json:"response"`
	Skipped  bool     `json:"skipped"` // user chose to skip
}

// ClarifiedScope is the output of a deep interview session.
// This feeds directly into plan generation and execution prompts.
type ClarifiedScope struct {
	OriginalRequest string    `json:"original_request"`
	Goal            string    `json:"goal"`
	InScope         []string  `json:"in_scope"`
	OutOfScope      []string  `json:"out_of_scope"`
	Constraints     []string  `json:"constraints"`
	Risks           []string  `json:"risks"`
	SuccessCriteria []string  `json:"success_criteria"`
	Assumptions     []string  `json:"assumptions"`
	NonGoals        []string  `json:"non_goals"`
	Answers         []Answer  `json:"answers"`
	CompletedAt     time.Time `json:"completed_at"`
	Confidence      float64   `json:"confidence"` // 0-1, how confident we are in the scope
}

// Session manages a deep interview conversation.
type Session struct {
	request   string
	phase     Phase
	questions []Question
	answers   []Answer
	idx       int
}

// NewSession creates a new deep interview session for a task request.
func NewSession(request string) *Session {
	s := &Session{
		request: request,
		phase:   PhaseGoal,
	}
	s.questions = generateQuestions(request)
	return s
}

// CurrentPhase returns the current interview phase.
func (s *Session) CurrentPhase() Phase {
	return s.phase
}

// NextQuestion returns the next question to ask, or nil if done.
func (s *Session) NextQuestion() *Question {
	if s.idx >= len(s.questions) {
		return nil
	}
	return &s.questions[s.idx]
}

// Answer records a response and advances to the next question.
func (s *Session) Answer(response string) {
	if s.idx >= len(s.questions) {
		return
	}
	s.answers = append(s.answers, Answer{
		Question: s.questions[s.idx],
		Response: response,
	})
	s.idx++
	if s.idx < len(s.questions) {
		s.phase = s.questions[s.idx].Phase
	} else {
		s.phase = PhaseApproval
	}
}

// Skip skips the current question using its default.
func (s *Session) Skip() {
	if s.idx >= len(s.questions) {
		return
	}
	s.answers = append(s.answers, Answer{
		Question: s.questions[s.idx],
		Response: s.questions[s.idx].Default,
		Skipped:  true,
	})
	s.idx++
	if s.idx < len(s.questions) {
		s.phase = s.questions[s.idx].Phase
	} else {
		s.phase = PhaseApproval
	}
}

// Progress returns how far through the interview we are (0-1).
func (s *Session) Progress() float64 {
	if len(s.questions) == 0 {
		return 1.0
	}
	return float64(s.idx) / float64(len(s.questions))
}

// IsComplete returns true when all questions have been answered.
func (s *Session) IsComplete() bool {
	return s.idx >= len(s.questions)
}

// Synthesize produces a ClarifiedScope from the interview answers.
func (s *Session) Synthesize() *ClarifiedScope {
	scope := &ClarifiedScope{
		OriginalRequest: s.request,
		Answers:         s.answers,
		CompletedAt:     time.Now(),
	}

	for _, a := range s.answers {
		if a.Response == "" {
			continue
		}
		switch a.Question.Phase {
		case PhaseGoal:
			if scope.Goal == "" {
				scope.Goal = a.Response
			} else {
				scope.Assumptions = append(scope.Assumptions, a.Response)
			}
		case PhaseBoundary:
			items := splitItems(a.Response)
			if strings.Contains(strings.ToLower(a.Question.Question), "out of scope") ||
				strings.Contains(strings.ToLower(a.Question.Question), "non-goal") {
				scope.OutOfScope = append(scope.OutOfScope, items...)
				scope.NonGoals = append(scope.NonGoals, items...)
			} else {
				scope.InScope = append(scope.InScope, items...)
			}
		case PhaseConstraint:
			scope.Constraints = append(scope.Constraints, splitItems(a.Response)...)
		case PhaseRisk:
			scope.Risks = append(scope.Risks, splitItems(a.Response)...)
		case PhaseVerify:
			scope.SuccessCriteria = append(scope.SuccessCriteria, splitItems(a.Response)...)
		case PhaseApproval:
			// Approval phase contributes no structured scope fields.
		}
	}

	// Confidence based on how many questions were actually answered
	answered := 0
	for _, a := range s.answers {
		if !a.Skipped && a.Response != "" {
			answered++
		}
	}
	if len(s.answers) > 0 {
		scope.Confidence = float64(answered) / float64(len(s.answers))
	}

	return scope
}

// ToPrompt converts a ClarifiedScope into prompt context for execution.
func (cs *ClarifiedScope) ToPrompt() string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("## Task: %s\n\n", cs.OriginalRequest))

	if cs.Goal != "" {
		sb.WriteString(fmt.Sprintf("### Goal\n%s\n\n", cs.Goal))
	}

	if len(cs.InScope) > 0 {
		sb.WriteString("### In Scope\n")
		for _, item := range cs.InScope {
			sb.WriteString(fmt.Sprintf("- %s\n", item))
		}
		sb.WriteString("\n")
	}

	if len(cs.OutOfScope) > 0 {
		sb.WriteString("### Out of Scope (DO NOT implement)\n")
		for _, item := range cs.OutOfScope {
			sb.WriteString(fmt.Sprintf("- %s\n", item))
		}
		sb.WriteString("\n")
	}

	if len(cs.Constraints) > 0 {
		sb.WriteString("### Constraints (MUST follow)\n")
		for _, item := range cs.Constraints {
			sb.WriteString(fmt.Sprintf("- %s\n", item))
		}
		sb.WriteString("\n")
	}

	if len(cs.Risks) > 0 {
		sb.WriteString("### Risks\n")
		for _, item := range cs.Risks {
			sb.WriteString(fmt.Sprintf("- %s\n", item))
		}
		sb.WriteString("\n")
	}

	if len(cs.SuccessCriteria) > 0 {
		sb.WriteString("### Success Criteria\n")
		for _, item := range cs.SuccessCriteria {
			sb.WriteString(fmt.Sprintf("- [ ] %s\n", item))
		}
		sb.WriteString("\n")
	}

	if len(cs.Assumptions) > 0 {
		sb.WriteString("### Assumptions\n")
		for _, item := range cs.Assumptions {
			sb.WriteString(fmt.Sprintf("- %s\n", item))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// InterviewPrompt generates the system prompt for an AI to conduct the interview.
func InterviewPrompt(request string) string {
	return fmt.Sprintf(`You are conducting a deep requirements interview for the following task:

"%s"

Your job is to ask probing questions to clarify scope, constraints, and success criteria BEFORE any implementation begins.

## Interview Protocol

1. GOAL: What exactly should be achieved? What's the measurable outcome?
2. BOUNDARY: What's in scope? What's explicitly out of scope? What are the non-goals?
3. CONSTRAINTS: What technical constraints exist? Performance requirements? Compatibility? Style?
4. RISKS: What could go wrong? What are the edge cases? What's the rollback plan?
5. VERIFICATION: How will we know it's done? What tests should pass? What should the user see?

## Rules
- Ask ONE question at a time
- Be specific, not generic
- Challenge vague answers ("what exactly do you mean by 'fast'?")
- Suggest defaults when the user is unsure
- Stop when you have enough clarity to write a complete specification
- DO NOT start implementing — this is ONLY clarification

Start with the most important ambiguity you see in the request.`, request)
}

// --- Internal ---

func generateQuestions(request string) []Question {
	questions := []Question{
		{
			Phase:    PhaseGoal,
			Question: "What is the specific, measurable outcome you want from this task?",
			Why:      "Vague goals lead to wasted iterations",
			Default:  request,
		},
		{
			Phase:    PhaseGoal,
			Question: "Are there any assumptions baked into this request that I should know about?",
			Why:      "Hidden assumptions cause late-stage failures",
			Default:  "None specified",
		},
		{
			Phase:    PhaseBoundary,
			Question: "What is explicitly IN scope for this task?",
			Why:      "Prevents scope creep during implementation",
			Default:  "Only what's described in the request",
		},
		{
			Phase:    PhaseBoundary,
			Question: "What is explicitly OUT OF SCOPE or a non-goal?",
			Why:      "Non-goals prevent over-engineering",
			Default:  "No specific exclusions",
		},
		{
			Phase:    PhaseConstraint,
			Question: "Are there technical constraints I should follow? (e.g., specific libraries, patterns, compatibility requirements)",
			Why:      "Constraints prevent rework from wrong technology choices",
			Default:  "Follow existing project conventions",
		},
		{
			Phase:    PhaseRisk,
			Question: "What could go wrong? Are there edge cases or failure modes I should handle?",
			Why:      "Early risk identification prevents production issues",
			Default:  "Handle common error cases",
		},
		{
			Phase:    PhaseVerify,
			Question: "How will we verify this is done correctly? What tests or checks should pass?",
			Why:      "Clear success criteria prevent endless iteration",
			Default:  "Build, test, and lint must pass",
		},
	}

	// Add context-specific questions based on keywords
	lower := strings.ToLower(request)

	if strings.Contains(lower, "api") || strings.Contains(lower, "endpoint") {
		questions = append(questions, Question{
			Phase:    PhaseConstraint,
			Question: "What should the API contract look like? (methods, paths, request/response shapes)",
			Why:      "API design affects all consumers",
			Default:  "Follow RESTful conventions",
		})
	}

	if strings.Contains(lower, "database") || strings.Contains(lower, "migration") {
		questions = append(questions, Question{
			Phase:    PhaseRisk,
			Question: "Is there existing data that needs migration? What's the rollback plan?",
			Why:      "Data migrations are high-risk and hard to undo",
			Default:  "No migration needed",
		})
	}

	if strings.Contains(lower, "security") || strings.Contains(lower, "auth") {
		questions = append(questions, Question{
			Phase:    PhaseConstraint,
			Question: "What's the threat model? Who are we protecting against and what assets?",
			Why:      "Security work without a threat model is theater",
			Default:  "Standard web application threat model",
		})
	}

	if strings.Contains(lower, "refactor") || strings.Contains(lower, "rewrite") {
		questions = append(questions, Question{
			Phase:    PhaseBoundary,
			Question: "Should the external behavior change, or is this purely internal restructuring?",
			Why:      "Behavioral changes need different testing",
			Default:  "Internal restructuring only, no behavior changes",
		})
	}

	return questions
}

// splitItems splits a response into individual items (by newline, comma, or semicolon).
func splitItems(response string) []string {
	// Try newline-separated first
	lines := strings.Split(response, "\n")
	if len(lines) > 1 {
		var items []string
		for _, l := range lines {
			l = strings.TrimSpace(l)
			l = strings.TrimPrefix(l, "- ")
			l = strings.TrimPrefix(l, "* ")
			if l != "" {
				items = append(items, l)
			}
		}
		return items
	}

	// Try comma/semicolon separated
	var items []string
	for _, sep := range []string{";", ","} {
		parts := strings.Split(response, sep)
		if len(parts) > 1 {
			for _, p := range parts {
				p = strings.TrimSpace(p)
				if p != "" {
					items = append(items, p)
				}
			}
			return items
		}
	}

	return []string{strings.TrimSpace(response)}
}
