package decisionbisect

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/RelayOne/r1/internal/wisdom"
)

// Input captures the reduced mission state needed for a decision narrative.
type Input struct {
	MissionID   string          `json:"mission_id"`
	Description string          `json:"description"`
	Regression  string          `json:"regression"`
	Decisions   []DecisionPoint `json:"decisions"`
}

// DecisionPoint describes one branch where the harness or reviewer had evidence.
type DecisionPoint struct {
	Step            string   `json:"step"`
	VerifierVerdict string   `json:"verifier_verdict,omitempty"`
	PolicyChange    string   `json:"policy_change,omitempty"`
	Dissents        []string `json:"dissents,omitempty"`
	Override        string   `json:"override,omitempty"`
	Reason          string   `json:"reason,omitempty"`
}

// Narrative is the result of the decision bisector.
type Narrative struct {
	MissionID   string          `json:"mission_id"`
	Regression  string          `json:"regression"`
	Steps       []string        `json:"steps"`
	RootCause   string          `json:"root_cause"`
	Learning    wisdom.Learning `json:"learning"`
	GeneratedAt time.Time       `json:"generated_at"`
}

// Analyze finds the first decision point with actionable evidence and produces a learning.
func Analyze(input Input, now time.Time) (Narrative, error) {
	if strings.TrimSpace(input.MissionID) == "" {
		return Narrative{}, fmt.Errorf("decisionbisect: mission_id is required")
	}
	if strings.TrimSpace(input.Regression) == "" {
		return Narrative{}, fmt.Errorf("decisionbisect: regression is required")
	}
	if len(input.Decisions) == 0 {
		return Narrative{}, fmt.Errorf("decisionbisect: at least one decision is required")
	}
	steps := make([]string, 0, len(input.Decisions))
	root := ""
	for idx, decision := range input.Decisions {
		steps = append(steps, formatStep(idx+1, decision))
		if root == "" && isActionable(decision) {
			root = deriveRootCause(decision)
		}
	}
	if root == "" {
		root = deriveRootCause(input.Decisions[len(input.Decisions)-1])
	}
	learning := wisdom.Learning{
		TaskID:         input.MissionID,
		Category:       wisdom.Gotcha,
		Description:    root,
		FailurePattern: failurePattern(input.Regression, root),
		ValidFrom:      now.UTC(),
	}
	return Narrative{
		MissionID:   input.MissionID,
		Regression:  input.Regression,
		Steps:       steps,
		RootCause:   root,
		Learning:    learning,
		GeneratedAt: now.UTC(),
	}, nil
}

func formatStep(index int, decision DecisionPoint) string {
	parts := []string{fmt.Sprintf("%d. %s", index, decision.Step)}
	if decision.VerifierVerdict != "" {
		parts = append(parts, "verifier="+decision.VerifierVerdict)
	}
	if decision.PolicyChange != "" {
		parts = append(parts, "policy="+decision.PolicyChange)
	}
	if len(decision.Dissents) > 0 {
		parts = append(parts, "dissent="+decision.Dissents[0])
	}
	if decision.Override != "" {
		parts = append(parts, "override="+decision.Override)
	}
	return strings.Join(parts, " | ")
}

func isActionable(decision DecisionPoint) bool {
	return decision.VerifierVerdict == "partial" || len(decision.Dissents) > 0 || decision.Override != ""
}

func deriveRootCause(decision DecisionPoint) string {
	switch {
	case decision.Override != "":
		return fmt.Sprintf("%s despite override %q", decision.Step, decision.Override)
	case len(decision.Dissents) > 0:
		return fmt.Sprintf("%s while dissent flagged %q", decision.Step, decision.Dissents[0])
	case decision.VerifierVerdict != "":
		return fmt.Sprintf("%s with verifier verdict %q", decision.Step, decision.VerifierVerdict)
	default:
		if decision.Reason != "" {
			return decision.Reason
		}
		return decision.Step
	}
}

func failurePattern(regression, root string) string {
	sum := sha256.Sum256([]byte(regression + "|" + root))
	return "hash:" + hex.EncodeToString(sum[:8])
}
