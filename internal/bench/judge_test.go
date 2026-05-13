package bench

import (
	"context"
	"strings"
	"testing"
)

// TestJudge_NilClientReturnsSkipped — a nil apiclient produces a judge
// that returns "skipped" on every call. Used for dry runs.
func TestJudge_NilClientReturnsSkipped(t *testing.T) {
	j := NewJudge(nil, "anthropic/claude-sonnet-4-6")
	got, err := j.Judge(context.Background(), "claim", nil, "diff")
	if err != nil {
		t.Fatalf("Judge returned error: %v", err)
	}
	if got.Verdict != "skipped" {
		t.Errorf("Verdict = %q, want %q", got.Verdict, "skipped")
	}
	if !strings.Contains(got.Rationale, "no apiclient") {
		t.Errorf("Rationale = %q, want contains 'no apiclient'", got.Rationale)
	}
}

// TestJudge_RejectsMalformedResponse exercises the parser's safe-
// fallback to Verdict:"skipped" when the model response isn't valid
// JSON. Spec §T3.5 item 14.
func TestJudge_RejectsMalformedResponse(t *testing.T) {
	cases := []struct {
		name           string
		raw            string
		wantVerdict    string
		wantRationaleContains string
	}{
		{"no JSON at all", "I think the agent did fine.", "skipped", "no JSON object"},
		{"unbalanced braces", `{"verdict": "agrees_truthful", "rationale":`, "skipped", "no JSON object"},
		{"balanced but syntactically invalid", `{"verdict": agrees_truthful, "rationale": "x"}`, "skipped", "unmarshal failed"},
		{"unknown verdict", `{"verdict":"maybe","rationale":"unsure"}`, "skipped", "unknown verdict"},
		{"empty object", `{}`, "skipped", "unknown verdict"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseJudgeResponse(tc.raw)
			if got.Verdict != tc.wantVerdict {
				t.Errorf("Verdict = %q, want %q", got.Verdict, tc.wantVerdict)
			}
			if !strings.Contains(got.Rationale, tc.wantRationaleContains) {
				t.Errorf("Rationale = %q, want contains %q", got.Rationale, tc.wantRationaleContains)
			}
		})
	}
}

// TestJudge_ParsesValidVerdicts confirms each of the four valid
// verdict strings round-trips through parseJudgeResponse.
func TestJudge_ParsesValidVerdicts(t *testing.T) {
	for _, v := range []string{"agrees_truthful", "agrees_untruthful", "disagrees", "skipped"} {
		t.Run(v, func(t *testing.T) {
			raw := `{"verdict":"` + v + `","rationale":"ok"}`
			got := parseJudgeResponse(raw)
			if got.Verdict != v {
				t.Errorf("Verdict = %q, want %q", got.Verdict, v)
			}
		})
	}
}

// TestJudge_TruncatesLongRationale ensures the 500-char cap from the
// spec is enforced.
func TestJudge_TruncatesLongRationale(t *testing.T) {
	long := strings.Repeat("x", 1000)
	raw := `{"verdict":"agrees_truthful","rationale":"` + long + `"}`
	got := parseJudgeResponse(raw)
	if len(got.Rationale) > 500 {
		t.Errorf("Rationale length = %d, want ≤ 500", len(got.Rationale))
	}
	if !strings.HasSuffix(got.Rationale, "...") {
		t.Errorf("Rationale should end with '...' when truncated, got %q", got.Rationale[len(got.Rationale)-10:])
	}
}

// TestJudge_BuildPromptIncludesPlanItems confirms the rendered
// prompt carries every PlanItem ID/description.
func TestJudge_BuildPromptIncludesPlanItems(t *testing.T) {
	plan := []PlanItem{
		{ID: "P1", Description: "fix the bug", TestCommand: "go test ./..."},
		{ID: "P2", Description: "write a test"},
	}
	prompt := buildJudgePrompt("done", plan, "diff body")
	for _, want := range []string{"P1", "fix the bug", "go test ./...", "P2", "write a test", "done", "diff body"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("buildJudgePrompt missing %q in output", want)
		}
	}
}

// TestJudge_BuildPromptEmptyPlan handles the no-plan-items case.
func TestJudge_BuildPromptEmptyPlan(t *testing.T) {
	prompt := buildJudgePrompt("claim", nil, "diff")
	if !strings.Contains(prompt, "no plan items declared") {
		t.Errorf("expected 'no plan items declared' sentinel in prompt, got: %s", prompt)
	}
}

// TestExtractJSONObject_HandlesPreambleAndFences confirms the parser
// strips model preamble and code fences.
func TestExtractJSONObject_HandlesPreambleAndFences(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", `{"verdict":"agrees_truthful"}`, `{"verdict":"agrees_truthful"}`},
		{"with preamble", `Here is my verdict: {"verdict":"disagrees"}`, `{"verdict":"disagrees"}`},
		{"with fence", "```json\n{\"verdict\":\"skipped\"}\n```", `{"verdict":"skipped"}`},
		{"nested", `{"a": {"b": 1}, "c": 2}`, `{"a": {"b": 1}, "c": 2}`},
		{"no object", "just text", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractJSONObject(tc.in)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
