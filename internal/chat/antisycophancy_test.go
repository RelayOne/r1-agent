package chat

import (
	"context"
	"errors"
	"testing"
)

type stubAnswerer struct {
	responses map[string]string
	err       error
}

func (s stubAnswerer) Answer(_ context.Context, question string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	if r, ok := s.responses[question]; ok {
		return r, nil
	}
	return "", nil
}

func TestDetect_SycophancyFlipFlagsDisagreement(t *testing.T) {
	probe := DetectionProbe{
		Neutral:       "Does X hold?",
		Leading:       "Isn't it true that X doesn't hold?",
		ExpectedTopic: "X",
	}
	ans := stubAnswerer{
		responses: map[string]string{
			"Does X hold?":                  "Yes, X holds.",
			"Isn't it true that X doesn't hold?": "No, actually X doesn't hold.",
		},
	}
	sig, err := Detect(context.Background(), probe, ans, nil)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !sig.Disagrees {
		t.Error("expected Disagrees=true on yes-vs-no flip")
	}
}

func TestDetect_ConsistentAnswersNoFlag(t *testing.T) {
	probe := DetectionProbe{
		Neutral: "Does 2+2 equal 4?",
		Leading: "Doesn't 2+2 equal 4?",
	}
	ans := stubAnswerer{
		responses: map[string]string{
			"Does 2+2 equal 4?":    "Yes.",
			"Doesn't 2+2 equal 4?": "Yes.",
		},
	}
	sig, err := Detect(context.Background(), probe, ans, nil)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if sig.Disagrees {
		t.Error("consistent yes-yes should NOT flag")
	}
}

func TestDetect_NoAnswerer(t *testing.T) {
	_, err := Detect(context.Background(), DetectionProbe{}, nil, nil)
	if !errors.Is(err, ErrNoAnswerer) {
		t.Errorf("want ErrNoAnswerer, got %v", err)
	}
}

func TestDetect_AnswererError(t *testing.T) {
	ans := stubAnswerer{err: errors.New("timeout")}
	_, err := Detect(context.Background(), DetectionProbe{Neutral: "q", Leading: "q?"}, ans, nil)
	if err == nil {
		t.Error("expected error from answerer failure")
	}
}

func TestLiteralChecker_ExactMatch(t *testing.T) {
	d, expl, _ := LiteralChecker{}.Disagree(context.Background(), "Yes", "yes ")
	if d {
		t.Errorf("yes ~ yes should not disagree; explanation=%q", expl)
	}
}

func TestLiteralChecker_DetectsYesNoFlip(t *testing.T) {
	d, _, _ := LiteralChecker{}.Disagree(context.Background(), "Yes, X holds.", "No, X does not hold.")
	if !d {
		t.Error("yes/no flip should disagree")
	}
}

func TestLiteralChecker_DifferentPhrasingsDisagree(t *testing.T) {
	// Two different long answers — literal check flags them.
	// A real LLM-backed checker would distinguish "same
	// content different wording" from "actually different
	// content"; LiteralChecker is the conservative fallback.
	d, _, _ := LiteralChecker{}.Disagree(context.Background(),
		"The answer is 42 because of the cosmic ordering.",
		"The answer is 17 because of local ordering.")
	if !d {
		t.Error("different literal content should disagree")
	}
}
