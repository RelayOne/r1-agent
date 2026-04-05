// Converged answer: recursive multi-model convergence until an arbiter
// declares the answer complete.
//
// This is NOT "try 3 times and give up." This is:
//
//  1. All available models answer the question independently
//  2. An arbiter combines answers, flags conflicts, identifies gaps
//  3. Arbiter judges: complete? Return. Not complete? Recurse with
//     accumulated context (previous answers + review + what's missing)
//  4. Repeat until the arbiter says done
//
// There is no iteration cap in the logical sense — the arbiter decides
// convergence, not a counter. A safety depth limit prevents runaway
// recursion (default 20), but this is a circuit breaker, not a design
// constraint.
//
// This replaces the simple execute→validate→fix loop for any work where
// multiple perspectives catch what a single model misses.
package mission

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

// ModelAskFn sends a prompt to a specific model and returns its response.
// The model parameter identifies which model to use (e.g., "claude", "codex").
type ModelAskFn func(ctx context.Context, model string, prompt string) (response string, err error)

// ConvergedAnswerConfig configures the recursive multi-model convergence loop.
type ConvergedAnswerConfig struct {
	// Models lists all available models that answer independently.
	// Each model receives the same prompt and answers without seeing others' work.
	Models []string

	// ArbiterModel is the model that combines answers, reviews for conflicts
	// and gaps, and decides whether the answer is complete.
	// Should be the strongest/most capable model available.
	ArbiterModel string

	// AskFn sends a prompt to a specific model.
	AskFn ModelAskFn

	// BiggerMission provides the parent context — what larger goal this
	// work is part of. Ensures sub-tasks don't lose sight of the whole.
	BiggerMission string

	// Mission is the specific task to converge on.
	Mission string

	// MaxDepth is a safety circuit breaker, NOT a design constraint.
	// The arbiter decides convergence, not this counter.
	// Default: 20. Set higher if you trust your cost budget.
	MaxDepth int

	// StepName identifies this convergence for logging.
	StepName string

	// OnIteration is called after each recursion with the current depth
	// and arbiter review. Used for logging, metrics, and TUI updates.
	OnIteration func(depth int, review string, complete bool)
}

// ConvergedAnswerResult captures the outcome of recursive convergence.
type ConvergedAnswerResult struct {
	// Answer is the final converged answer from the arbiter.
	Answer string

	// Depth is how many recursions it took to converge.
	Depth int

	// Converged is true if the arbiter declared the answer complete.
	// False only if the safety depth limit was hit.
	Converged bool

	// Duration is wall-clock time for the entire convergence.
	Duration time.Duration

	// Rounds records each recursion's model answers and arbiter review.
	Rounds []ConvergenceRound
}

// ConvergenceRound records a single recursion in the convergence loop.
type ConvergenceRound struct {
	Depth        int               `json:"depth"`
	ModelAnswers map[string]string `json:"model_answers"` // model → answer
	ArbiterReview string           `json:"arbiter_review"`
	Complete     bool              `json:"complete"`
	Duration     time.Duration     `json:"duration"`
}

// ConvergedAnswer recursively drives multiple models toward a converged answer.
//
// Pattern:
//
//	for each model: answer independently
//	arbiter: combine, flag conflicts, identify gaps
//	arbiter: is it complete? yes → return. no → recurse with context.
func ConvergedAnswer(ctx context.Context, cfg ConvergedAnswerConfig) (*ConvergedAnswerResult, error) {
	if cfg.AskFn == nil {
		return nil, fmt.Errorf("converged: AskFn is required")
	}
	if len(cfg.Models) == 0 {
		return nil, fmt.Errorf("converged: at least one model is required")
	}
	if cfg.ArbiterModel == "" {
		// Default: use first model as arbiter
		cfg.ArbiterModel = cfg.Models[0]
	}
	if cfg.MaxDepth <= 0 {
		cfg.MaxDepth = 20
	}

	start := time.Now()
	result := &ConvergedAnswerResult{}

	return convergedAnswerRecurse(ctx, cfg, result, cfg.Mission, 0, start)
}

func convergedAnswerRecurse(
	ctx context.Context,
	cfg ConvergedAnswerConfig,
	result *ConvergedAnswerResult,
	currentMission string,
	depth int,
	start time.Time,
) (*ConvergedAnswerResult, error) {
	select {
	case <-ctx.Done():
		result.Duration = time.Since(start)
		return result, ctx.Err()
	default:
	}

	// Safety circuit breaker — NOT the convergence condition
	if depth >= cfg.MaxDepth {
		log.Printf("[converged] %s: safety depth limit %d reached — returning best answer",
			cfg.StepName, cfg.MaxDepth)
		result.Duration = time.Since(start)
		result.Converged = false
		return result, nil
	}

	roundStart := time.Now()
	round := ConvergenceRound{
		Depth:        depth,
		ModelAnswers: make(map[string]string),
	}

	// Step 1: All models answer independently
	modelPrompt := buildModelPrompt(cfg.BiggerMission, currentMission)
	for _, model := range cfg.Models {
		select {
		case <-ctx.Done():
			result.Duration = time.Since(start)
			return result, ctx.Err()
		default:
		}

		answer, err := cfg.AskFn(ctx, model, modelPrompt)
		if err != nil {
			log.Printf("[converged] %s: model %s failed at depth %d: %v", cfg.StepName, model, depth, err)
			round.ModelAnswers[model] = fmt.Sprintf("[ERROR: %v]", err)
			continue
		}
		round.ModelAnswers[model] = answer
	}

	// Step 2: Arbiter combines answers, flags conflicts, identifies gaps
	combinedContext := buildCombinedContext(cfg.BiggerMission, currentMission, round.ModelAnswers)
	reviewPrompt := buildReviewPrompt(combinedContext)

	review, err := cfg.AskFn(ctx, cfg.ArbiterModel, reviewPrompt)
	if err != nil {
		result.Duration = time.Since(start)
		return result, fmt.Errorf("converged: arbiter review failed at depth %d: %w", depth, err)
	}
	round.ArbiterReview = review

	// Step 3: Arbiter judges completeness
	completenessPrompt := buildCompletenessPrompt(combinedContext, review)
	verdict, err := cfg.AskFn(ctx, cfg.ArbiterModel, completenessPrompt)
	if err != nil {
		result.Duration = time.Since(start)
		return result, fmt.Errorf("converged: arbiter completeness check failed at depth %d: %w", depth, err)
	}

	complete := isCompleteVerdict(verdict)
	round.Complete = complete
	round.Duration = time.Since(roundStart)
	result.Rounds = append(result.Rounds, round)
	result.Depth = depth + 1
	result.Answer = review

	if cfg.OnIteration != nil {
		cfg.OnIteration(depth, review, complete)
	}

	if complete {
		log.Printf("[converged] %s: converged at depth %d in %s",
			cfg.StepName, depth, time.Since(start).Round(time.Millisecond))
		result.Converged = true
		result.Duration = time.Since(start)
		return result, nil
	}

	// Step 4: Not complete — recurse with accumulated context
	log.Printf("[converged] %s: depth %d not complete, recursing", cfg.StepName, depth)

	nextMission := buildNextMission(currentMission, round.ModelAnswers, review)
	return convergedAnswerRecurse(ctx, cfg, result, nextMission, depth+1, start)
}

// buildModelPrompt creates the prompt each model receives independently.
func buildModelPrompt(biggerMission, mission string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Your Mission\n\n%s\n\n", mission)
	if biggerMission != "" {
		fmt.Fprintf(&b, "## Larger Context\n\nThis is part of a bigger mission:\n%s\n\n", biggerMission)
	}
	fmt.Fprintf(&b, `## Instructions

Complete the mission above thoroughly. Do not cut corners.
Do not claim something is done unless you can cite specific evidence.
Do not defer work — if it's in scope, do it now.
If something is unclear, state what's unclear and provide your best answer anyway.
`)
	return b.String()
}

// buildCombinedContext assembles all model answers for the arbiter.
func buildCombinedContext(biggerMission, mission string, answers map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Mission Given to Models\n\n%s\n\n", mission)
	if biggerMission != "" {
		fmt.Fprintf(&b, "## Larger Mission Context\n\n%s\n\n", biggerMission)
	}
	fmt.Fprintf(&b, "## Model Answers\n\n")
	for model, answer := range answers {
		fmt.Fprintf(&b, "### %s\n\n%s\n\n---\n\n", model, answer)
	}
	return b.String()
}

// buildReviewPrompt creates the arbiter's review prompt.
func buildReviewPrompt(combinedContext string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s", combinedContext)
	fmt.Fprintf(&b, `## Your Role: Arbiter

You are the arbiter. Multiple models independently answered the mission above.

Your job:
1. **Combine** the answers into one unified, complete response
2. **Flag conflicts** where models disagree — resolve them with evidence
3. **Identify gaps** — what did ALL models miss? What's incomplete?
4. **Synthesize** the best parts of each answer into a single authoritative response

Do NOT just pick one answer. Do NOT average them. Synthesize the strongest
elements and fix what's wrong. If all models missed something, YOU must catch it.

Provide the complete, synthesized answer. Not a meta-commentary about the answers.
The actual answer.
`)
	return b.String()
}

// buildCompletenessPrompt asks the arbiter if the combined answer is complete.
func buildCompletenessPrompt(combinedContext, review string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s", combinedContext)
	fmt.Fprintf(&b, "## Arbiter's Synthesized Answer\n\n%s\n\n", review)
	fmt.Fprintf(&b, `## Completeness Judgment

You already synthesized an answer above. Now judge it adversarially.

Ask yourself:
- Does the answer FULLY satisfy the mission? Not partially. Not "mostly." FULLY.
- Are there conflicts that weren't resolved?
- Are there gaps that weren't filled?
- Would a domain expert find anything missing?
- Is there work that was deferred, hand-waved, or marked as "future work"?

Respond with EXACTLY one of:
- "COMPLETE" — the answer fully satisfies the mission with no gaps
- "INCOMPLETE: <what's missing>" — specific gaps that need another round

Do not hedge. Do not say "mostly complete." Either it's done or it's not.
`)
	return b.String()
}

// isCompleteVerdict parses the arbiter's completeness judgment.
func isCompleteVerdict(verdict string) bool {
	v := strings.TrimSpace(strings.ToUpper(verdict))
	// Must start with COMPLETE (not INCOMPLETE)
	if strings.HasPrefix(v, "INCOMPLETE") {
		return false
	}
	return strings.HasPrefix(v, "COMPLETE")
}

// buildNextMission constructs the mission for the next recursion,
// carrying forward all accumulated context.
func buildNextMission(previousMission string, previousAnswers map[string]string, review string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Previous Mission\n\n%s\n\n", previousMission)
	fmt.Fprintf(&b, "## What Was Attempted\n\nModels provided these answers:\n\n")
	for model, answer := range previousAnswers {
		// Truncate long answers to keep context manageable
		a := answer
		if len(a) > 2000 {
			a = a[:2000] + "\n...[truncated]"
		}
		fmt.Fprintf(&b, "### %s\n%s\n\n", model, a)
	}
	fmt.Fprintf(&b, "## Arbiter Review\n\n%s\n\n", review)
	fmt.Fprintf(&b, `## Your Mission Now

The previous round was NOT complete. The arbiter's review above explains what's missing.

You MUST:
1. Address every gap the arbiter identified
2. Resolve every conflict the arbiter flagged
3. Complete every piece of deferred work
4. Provide the FULL answer, not just the delta

Do not repeat the same incomplete answer. Fix it.
`)
	return b.String()
}
