package reviewereval

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fourCases returns a 2-real / 2-fake in-memory corpus for RunPair
// unit tests. Case IDs are embedded in the user prompt by
// buildReviewPrompts, which is what lets scripted ReviewFuncs key
// their answers.
func fourCases() []Case {
	return []Case{
		{ID: "r1", Spec: "spec r1", Files: map[string]string{"a.go": "real code"}, Label: LabelReal},
		{ID: "r2", Spec: "spec r2", Files: map[string]string{"b.go": "real code"}, Label: LabelReal},
		{ID: "f1", Spec: "spec f1", Files: map[string]string{"c.go": "fake code"}, Label: LabelFake},
		{ID: "f2", Spec: "spec f2", Files: map[string]string{"d.go": "fake code"}, Label: LabelFake},
	}
}

// caseIDFromPrompt recovers the case ID buildReviewPrompts embedded.
func caseIDFromPrompt(t *testing.T, userPrompt string) string {
	t.Helper()
	for _, line := range strings.Split(userPrompt, "\n") {
		if id, ok := strings.CutPrefix(line, "Case ID: "); ok {
			return id
		}
	}
	t.Fatalf("no Case ID line in prompt: %q", userPrompt)
	return ""
}

// TestRunPairPopulatesPairResult scripts a reviewer that is right on
// three cases and wrong on one fake (accepts f1) and asserts the full
// PairResult: confusion counts, derived scores, and decisions.
func TestRunPairPopulatesPairResult(t *testing.T) {
	review := func(_ context.Context, _, userPrompt string) (string, error) {
		switch caseIDFromPrompt(t, userPrompt) {
		case "r1", "r2", "f1": // f1 is the reviewer's mistake: fake accepted as real
			return `{"verdict":"real","reasoning":"looks implemented"}`, nil
		default:
			return `{"verdict":"fake","reasoning":"placeholder"}`, nil
		}
	}

	res, err := RunPair(context.Background(), "builder-x", "reviewer-y", fourCases(), review)
	if err != nil {
		t.Fatalf("RunPair: %v", err)
	}
	if res.BuilderModel != "builder-x" || res.ReviewerModel != "reviewer-y" {
		t.Errorf("attribution = (%q, %q), want (builder-x, reviewer-y)", res.BuilderModel, res.ReviewerModel)
	}
	want := Confusion{TP: 2, FP: 1, FN: 0, TN: 1}
	if res.Confusion != want {
		t.Errorf("confusion = %+v, want %+v", res.Confusion, want)
	}
	if res.Precision != 2.0/3.0 {
		t.Errorf("precision = %v, want 2/3", res.Precision)
	}
	if res.Recall != 1.0 {
		t.Errorf("recall = %v, want 1", res.Recall)
	}
	if res.Accuracy != 0.75 {
		t.Errorf("accuracy = %v, want 0.75", res.Accuracy)
	}
	if len(res.Decisions) != 4 {
		t.Errorf("decisions = %d, want 4", len(res.Decisions))
	}
	if len(res.Skipped) != 0 {
		t.Errorf("skipped = %v, want none", res.Skipped)
	}
}

// TestRunPairSkipsFailedCases: transport errors and undecidable
// replies land in Skipped without sinking the evaluation.
func TestRunPairSkipsFailedCases(t *testing.T) {
	review := func(_ context.Context, _, userPrompt string) (string, error) {
		switch caseIDFromPrompt(t, userPrompt) {
		case "r2":
			return "", errors.New("transport down")
		case "f2":
			return "no idea, could go either way — it is real-ish but also fake-ish", nil
		case "r1":
			return `{"verdict":"real","reasoning":"ok"}`, nil
		default:
			return `{"verdict":"fake","reasoning":"stub"}`, nil
		}
	}

	res, err := RunPair(context.Background(), "b", "r", fourCases(), review)
	if err != nil {
		t.Fatalf("RunPair: %v", err)
	}
	if len(res.Decisions) != 2 {
		t.Errorf("decisions = %d, want 2", len(res.Decisions))
	}
	if len(res.Skipped) != 2 {
		t.Fatalf("skipped = %v, want 2 entries", res.Skipped)
	}
	got := map[string]bool{res.Skipped[0]: true, res.Skipped[1]: true}
	if !got["r2"] || !got["f2"] {
		t.Errorf("skipped = %v, want r2 + f2", res.Skipped)
	}
	want := Confusion{TP: 1, TN: 1}
	if res.Confusion != want {
		t.Errorf("confusion = %+v, want %+v", res.Confusion, want)
	}
}

// TestRunPairAllFailedErrors: an evaluation where every review call
// fails must error instead of returning a zero "insufficient data"
// score that could be mistaken for a result.
func TestRunPairAllFailedErrors(t *testing.T) {
	review := func(context.Context, string, string) (string, error) {
		return "", errors.New("boom")
	}
	if _, err := RunPair(context.Background(), "b", "r", fourCases(), review); err == nil {
		t.Error("expected error when all case reviews fail")
	}
	if _, err := RunPair(context.Background(), "b", "r", nil, review); err == nil {
		t.Error("expected error on empty corpus")
	}
	if _, err := RunPair(context.Background(), "b", "r", fourCases(), nil); err == nil {
		t.Error("expected error on nil ReviewFunc")
	}
}

// TestParseReviewerVerdict covers the JSON contract, prose-wrapped
// JSON, the bare-word fallback, and undecidable replies.
func TestParseReviewerVerdict(t *testing.T) {
	cases := []struct {
		name     string
		reply    string
		wantReal bool
		wantErr  bool
	}{
		{"clean json real", `{"verdict":"real","reasoning":"ok"}`, true, false},
		{"clean json fake", `{"verdict":"fake","reasoning":"stub"}`, false, false},
		{"json in prose", "Here is my verdict:\n```json\n{\"verdict\":\"fake\",\"reasoning\":\"hardcoded\"}\n```", false, false},
		{"bare word real", "VERDICT: REAL — implements the spec", true, false},
		{"bare word fake", "this is clearly FAKE", false, false},
		{"ambiguous", "it is real but also fake", false, true},
		{"empty", "", false, true},
	}
	for _, tc := range cases {
		gotReal, _, err := parseReviewerVerdict(tc.reply)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v, wantErr = %v", tc.name, err, tc.wantErr)
			continue
		}
		if !tc.wantErr && gotReal != tc.wantReal {
			t.Errorf("%s: matchesReal = %v, want %v", tc.name, gotReal, tc.wantReal)
		}
	}
}

// TestSeedCorpusLoads locks the shipped seed corpus: it must load in
// the package Case format with a balanced 3-real / 3-fake label split.
func TestSeedCorpusLoads(t *testing.T) {
	cases, err := LoadCorpus("corpus")
	if err != nil {
		t.Fatalf("LoadCorpus(corpus): %v", err)
	}
	if len(cases) != 6 {
		t.Fatalf("seed corpus has %d cases, want 6", len(cases))
	}
	var real, fake int
	for _, c := range cases {
		if c.ID == "" || c.Spec == "" || len(c.Files) == 0 {
			t.Errorf("case %q missing required fields", c.ID)
		}
		switch c.Label {
		case LabelReal:
			real++
		case LabelFake:
			fake++
		default:
			t.Errorf("case %q has invalid label %q", c.ID, c.Label)
		}
	}
	if real != 3 || fake != 3 {
		t.Errorf("label split = %d real / %d fake, want 3/3", real, fake)
	}
}
