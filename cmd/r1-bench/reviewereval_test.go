package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RelayOne/r1/internal/reviewereval"
)

// stubReviewer swaps buildReviewerFunc for an offline heuristic
// reviewer (flags TODO / panic / hardcoded-return bodies as fake) and
// returns a restore func. The heuristic is intentionally aligned with
// the seed corpus's fake markers so the end-to-end pipeline can be
// asserted deterministically without a paid model call.
func stubReviewer(t *testing.T) {
	t.Helper()
	orig := buildReviewerFunc
	buildReviewerFunc = func(_, model, _, _ string, _ time.Duration) (reviewereval.ReviewFunc, string, error) {
		fn := func(_ context.Context, _, userPrompt string) (string, error) {
			low := strings.ToLower(userPrompt)
			if strings.Contains(low, "todo") || strings.Contains(low, "panic(") || strings.Contains(low, "return 4") {
				return `{"verdict":"fake","reasoning":"placeholder or hardcoded body"}`, nil
			}
			return `{"verdict":"real","reasoning":"implements the spec"}`, nil
		}
		return fn, model, nil
	}
	t.Cleanup(func() { buildReviewerFunc = orig })
}

// TestReviewerEvalSubcommandEndToEnd drives the full subcommand
// pipeline — LoadCorpus over the shipped seed corpus → RunPair →
// Report/JSON emission — with only the LLM transport stubbed.
func TestReviewerEvalSubcommandEndToEnd(t *testing.T) {
	stubReviewer(t)

	out := filepath.Join(t.TempDir(), "result.json")
	err := runReviewerEval([]string{
		"--corpus", "../../internal/reviewereval/corpus",
		"--builder", "claude-sonnet-4-6",
		"--reviewer", "stub-reviewer",
		"--output", out,
	})
	if err != nil {
		t.Fatalf("runReviewerEval: %v", err)
	}

	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	var results []reviewereval.PairResult
	if err := json.Unmarshal(body, &results); err != nil {
		t.Fatalf("unmarshal results: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	res := results[0]
	if res.BuilderModel != "claude-sonnet-4-6" || res.ReviewerModel != "stub-reviewer" {
		t.Errorf("attribution = (%q, %q)", res.BuilderModel, res.ReviewerModel)
	}
	// Seed corpus is 3 real / 3 fake and the stub is exact on it.
	want := reviewereval.Confusion{TP: 3, TN: 3}
	if res.Confusion != want {
		t.Errorf("confusion = %+v, want %+v", res.Confusion, want)
	}
	if res.Accuracy != 1.0 || res.Precision != 1.0 || res.Recall != 1.0 {
		t.Errorf("scores = p=%v r=%v a=%v, want all 1.0", res.Precision, res.Recall, res.Accuracy)
	}
	if len(res.Decisions) != 6 {
		t.Errorf("decisions = %d, want 6", len(res.Decisions))
	}
	if len(res.Skipped) != 0 {
		t.Errorf("skipped = %v, want none", res.Skipped)
	}
}

// TestReviewerEvalSubcommandFlagValidation locks the required-flag
// contract and the missing-corpus failure mode.
func TestReviewerEvalSubcommandFlagValidation(t *testing.T) {
	stubReviewer(t)

	if err := runReviewerEval([]string{"--reviewer", "x", "--corpus", "../../internal/reviewereval/corpus"}); err == nil {
		t.Error("expected error when --builder is missing")
	}
	if err := runReviewerEval([]string{"--builder", "x", "--corpus", "../../internal/reviewereval/corpus"}); err == nil {
		t.Error("expected error when --reviewer is missing")
	}
	if err := runReviewerEval([]string{"--builder", "x", "--reviewer", "y", "--corpus", t.TempDir() + "/nope"}); err == nil {
		t.Error("expected error for missing corpus dir")
	}
}
