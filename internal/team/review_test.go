package team

import (
	"context"
	"testing"
)

func TestParallelReviewAllPass(t *testing.T) {
	perspectives := DefaultPerspectives()
	diff := "diff --git a/main.go b/main.go\n+func hello() { fmt.Println(\"hello\") }"

	mockReview := func(_ context.Context, p ReviewPerspective, _ string) ReviewResult {
		return ReviewResult{
			PerspectiveID: p.ID,
			Pass:          true,
			Summary:       "looks good",
			DurationMs:    100,
		}
	}

	verdict := ParallelReview(context.Background(), perspectives, diff, mockReview)

	if !verdict.Pass {
		t.Error("expected pass when all reviewers pass")
	}
	if verdict.Consensus != "unanimous" {
		t.Errorf("expected unanimous, got %s", verdict.Consensus)
	}
	if len(verdict.Reviews) != 3 {
		t.Errorf("expected 3 reviews, got %d", len(verdict.Reviews))
	}
}

func TestParallelReviewCriticalFail(t *testing.T) {
	perspectives := DefaultPerspectives()
	diff := "diff"

	mockReview := func(_ context.Context, p ReviewPerspective, _ string) ReviewResult {
		if p.ID == "security" {
			return ReviewResult{
				PerspectiveID: p.ID,
				Pass:          false,
				Findings: []Finding{{
					PerspectiveID: p.ID,
					Severity:      "critical",
					Issue:         "SQL injection in query builder",
				}},
				DurationMs: 200,
			}
		}
		return ReviewResult{PerspectiveID: p.ID, Pass: true, DurationMs: 100}
	}

	verdict := ParallelReview(context.Background(), perspectives, diff, mockReview)

	if verdict.Pass {
		t.Error("expected fail when critical perspective fails")
	}
	if verdict.Consensus != "critical_fail" {
		t.Errorf("expected critical_fail, got %s", verdict.Consensus)
	}
	if len(verdict.CriticalFindings) != 1 {
		t.Errorf("expected 1 critical finding, got %d", len(verdict.CriticalFindings))
	}
}

func TestParallelReviewMajority(t *testing.T) {
	perspectives := []ReviewPerspective{
		{ID: "a", Name: "A", Critical: false},
		{ID: "b", Name: "B", Critical: false},
		{ID: "c", Name: "C", Critical: false},
	}

	mockReview := func(_ context.Context, p ReviewPerspective, _ string) ReviewResult {
		if p.ID == "c" {
			return ReviewResult{PerspectiveID: p.ID, Pass: false, DurationMs: 100}
		}
		return ReviewResult{PerspectiveID: p.ID, Pass: true, DurationMs: 100}
	}

	verdict := ParallelReview(context.Background(), perspectives, "diff", mockReview)

	if !verdict.Pass {
		t.Error("expected pass with majority (no critical fails)")
	}
	if verdict.Consensus != "majority" {
		t.Errorf("expected majority, got %s", verdict.Consensus)
	}
}

func TestParallelReviewSplit(t *testing.T) {
	perspectives := []ReviewPerspective{
		{ID: "a", Name: "A", Critical: false},
		{ID: "b", Name: "B", Critical: false},
	}

	mockReview := func(_ context.Context, p ReviewPerspective, _ string) ReviewResult {
		if p.ID == "a" {
			return ReviewResult{PerspectiveID: p.ID, Pass: true, DurationMs: 100}
		}
		return ReviewResult{PerspectiveID: p.ID, Pass: false, DurationMs: 100}
	}

	verdict := ParallelReview(context.Background(), perspectives, "diff", mockReview)

	if verdict.Pass {
		t.Error("expected fail on split vote")
	}
	if verdict.Consensus != "split" {
		t.Errorf("expected split, got %s", verdict.Consensus)
	}
}

func TestParallelReviewSpeedup(t *testing.T) {
	perspectives := DefaultPerspectives()

	mockReview := func(_ context.Context, p ReviewPerspective, _ string) ReviewResult {
		return ReviewResult{PerspectiveID: p.ID, Pass: true, DurationMs: 1000}
	}

	verdict := ParallelReview(context.Background(), perspectives, "diff", mockReview)

	// Total compute should be ~3000ms, wall clock should be much less (parallel)
	if verdict.TotalDurationMs < 2000 {
		t.Errorf("expected total compute >= 2000ms, got %d", verdict.TotalDurationMs)
	}
	// Wall clock should be < total compute (parallel execution)
	if verdict.WallClockMs >= verdict.TotalDurationMs {
		t.Error("expected parallel speedup (wall clock < total compute)")
	}
}

func TestVerdictSummaryFormat(t *testing.T) {
	perspectives := DefaultPerspectives()
	verdict := ParallelReview(context.Background(), perspectives, "diff", func(_ context.Context, p ReviewPerspective, _ string) ReviewResult {
		return ReviewResult{PerspectiveID: p.ID, Pass: true, DurationMs: 50}
	})

	summary := verdict.Summary
	if summary == "" {
		t.Error("expected non-empty summary")
	}
	if !contains(summary, "PASS") {
		t.Error("expected PASS in summary")
	}
	if !contains(summary, "unanimous") {
		t.Error("expected unanimous in summary")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
