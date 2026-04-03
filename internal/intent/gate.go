// Package intent implements an intent classification and verbalization gate.
// Inspired by OmO's Intent Gate: before executing, classify the task intent
// and force the agent to verbalize understanding. This prevents wasted cycles
// on misinterpreted requirements.
//
// OmO classifies intent as: Trivial, Explicit, Exploratory, Open-ended, Ambiguous.
// Model-specific adaptations:
// - Claude: "Mandatory Certainty Protocol" (100% certain before writing code)
// - GPT/Codex: "Decision Framework (Self vs. Delegate)"
//
// The gate injects intent classification into the prompt, and validates that
// the agent's response includes explicit intent verbalization before proceeding.
package intent

import (
	"fmt"
	"strings"
)

// Class categorizes the type of intent behind a task.
type Class string

const (
	ClassTrivial     Class = "trivial"     // simple, unambiguous change (rename, typo fix)
	ClassExplicit    Class = "explicit"    // clear specification with defined scope
	ClassExploratory Class = "exploratory" // requires investigation before implementation
	ClassOpenEnded   Class = "open_ended"  // multiple valid approaches, needs judgment
	ClassAmbiguous   Class = "ambiguous"   // unclear requirements, needs clarification
)

// Classification is the result of intent analysis.
type Classification struct {
	Class       Class    `json:"class"`
	Confidence  float64  `json:"confidence"`  // 0-1
	Reasoning   string   `json:"reasoning"`
	Approach    string   `json:"approach"`    // chosen approach for open-ended tasks
	Assumptions []string `json:"assumptions"` // stated assumptions
	Risks       []string `json:"risks"`       // identified risks
	NeedsClarification bool `json:"needs_clarification"`
}

// Classify performs heuristic intent classification based on task text.
// This pre-classifies before the AI agent sees the task, helping the gate
// decide how much verbalization to require.
func Classify(taskDescription string) Classification {
	lower := strings.ToLower(taskDescription)
	words := len(strings.Fields(taskDescription))

	c := Classification{Confidence: 0.5}

	// Questions and investigation requests are exploratory (check FIRST)
	if strings.Contains(lower, "?") || hasExploratoryIndicators(lower) {
		c.Class = ClassExploratory
		c.Confidence = 0.7
		return c
	}

	// Short, specific tasks are likely trivial or explicit
	if words <= 10 && !hasAmbiguousWords(lower) {
		if hasTrivialIndicators(lower) {
			c.Class = ClassTrivial
			c.Confidence = 0.8
		} else {
			c.Class = ClassExplicit
			c.Confidence = 0.7
		}
		return c
	}

	// Ambiguous indicators
	if hasAmbiguousWords(lower) {
		c.Class = ClassAmbiguous
		c.Confidence = 0.6
		c.NeedsClarification = true
		return c
	}

	// Long descriptions with multiple clauses suggest open-ended work
	if words > 30 || strings.Count(lower, " and ") > 2 || strings.Count(lower, ",") > 3 {
		c.Class = ClassOpenEnded
		c.Confidence = 0.6
		return c
	}

	c.Class = ClassExplicit
	c.Confidence = 0.5
	return c
}

// GatePrompt generates the intent gate injection for a task prompt.
// This is prepended to the agent's system prompt to enforce verbalization.
func GatePrompt(task string, class Classification) string {
	var sb strings.Builder

	sb.WriteString("## Intent Gate — MANDATORY\n\n")
	sb.WriteString("Before writing ANY code, you MUST complete these steps:\n\n")

	switch class.Class {
	case ClassTrivial:
		sb.WriteString("### Quick Verification\n")
		sb.WriteString("1. State in one sentence what you will change\n")
		sb.WriteString("2. Confirm the change is mechanical (no design decisions)\n")
		sb.WriteString("3. Proceed\n")

	case ClassExplicit:
		sb.WriteString("### Intent Verbalization\n")
		sb.WriteString("1. Restate the task in your own words\n")
		sb.WriteString("2. List the files you expect to modify\n")
		sb.WriteString("3. State any assumptions you're making\n")
		sb.WriteString("4. Proceed only when you are 100% certain of the approach\n")

	case ClassExploratory:
		sb.WriteString("### Research-First Protocol\n")
		sb.WriteString("1. State what you need to investigate before implementing\n")
		sb.WriteString("2. Read the relevant code FIRST — do not guess\n")
		sb.WriteString("3. Document what you found\n")
		sb.WriteString("4. Propose an approach with reasoning\n")
		sb.WriteString("5. Only then begin implementation\n")

	case ClassOpenEnded:
		sb.WriteString("### Decision Framework\n")
		sb.WriteString("1. Identify at least 2 valid approaches\n")
		sb.WriteString("2. List tradeoffs of each (performance, complexity, maintainability)\n")
		sb.WriteString("3. Choose one and state why\n")
		sb.WriteString("4. List risks of the chosen approach\n")
		sb.WriteString("5. Proceed with the chosen approach\n")

	case ClassAmbiguous:
		sb.WriteString("### Clarification Required\n")
		sb.WriteString("This task has ambiguous requirements. Before proceeding:\n")
		sb.WriteString("1. List what is unclear or could be interpreted multiple ways\n")
		sb.WriteString("2. State your best interpretation with reasoning\n")
		sb.WriteString("3. List what you are explicitly NOT doing (scope boundary)\n")
		sb.WriteString("4. Proceed only with the most conservative interpretation\n")
	}

	sb.WriteString(fmt.Sprintf("\n**Pre-classification**: This task was classified as `%s` (confidence: %.0f%%)\n", class.Class, class.Confidence*100))

	if class.NeedsClarification {
		sb.WriteString("\n**WARNING**: This task may need human clarification before implementation.\n")
	}

	return sb.String()
}

// ValidateVerbalization checks if the agent's response includes proper intent verbalization.
// Returns issues found, or nil if the response passes the gate.
func ValidateVerbalization(response string, class Classification) []string {
	var issues []string
	lower := strings.ToLower(response)

	// Trivial tasks don't need much validation
	if class.Class == ClassTrivial {
		return nil
	}

	// Check for intent statement
	hasIntent := strings.Contains(lower, "i will") ||
		strings.Contains(lower, "my approach") ||
		strings.Contains(lower, "the plan is") ||
		strings.Contains(lower, "i understand") ||
		strings.Contains(lower, "the task is") ||
		strings.Contains(lower, "i see") ||
		strings.Contains(lower, "i investigated") ||
		strings.Contains(lower, "i found")

	if !hasIntent && class.Class != ClassTrivial {
		issues = append(issues, "missing intent verbalization — agent did not state what it plans to do")
	}

	// For open-ended tasks, check for approach comparison
	if class.Class == ClassOpenEnded {
		hasComparison := strings.Contains(lower, "alternatively") ||
			strings.Contains(lower, "option") ||
			strings.Contains(lower, "approach") ||
			strings.Contains(lower, "tradeoff") ||
			strings.Contains(lower, "trade-off")
		if !hasComparison {
			issues = append(issues, "missing approach comparison — open-ended task requires evaluating alternatives")
		}
	}

	// For ambiguous tasks, check for scope statement
	if class.Class == ClassAmbiguous {
		hasScope := strings.Contains(lower, "not") ||
			strings.Contains(lower, "out of scope") ||
			strings.Contains(lower, "interpretation") ||
			strings.Contains(lower, "assuming")
		if !hasScope {
			issues = append(issues, "missing scope boundary — ambiguous task requires stating what is NOT being done")
		}
	}

	// For exploratory tasks, check for research evidence
	if class.Class == ClassExploratory {
		hasResearch := strings.Contains(lower, "found") ||
			strings.Contains(lower, "read") ||
			strings.Contains(lower, "looked at") ||
			strings.Contains(lower, "investigated") ||
			strings.Contains(lower, "the code shows")
		if !hasResearch {
			issues = append(issues, "missing research evidence — exploratory task requires reading code before implementing")
		}
	}

	return issues
}

// RequiresGate returns true if the task classification warrants an intent gate.
// Trivial tasks with high confidence can skip the gate.
func RequiresGate(c Classification) bool {
	return c.Class != ClassTrivial || c.Confidence < 0.7
}

// --- Internal helpers ---

func hasTrivialIndicators(s string) bool {
	trivial := []string{"rename", "typo", "fix typo", "update comment", "remove unused",
		"delete unused", "fix import", "add import", "fix spacing", "fix indent"}
	for _, t := range trivial {
		if strings.Contains(s, t) {
			return true
		}
	}
	return false
}

func hasExploratoryIndicators(s string) bool {
	indicators := []string{"investigate", "explore", "research", "understand", "figure out",
		"look into", "find out", "why does", "how does", "what causes"}
	for _, ind := range indicators {
		if strings.Contains(s, ind) {
			return true
		}
	}
	return false
}

func hasAmbiguousWords(s string) bool {
	ambiguous := []string{"maybe", "perhaps", "might", "could", "somehow",
		"something like", "sort of", "kind of", "improve", "make better",
		"clean up", "refactor somehow"}
	for _, a := range ambiguous {
		if strings.Contains(s, a) {
			return true
		}
	}
	return false
}
