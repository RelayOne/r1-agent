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

// TestDetect_SymmetricProbe_YesNoContentFlip: with same-
// polarity probes (both asking "does X hold?"), a yes-vs-no
// answer pair IS genuine disagreement. LiteralChecker sees
// the content difference beyond just the yes/no and flags it.
func TestDetect_SymmetricProbe_YesNoContentFlip(t *testing.T) {
	probe := DetectionProbe{
		Neutral:       "Does X hold?",
		Leading:       "X holds, right?",
		ExpectedTopic: "X",
	}
	ans := stubAnswerer{
		responses: map[string]string{
			"Does X hold?":     "Yes, X holds completely.",
			"X holds, right?":  "No, X does not hold at all.",
		},
	}
	sig, err := Detect(context.Background(), probe, ans, nil)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !sig.Disagrees {
		t.Error("expected Disagrees=true on content-level yes-vs-no flip with same-polarity probes")
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

// TestLiteralChecker_BareYesNoFlipNotFlagged: bare "Yes" vs
// "No" on its own could be a polarity-inverted probe asking
// about the same claim. LiteralChecker can't disambiguate,
// so it does NOT flag — defers to an LLM-backed checker.
// Content-level differences still flag (see next test).
func TestLiteralChecker_BareYesNoFlipNotFlagged(t *testing.T) {
	d, expl, _ := LiteralChecker{}.Disagree(context.Background(), "Yes.", "No.")
	if d {
		t.Errorf("bare yes/no should NOT flag (polarity ambiguous); got explanation=%q", expl)
	}
}

// TestLiteralChecker_DetectsContentLevelDisagreement: when
// the prose differs in content (not just yes/no), LiteralChecker
// flags because the content difference is materially real.
func TestLiteralChecker_DetectsContentLevelDisagreement(t *testing.T) {
	d, _, _ := LiteralChecker{}.Disagree(context.Background(),
		"Yes, X holds because of mechanism Alpha.",
		"No, X does not hold because of mechanism Beta.")
	if !d {
		t.Error("content-level difference (different mechanisms cited) should flag")
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
