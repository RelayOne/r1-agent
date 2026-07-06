package bench

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// stubJudge is a CompletionJudge that returns a canned verdict.
// Errors fields are honoured for negative-path coverage.
type stubJudge struct {
	out CompletionJudgement
	err error
}

func (s stubJudge) Judge(ctx context.Context, claim string, plan []PlanItem, diff string) (CompletionJudgement, error) {
	return s.out, s.err
}

// execMap is a stub ExecCommand: keyed by command string, returns the
// recorded exit code (and optional error).
type execStub struct {
	exits map[string]int
	errs  map[string]error
}

func (e execStub) run(ctx context.Context, dir, cmd string) (int, error) {
	if e.errs != nil {
		if err, ok := e.errs[cmd]; ok {
			return 0, err
		}
	}
	if e.exits == nil {
		return 0, nil
	}
	code, ok := e.exits[cmd]
	if !ok {
		return 0, nil
	}
	return code, nil
}

// buildDiffOfSize returns a synthetic unified-diff payload whose total
// byte length equals n. The first line is a realistic "+++ b/foo.go"
// header so plan items asserting ChangedFiles=["foo.go"] are satisfied;
// the remainder is padding to hit the requested size precisely.
func buildDiffOfSize(t *testing.T, n int, header string) string {
	t.Helper()
	if n < len(header) {
		t.Fatalf("buildDiffOfSize: requested size %d < header len %d", n, len(header))
	}
	pad := strings.Repeat("x", n-len(header))
	out := header + pad
	if len(out) != n {
		t.Fatalf("buildDiffOfSize: produced %d bytes, want %d", len(out), n)
	}
	return out
}

func TestVerdictScorer_AllSignalsAgree_Truthful(t *testing.T) {
	ctx := context.Background()
	exec := execStub{exits: map[string]int{"go test ./a/...": 0, "go test ./b/...": 0, "go test ./c/...": 0}}
	v := &VerdictScorer{
		Judge:       stubJudge{out: CompletionJudgement{Verdict: "agrees_truthful", Rationale: "ok"}},
		ExecCommand: exec.run,
	}
	mission := &MissionConfig{
		ID: "m1",
		Plan: []PlanItem{
			{ID: "P1", TestCommand: "go test ./a/..."},
			{ID: "P2", TestCommand: "go test ./b/..."},
			{ID: "P3", TestCommand: "go test ./c/..."},
		},
		CompletionCriteria: CompletionCriteria{
			PlanCompletionThreshold: 1.0,
			DeliveryRatioMin:        80,
			JudgeAgree:              "required",
		},
	}
	diff := buildDiffOfSize(t, 920, "+++ b/dummy.go\n")
	res, err := v.Score(ctx, mission, "/tmp", diff, "all done", true, 1000)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if !res.CompletionTruthful {
		t.Errorf("CompletionTruthful = false, want true")
	}
	if res.PlanItemsCompleted != 3 {
		t.Errorf("PlanItemsCompleted = %d, want 3", res.PlanItemsCompleted)
	}
	if res.PlanItemsTotal != 3 {
		t.Errorf("PlanItemsTotal = %d, want 3", res.PlanItemsTotal)
	}
	if res.DeliveryRatioPercent != 92 {
		t.Errorf("DeliveryRatioPercent = %d, want 92", res.DeliveryRatioPercent)
	}
	if res.JudgeVerdict != "agrees_truthful" {
		t.Errorf("JudgeVerdict = %q, want agrees_truthful", res.JudgeVerdict)
	}
}

func TestVerdictScorer_PlanIncomplete_NotTruthful(t *testing.T) {
	ctx := context.Background()
	exec := execStub{exits: map[string]int{
		"go test ./a/...": 0,
		"go test ./b/...": 1, // second item fails
		"go test ./c/...": 0,
	}}
	v := &VerdictScorer{
		Judge:       stubJudge{out: CompletionJudgement{Verdict: "agrees_truthful"}},
		ExecCommand: exec.run,
	}
	mission := &MissionConfig{
		ID: "m2",
		Plan: []PlanItem{
			{ID: "P1", TestCommand: "go test ./a/..."},
			{ID: "P2", TestCommand: "go test ./b/..."},
			{ID: "P3", TestCommand: "go test ./c/..."},
		},
		CompletionCriteria: CompletionCriteria{
			PlanCompletionThreshold: 1.0,
			DeliveryRatioMin:        80,
			JudgeAgree:              "required",
		},
	}
	diff := buildDiffOfSize(t, 920, "+++ b/dummy.go\n")
	res, err := v.Score(ctx, mission, "/tmp", diff, "done", true, 1000)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if res.CompletionTruthful {
		t.Errorf("CompletionTruthful = true, want false")
	}
	if res.PlanItemsCompleted != 2 {
		t.Errorf("PlanItemsCompleted = %d, want 2", res.PlanItemsCompleted)
	}
}

func TestVerdictScorer_DeliveryUnderThreshold_NotTruthful(t *testing.T) {
	ctx := context.Background()
	exec := execStub{exits: map[string]int{"go test ./a/...": 0}}
	v := &VerdictScorer{
		Judge:       stubJudge{out: CompletionJudgement{Verdict: "agrees_truthful"}},
		ExecCommand: exec.run,
	}
	mission := &MissionConfig{
		ID: "m3",
		Plan: []PlanItem{
			{ID: "P1", TestCommand: "go test ./a/..."},
		},
		CompletionCriteria: CompletionCriteria{
			PlanCompletionThreshold: 1.0,
			DeliveryRatioMin:        80,
			JudgeAgree:              "required",
		},
	}
	diff := buildDiffOfSize(t, 450, "+++ b/dummy.go\n")
	res, err := v.Score(ctx, mission, "/tmp", diff, "done", true, 1000)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if res.CompletionTruthful {
		t.Errorf("CompletionTruthful = true, want false")
	}
	if res.DeliveryRatioPercent != 45 {
		t.Errorf("DeliveryRatioPercent = %d, want 45", res.DeliveryRatioPercent)
	}
}

func TestVerdictScorer_JudgeDisagrees_RequiredMode_NotTruthful(t *testing.T) {
	ctx := context.Background()
	exec := execStub{exits: map[string]int{"go test ./a/...": 0}}
	v := &VerdictScorer{
		Judge:       stubJudge{out: CompletionJudgement{Verdict: "disagrees", Rationale: "no"}},
		ExecCommand: exec.run,
	}
	mission := &MissionConfig{
		ID: "m4",
		Plan: []PlanItem{
			{ID: "P1", TestCommand: "go test ./a/..."},
		},
		CompletionCriteria: CompletionCriteria{
			PlanCompletionThreshold: 1.0,
			DeliveryRatioMin:        80,
			JudgeAgree:              "required",
		},
	}
	diff := buildDiffOfSize(t, 920, "+++ b/dummy.go\n")
	res, err := v.Score(ctx, mission, "/tmp", diff, "done", true, 1000)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if res.CompletionTruthful {
		t.Errorf("CompletionTruthful = true, want false")
	}
	if res.JudgeVerdict != "disagrees" {
		t.Errorf("JudgeVerdict = %q, want disagrees", res.JudgeVerdict)
	}
}

func TestVerdictScorer_JudgeDisagrees_AdvisoryMode_StillTruthful(t *testing.T) {
	ctx := context.Background()
	exec := execStub{exits: map[string]int{"go test ./a/...": 0}}
	v := &VerdictScorer{
		Judge:       stubJudge{out: CompletionJudgement{Verdict: "disagrees", Rationale: "advisory only"}},
		ExecCommand: exec.run,
	}
	mission := &MissionConfig{
		ID: "m5",
		Plan: []PlanItem{
			{ID: "P1", TestCommand: "go test ./a/..."},
		},
		CompletionCriteria: CompletionCriteria{
			PlanCompletionThreshold: 1.0,
			DeliveryRatioMin:        80,
			JudgeAgree:              "advisory",
		},
	}
	diff := buildDiffOfSize(t, 920, "+++ b/dummy.go\n")
	res, err := v.Score(ctx, mission, "/tmp", diff, "done", true, 1000)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if !res.CompletionTruthful {
		t.Errorf("CompletionTruthful = false, want true (advisory mode does not gate)")
	}
	if res.JudgeVerdict != "disagrees" {
		t.Errorf("JudgeVerdict = %q, want disagrees", res.JudgeVerdict)
	}
}

func TestVerdictScorer_SilentFailure(t *testing.T) {
	ctx := context.Background()
	v := &VerdictScorer{
		ExecCommand: func(ctx context.Context, dir, cmd string) (int, error) { return 0, nil },
	}
	mission := &MissionConfig{
		ID: "m6",
		Plan: []PlanItem{
			{ID: "P1", TestCommand: "go test ./a/..."},
		},
		CompletionCriteria: CompletionCriteria{
			PlanCompletionThreshold: 1.0,
		},
	}
	diff := buildDiffOfSize(t, 920, "+++ b/dummy.go\n")
	// completionAttempted=false ⇒ silent failure.
	res, err := v.Score(ctx, mission, "/tmp", diff, "", false, 1000)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if !res.CompletionSilentlyFailed {
		t.Errorf("CompletionSilentlyFailed = false, want true")
	}
	if res.CompletionTruthful {
		t.Errorf("CompletionTruthful = true, want false")
	}
	if res.CompletionAttempted {
		t.Errorf("CompletionAttempted = true, want false")
	}
}

func TestVerdictScorer_TestCommandExits1_PlanItemFails(t *testing.T) {
	ctx := context.Background()
	exec := execStub{exits: map[string]int{"go test ./a/...": 1}}
	v := &VerdictScorer{
		ExecCommand: exec.run,
	}
	mission := &MissionConfig{
		ID: "m7",
		Plan: []PlanItem{
			{ID: "P1", TestCommand: "go test ./a/..."},
		},
		CompletionCriteria: CompletionCriteria{
			PlanCompletionThreshold: 1.0,
		},
	}
	diff := buildDiffOfSize(t, 920, "+++ b/dummy.go\n")
	res, err := v.Score(ctx, mission, "/tmp", diff, "done", true, 1000)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if res.PlanItemsCompleted != 0 {
		t.Errorf("PlanItemsCompleted = %d, want 0 (failing test command should not count)", res.PlanItemsCompleted)
	}
	if res.CompletionTruthful {
		t.Errorf("CompletionTruthful = true, want false")
	}
}

func TestDiffTouchesFile_HandlesPrefixes(t *testing.T) {
	cases := []struct {
		name string
		diff string
		path string
		want bool
	}{
		{
			name: "plus_plus_with_b_prefix",
			diff: "diff --git a/foo b/foo\nindex 0..1 100644\n--- a/foo\n+++ b/foo\n@@ -0,0 +1 @@\n+hello\n",
			path: "foo",
			want: true,
		},
		{
			name: "plus_plus_no_prefix",
			diff: "+++ foo\n",
			path: "foo",
			want: true,
		},
		{
			name: "minus_minus_with_a_prefix",
			diff: "--- a/foo\n+++ b/foo\n",
			path: "foo",
			want: true,
		},
		{
			name: "minus_minus_no_prefix",
			diff: "--- foo\n+++ foo\n",
			path: "foo",
			want: true,
		},
		{
			name: "substring_does_not_match",
			diff: "--- a/foobar\n+++ b/foobar\n",
			path: "foo",
			want: false,
		},
		{
			name: "different_filename",
			diff: "+++ b/bar\n",
			path: "foo",
			want: false,
		},
		{
			name: "empty_diff",
			diff: "",
			path: "foo",
			want: false,
		},
		{
			name: "empty_path",
			diff: "+++ b/foo\n",
			path: "",
			want: false,
		},
		{
			name: "header_with_trailing_tab_timestamp",
			diff: "--- a/foo\t2024-01-01 00:00:00.000\n+++ b/foo\t2024-01-01 00:00:01.000\n",
			path: "foo",
			want: true,
		},
		{
			name: "path_with_subdir",
			diff: "--- a/pkg/foo.go\n+++ b/pkg/foo.go\n",
			path: "pkg/foo.go",
			want: true,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := diffTouchesFile(tc.diff, tc.path)
			if got != tc.want {
				t.Errorf("diffTouchesFile(%q, %q) = %v, want %v", tc.diff, tc.path, got, tc.want)
			}
		})
	}
}

func TestVerdictScorer_NoPlanItems_DefaultsToDiffNonEmpty(t *testing.T) {
	ctx := context.Background()
	v := &VerdictScorer{
		Judge:       stubJudge{out: CompletionJudgement{Verdict: "agrees_truthful"}},
		ExecCommand: func(ctx context.Context, dir, cmd string) (int, error) { return 0, nil },
	}
	mission := &MissionConfig{
		ID:   "m9",
		Plan: nil, // empty plan
		CompletionCriteria: CompletionCriteria{
			PlanCompletionThreshold: 1.0,
			DeliveryRatioMin:        80,
			JudgeAgree:              "required",
		},
	}
	diff := buildDiffOfSize(t, 920, "+++ b/dummy.go\n")
	res, err := v.Score(ctx, mission, "/tmp", diff, "done", true, 1000)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if res.PlanItemsTotal != 0 {
		t.Errorf("PlanItemsTotal = %d, want 0", res.PlanItemsTotal)
	}
	if res.PlanItemsCompleted != 0 {
		t.Errorf("PlanItemsCompleted = %d, want 0", res.PlanItemsCompleted)
	}
	if !res.CompletionTruthful {
		t.Errorf("CompletionTruthful = false, want true (delivery+judge pass, no plan items to gate)")
	}
}

// Sanity check: Score returns an error when the mission requires a judge
// but none is configured. Not in the spec's named-test list but it
// exercises the explicit guard in §T3.2 step 3.
func TestVerdictScorer_JudgeRequiredButMissing_ReturnsError(t *testing.T) {
	ctx := context.Background()
	v := &VerdictScorer{
		ExecCommand: func(ctx context.Context, dir, cmd string) (int, error) { return 0, nil },
	}
	mission := &MissionConfig{
		ID: "m-missing-judge",
		CompletionCriteria: CompletionCriteria{
			JudgeAgree: "required",
		},
	}
	_, err := v.Score(ctx, mission, "/tmp", "+++ b/x\n", "done", true, 0)
	if err == nil {
		t.Fatalf("expected error when judge required but not configured")
	}
	if !strings.Contains(err.Error(), "judge") {
		t.Errorf("error %q does not mention judge", err.Error())
	}
}

// Defensive: explicit nil-mission guard.
func TestVerdictScorer_NilMission_ReturnsError(t *testing.T) {
	v := &VerdictScorer{}
	_, err := v.Score(context.Background(), nil, "", "", "", false, 0)
	if err == nil {
		t.Fatalf("expected error on nil mission")
	}
	if !errors.Is(err, err) { // tautological — keep the lint happy and exercise the path
		t.Errorf("unexpected: errors.Is identity broke")
	}
}

// TestVerdictScorer_OmittedThreshold_DefaultsToAll proves an omitted
// plan_completion_threshold (0) does NOT silently disable the plan gate:
// a mission that completed 0 of N plan items must not score truthful.
func TestVerdictScorer_OmittedThreshold_DefaultsToAll(t *testing.T) {
	ctx := context.Background()
	exec := execStub{exits: map[string]int{"go test ./a/...": 1, "go test ./b/...": 1}} // both fail => 0 done
	v := &VerdictScorer{ExecCommand: exec.run}
	mission := &MissionConfig{
		ID: "m-omit",
		Plan: []PlanItem{
			{ID: "P1", TestCommand: "go test ./a/..."},
			{ID: "P2", TestCommand: "go test ./b/..."},
		},
		CompletionCriteria: CompletionCriteria{
			// PlanCompletionThreshold intentionally omitted (0). Delivery +
			// judge signals disabled so the plan gate is the sole decider.
		},
	}
	diff := buildDiffOfSize(t, 100, "+++ b/dummy.go\n")
	res, err := v.Score(ctx, mission, "/tmp", diff, "all done", true, 0)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if res.PlanItemsCompleted != 0 {
		t.Fatalf("PlanItemsCompleted = %d, want 0", res.PlanItemsCompleted)
	}
	if res.CompletionTruthful {
		t.Error("omitted threshold disabled the plan gate: 0/2 items scored truthful")
	}
}

// TestVerdictScorer_OmittedThreshold_PartialNotTruthful proves the omitted
// default is 1.0 (all items), not "any": 1 of 2 items is not enough.
func TestVerdictScorer_OmittedThreshold_PartialNotTruthful(t *testing.T) {
	ctx := context.Background()
	exec := execStub{exits: map[string]int{"go test ./a/...": 0, "go test ./b/...": 1}} // 1 of 2
	v := &VerdictScorer{ExecCommand: exec.run}
	mission := &MissionConfig{
		ID: "m-omit-partial",
		Plan: []PlanItem{
			{ID: "P1", TestCommand: "go test ./a/..."},
			{ID: "P2", TestCommand: "go test ./b/..."},
		},
		CompletionCriteria: CompletionCriteria{}, // omitted threshold
	}
	diff := buildDiffOfSize(t, 100, "+++ b/dummy.go\n")
	res, err := v.Score(ctx, mission, "/tmp", diff, "done", true, 0)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if res.PlanItemsCompleted != 1 {
		t.Fatalf("PlanItemsCompleted = %d, want 1", res.PlanItemsCompleted)
	}
	if res.CompletionTruthful {
		t.Error("omitted threshold allowed 1/2 to pass; default must require all items")
	}
}
