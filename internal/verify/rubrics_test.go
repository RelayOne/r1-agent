package verify

import (
	"context"
	"errors"
	"testing"
)

type fakeEvaluator struct {
	scores map[string]float64
	err    error
}

func (f fakeEvaluator) EvaluateCriterion(_ context.Context, _ string, c Criterion) (float64, string, error) {
	if f.err != nil {
		return 0, "", f.err
	}
	return f.scores[c.ID], "fake " + c.ID, nil
}

func TestRubric_Validate(t *testing.T) {
	if err := CodeRubric.Validate(); err != nil {
		t.Errorf("CodeRubric failed validate: %v", err)
	}
	if err := ResearchRubric.Validate(); err != nil {
		t.Errorf("ResearchRubric failed validate: %v", err)
	}
	if err := WritingRubric.Validate(); err != nil {
		t.Errorf("WritingRubric failed validate: %v", err)
	}
	if err := SchedulingRubric.Validate(); err != nil {
		t.Errorf("SchedulingRubric failed validate: %v", err)
	}
}

func TestRubric_ValidateRejectsBad(t *testing.T) {
	bad := Rubric{Class: "x", Criteria: []Criterion{
		{ID: "", Name: "n", Description: "d"},
	}}
	if err := bad.Validate(); err == nil {
		t.Error("expected error on empty criterion ID")
	}

	dup := Rubric{Class: "x", Criteria: []Criterion{
		{ID: "a", Name: "n", Description: "d"},
		{ID: "a", Name: "n2", Description: "d2"},
	}}
	if err := dup.Validate(); err == nil {
		t.Error("expected error on duplicate criterion ID")
	}

	neg := Rubric{Class: "x", Criteria: []Criterion{
		{ID: "a", Name: "n", Description: "d", Weight: -1},
	}}
	if err := neg.Validate(); err == nil {
		t.Error("expected error on negative weight")
	}
}

func TestRegistry_BuiltInsRegistered(t *testing.T) {
	r := NewRegistry()
	for _, c := range []TaskClass{TaskClassCode, TaskClassResearch, TaskClassWriting, TaskClassScheduling} {
		if _, ok := r.Get(c); !ok {
			t.Errorf("class %q not registered", c)
		}
	}
}

func TestRegistry_RegisteredClasses_Sorted(t *testing.T) {
	r := NewRegistry()
	got := r.RegisteredClasses()
	for i := 1; i < len(got); i++ {
		if got[i] < got[i-1] {
			t.Errorf("not sorted at index %d: %q < %q", i, got[i], got[i-1])
		}
	}
}

func TestEvaluateRubric_AllPass(t *testing.T) {
	// Perfect scores across all criteria.
	scores := map[string]float64{}
	for _, c := range CodeRubric.Criteria {
		scores[c.ID] = 1.0
	}
	res, err := EvaluateRubric(context.Background(), "subject",
		CodeRubric, fakeEvaluator{scores: scores})
	if err != nil {
		t.Fatalf("EvaluateRubric: %v", err)
	}
	if !res.Passed {
		t.Error("all-1.0 should pass the rubric")
	}
	if res.WeightedScore < 0.99 {
		t.Errorf("WeightedScore=%v want >=0.99", res.WeightedScore)
	}
}

func TestEvaluateRubric_OneFailFailsWhole(t *testing.T) {
	scores := map[string]float64{}
	for _, c := range CodeRubric.Criteria {
		scores[c.ID] = 1.0
	}
	// Tank just one criterion.
	scores["lint"] = 0.2
	res, err := EvaluateRubric(context.Background(), "subject",
		CodeRubric, fakeEvaluator{scores: scores})
	if err != nil {
		t.Fatalf("EvaluateRubric: %v", err)
	}
	if res.Passed {
		t.Error("one failing criterion should fail the whole rubric")
	}
	// Weighted score still reflects the partial pass.
	if res.WeightedScore < 0.5 || res.WeightedScore > 1.0 {
		t.Errorf("WeightedScore=%v out of expected range", res.WeightedScore)
	}
}

func TestEvaluateRubric_NoEvaluator(t *testing.T) {
	_, err := EvaluateRubric(context.Background(), "subject", CodeRubric, nil)
	if !errors.Is(err, ErrNoEvaluator) {
		t.Errorf("want ErrNoEvaluator, got %v", err)
	}
}

func TestEvaluateRubric_EvaluatorErrorPropagated(t *testing.T) {
	_, err := EvaluateRubric(context.Background(), "s", CodeRubric,
		fakeEvaluator{err: errors.New("LLM failed")})
	if err == nil {
		t.Error("expected evaluator error to propagate")
	}
}

func TestEvaluateRubric_ScoreClamping(t *testing.T) {
	// Evaluator returns out-of-range scores; clamped to [0,1].
	scores := map[string]float64{}
	for _, c := range CodeRubric.Criteria {
		scores[c.ID] = 2.5 // out of range
	}
	res, _ := EvaluateRubric(context.Background(), "s", CodeRubric,
		fakeEvaluator{scores: scores})
	for _, o := range res.Outcomes {
		if o.Score > 1.0 {
			t.Errorf("criterion %q score=%v not clamped", o.CriterionID, o.Score)
		}
	}
}
