// Package verify — rubrics.go
//
// STOKE-022 primitive #1: structured verification rubrics with
// per-task-class criteria. Each criterion is evaluated in a
// SEPARATE LLM call (not one big prompt evaluating everything
// at once) because independent evaluation catches failures
// that a composite prompt would smooth over.
//
// Scope of this file:
//
//   - Rubric + Criterion types
//   - RubricRegistry with per-task-class lookup
//   - CodeRubric, ResearchRubric, WritingRubric built-ins
//   - Evaluator interface that runs one criterion at a time
//   - EvaluateRubric that dispatches each criterion through
//     the evaluator and collects pass/fail outcomes
//
// The per-criterion LLM evaluator is NOT in this file — the
// Evaluator interface lets callers plug in whatever provider
// adapter they use (internal/provider/, internal/modelsource/,
// etc.) without this package importing LLM types.
package verify

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// TaskClass names the rubric category. Rubrics are selected
// per TaskClass so a research task gets faithfulness /
// relevance / completeness criteria instead of code-specific
// build + lint criteria.
type TaskClass string

const (
	TaskClassCode     TaskClass = "code"
	TaskClassResearch TaskClass = "research"
	TaskClassWriting  TaskClass = "writing"
	TaskClassScheduling TaskClass = "scheduling"
)

// Criterion is one graded axis of a rubric. Each criterion is
// evaluated in its own LLM call — the Weight + Threshold
// fields let callers weight dimensions (a code rubric might
// weight "tests pass" heavier than "naming consistency").
type Criterion struct {
	// ID is a stable identifier used in reports + logs.
	ID string

	// Name is the human-readable rubric axis (e.g.
	// "faithfulness", "build-passes", "narrative-flow").
	Name string

	// Description tells the evaluator what to look for. Fed
	// into the per-criterion LLM prompt verbatim.
	Description string

	// Weight is the relative importance, > 0. Criteria with
	// higher weight contribute more to the overall score.
	// Defaults to 1 when zero.
	Weight float64

	// Threshold is the minimum per-criterion score (on [0,1])
	// for this criterion to count as passed. Defaults to 0.7
	// when zero.
	Threshold float64
}

// Rubric is a full set of criteria for a TaskClass.
type Rubric struct {
	Class    TaskClass
	Criteria []Criterion
}

// Validate checks the rubric shape: non-empty Criteria list,
// every criterion has an ID + Name + non-empty Description,
// and weights are >= 0.
//
// Empty Criteria is REJECTED. A rubric without criteria would
// silently pass every artifact (EvaluateRubric returns
// Passed=true with zero evaluator calls on an empty slice),
// which is exactly the failure mode structured verification
// exists to prevent.
func (r Rubric) Validate() error {
	if len(r.Criteria) == 0 {
		return fmt.Errorf("verify: rubric for class %q has no criteria (would silently pass every artifact)", r.Class)
	}
	seenID := map[string]bool{}
	for i, c := range r.Criteria {
		if c.ID == "" {
			return fmt.Errorf("verify: rubric criterion %d has empty ID", i)
		}
		if seenID[c.ID] {
			return fmt.Errorf("verify: rubric criterion %d has duplicate ID %q", i, c.ID)
		}
		seenID[c.ID] = true
		if c.Name == "" {
			return fmt.Errorf("verify: rubric criterion %q has empty Name", c.ID)
		}
		if c.Description == "" {
			return fmt.Errorf("verify: rubric criterion %q has empty Description", c.ID)
		}
		if c.Weight < 0 {
			return fmt.Errorf("verify: rubric criterion %q has negative Weight", c.ID)
		}
	}
	return nil
}

// Built-in rubrics. Operators can replace these via
// Registry.Register.

// CodeRubric: build + test + lint + scope discipline.
var CodeRubric = Rubric{
	Class: TaskClassCode,
	Criteria: []Criterion{
		{ID: "build", Name: "build-passes", Weight: 2.0, Threshold: 1.0,
			Description: "The code compiles / builds without errors. This is pass/fail: the build either succeeds or it doesn't."},
		{ID: "tests", Name: "tests-pass", Weight: 2.0, Threshold: 1.0,
			Description: "All declared tests pass. Skipped tests count as not-passing unless they have an explicit skip reason."},
		{ID: "lint", Name: "lint-clean", Weight: 1.0, Threshold: 0.9,
			Description: "Lint output is clean or shows only pre-existing warnings that weren't introduced by this change."},
		{ID: "scope", Name: "scope-discipline", Weight: 1.0, Threshold: 1.0,
			Description: "Changed files are limited to the declared task scope. No files outside the task.files list were modified."},
		{ID: "tests-meaningful", Name: "tests-are-meaningful", Weight: 1.0, Threshold: 0.7,
			Description: "Added tests actually verify behavior — not assert-true noise, not commented-out blocks, not pre-compute-then-assert patterns."},
	},
}

// ResearchRubric: faithfulness / relevance / completeness.
var ResearchRubric = Rubric{
	Class: TaskClassResearch,
	Criteria: []Criterion{
		{ID: "faithfulness", Name: "faithfulness", Weight: 2.0, Threshold: 0.8,
			Description: "Every factual claim in the output is grounded in a cited source. Claims without sources count against this axis."},
		{ID: "relevance", Name: "relevance", Weight: 1.5, Threshold: 0.8,
			Description: "The cited sources actually support the claims they're attached to. Tangentially-related sources don't count."},
		{ID: "completeness", Name: "completeness", Weight: 1.0, Threshold: 0.7,
			Description: "The output addresses the full research question, not just the easy-to-find parts."},
		{ID: "recency", Name: "recency", Weight: 0.5, Threshold: 0.5,
			Description: "Sources are reasonably recent for the domain. A 2018 LLM source in a 2026 report is suspect; a 2018 physics source may still be current."},
	},
}

// WritingRubric: multi-dimensional analytic.
var WritingRubric = Rubric{
	Class: TaskClassWriting,
	Criteria: []Criterion{
		{ID: "clarity", Name: "clarity", Weight: 1.0, Threshold: 0.7,
			Description: "The writing is clear — a reader can follow the argument without re-reading sentences."},
		{ID: "structure", Name: "structure", Weight: 1.0, Threshold: 0.7,
			Description: "The piece has a logical flow: introduction → substance → conclusion, or an equivalent structure appropriate to the form."},
		{ID: "voice", Name: "voice", Weight: 1.0, Threshold: 0.7,
			Description: "Voice is consistent and appropriate to the task's stated tone."},
		{ID: "correctness", Name: "factual-correctness", Weight: 1.5, Threshold: 0.9,
			Description: "Factual claims are accurate. Misstatements of well-known facts fail this axis heavily."},
	},
}

// SchedulingRubric: state-based checks.
var SchedulingRubric = Rubric{
	Class: TaskClassScheduling,
	Criteria: []Criterion{
		{ID: "constraint-satisfaction", Name: "constraints-satisfied", Weight: 2.0, Threshold: 1.0,
			Description: "The proposed schedule satisfies every declared constraint (no double-booking, all required attendees included, duration matches the task)."},
		{ID: "preference-alignment", Name: "preference-alignment", Weight: 1.0, Threshold: 0.7,
			Description: "Soft preferences (time-of-day, buffer time, no-meeting days) are respected when possible."},
	},
}

// Registry maps TaskClass → Rubric. Thread-safe.
type Registry struct {
	mu      sync.RWMutex
	rubrics map[TaskClass]Rubric
}

// NewRegistry returns a registry pre-populated with the four
// built-in rubrics.
func NewRegistry() *Registry {
	r := &Registry{rubrics: map[TaskClass]Rubric{}}
	for _, rb := range []Rubric{CodeRubric, ResearchRubric, WritingRubric, SchedulingRubric} {
		r.rubrics[rb.Class] = rb
	}
	return r
}

// Register overrides or adds a rubric for a task class.
// Validates before storing — a bad rubric can't land.
func (r *Registry) Register(rb Rubric) error {
	if err := rb.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rubrics[rb.Class] = rb
	return nil
}

// Get looks up the rubric for a task class.
func (r *Registry) Get(class TaskClass) (Rubric, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rb, ok := r.rubrics[class]
	return rb, ok
}

// RegisteredClasses returns the sorted list of registered
// task classes. Used by discovery UIs + reports.
func (r *Registry) RegisteredClasses() []TaskClass {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]TaskClass, 0, len(r.rubrics))
	for c := range r.rubrics {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Evaluator runs one criterion at a time. Callers plug in
// whatever LLM provider they use; this interface is the
// boundary that keeps this package agnostic to model choice.
type Evaluator interface {
	// EvaluateCriterion takes the subject under test + the
	// criterion definition and returns a score in [0, 1] plus
	// an explanation. Implementations typically dispatch one
	// LLM call per invocation.
	EvaluateCriterion(ctx context.Context, subject string, c Criterion) (score float64, explanation string, err error)
}

// RubricOutcome is the per-criterion result.
type RubricOutcome struct {
	CriterionID string
	Score       float64
	Passed      bool
	Explanation string
}

// RubricResult summarizes a full rubric evaluation.
type RubricResult struct {
	Class         TaskClass
	Outcomes      []RubricOutcome
	WeightedScore float64 // [0, 1] weighted average across criteria
	Passed        bool    // true iff every criterion individually passed
}

// ErrNoEvaluator is returned by EvaluateRubric when the
// caller didn't supply one.
var ErrNoEvaluator = errors.New("verify: no evaluator supplied")

// EvaluateRubric runs each criterion through the evaluator
// in SEPARATE calls (the SOW's "each criterion in a separate
// LLM call" requirement). Per-criterion failures surface
// individually; the aggregate score is the weighted average
// but Passed is AND across all criteria — one failure fails
// the whole rubric.
func EvaluateRubric(ctx context.Context, subject string, rb Rubric, ev Evaluator) (RubricResult, error) {
	if ev == nil {
		return RubricResult{}, ErrNoEvaluator
	}
	if err := rb.Validate(); err != nil {
		return RubricResult{}, err
	}
	res := RubricResult{Class: rb.Class, Passed: true}
	var weightSum, scoreSum float64
	for _, c := range rb.Criteria {
		weight := c.Weight
		if weight == 0 {
			weight = 1
		}
		threshold := c.Threshold
		if threshold == 0 {
			threshold = 0.7
		}
		score, expl, err := ev.EvaluateCriterion(ctx, subject, c)
		if err != nil {
			return res, fmt.Errorf("verify: criterion %q: %w", c.ID, err)
		}
		if score < 0 {
			score = 0
		}
		if score > 1 {
			score = 1
		}
		passed := score >= threshold
		if !passed {
			res.Passed = false
		}
		res.Outcomes = append(res.Outcomes, RubricOutcome{
			CriterionID: c.ID,
			Score:       score,
			Passed:      passed,
			Explanation: expl,
		})
		weightSum += weight
		scoreSum += weight * score
	}
	if weightSum > 0 {
		res.WeightedScore = scoreSum / weightSum
	}
	return res, nil
}
