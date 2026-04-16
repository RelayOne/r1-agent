package critic

import "testing"

func hasFlag(findings []RedFlagFinding, flag RedFlag) bool {
	for _, f := range findings {
		if f.Flag == flag {
			return true
		}
	}
	return false
}

func TestDetectRedFlags_Overconfidence(t *testing.T) {
	cases := []string{
		"I'm confident this will work",
		"I am confident the change is correct",
		"100% sure about the fix",
		"this definitely works now",
		"guaranteed to work on all inputs",
	}
	for _, c := range cases {
		if !hasFlag(DetectRedFlags(c, true), FlagOverconfidence) {
			t.Errorf("expected overconfidence flag for %q", c)
		}
	}
}

func TestDetectRedFlags_Speculation(t *testing.T) {
	cases := []string{
		"This should work for all cases.",
		"The logic is probably fine.",
		"I think this works but I didn't run it.",
		"Most likely works in production.",
		"Seems to work in my head.",
		"I assume this is enough.",
	}
	for _, c := range cases {
		if !hasFlag(DetectRedFlags(c, true), FlagSpeculation) {
			t.Errorf("expected speculation flag for %q", c)
		}
	}
}

func TestDetectRedFlags_Deferral(t *testing.T) {
	cases := []string{
		"Tests will be added later.",
		"TODO: verify edge cases",
		"TODO: add tests for the error path",
		"Will add tests in a follow-up",
	}
	for _, c := range cases {
		got := DetectRedFlags(c, true)
		if !hasFlag(got, FlagDeferralToFuture) {
			t.Errorf("expected deferral flag for %q, got %+v", c, got)
		}
	}
}

func TestDetectRedFlags_HandWave(t *testing.T) {
	cases := []string{
		"The function basically works as expected.",
		"Handles most cases correctly.",
		"More or less correct for the common path.",
	}
	for _, c := range cases {
		if !hasFlag(DetectRedFlags(c, true), FlagHandWave) {
			t.Errorf("expected hand-wave flag for %q", c)
		}
	}
}

func TestDetectRedFlags_EvidenceClaim_NoTools(t *testing.T) {
	// When didRunTools=false, evidence claims fire.
	text := "I verified the build passes."
	if !hasFlag(DetectRedFlags(text, false), FlagEvidenceClaim) {
		t.Error("evidence claim without tools should fire")
	}
}

func TestDetectRedFlags_EvidenceClaim_WithTools(t *testing.T) {
	// When didRunTools=true, evidence claims DON'T fire — the
	// caller is asserting the agent actually ran tools, so the
	// claim is plausibly backed.
	text := "I verified the build passes."
	if hasFlag(DetectRedFlags(text, true), FlagEvidenceClaim) {
		t.Error("evidence claim with tools run should NOT fire")
	}
}

func TestDetectRedFlags_CleanTextProducesNothing(t *testing.T) {
	text := `Ran pnpm --filter @scope/foo build; exit 0.
Added missing "clsx" dep to packages/foo/package.json.
Committed as abc1234.`
	got := DetectRedFlags(text, true)
	if len(got) != 0 {
		t.Errorf("clean text produced findings: %+v", got)
	}
}

func TestDetectRedFlags_NoFalsePositiveOnSubstring(t *testing.T) {
	// "probability" should NOT match "probably" via substring.
	// Word boundaries prevent that.
	text := "The probability distribution is symmetric."
	got := DetectRedFlags(text, true)
	for _, f := range got {
		if f.Flag == FlagSpeculation {
			t.Errorf("false positive speculation flag on %q (match=%q)", text, f.Phrase)
		}
	}
}

func TestDetectRedFlags_LineNumbersCorrect(t *testing.T) {
	text := "line 1\nI'm confident\nline 3"
	got := DetectRedFlags(text, true)
	if len(got) == 0 {
		t.Fatal("expected finding")
	}
	if got[0].Line != 2 {
		t.Errorf("Line=%d want 2", got[0].Line)
	}
}

func TestDetectRedFlags_EmptyReturnsNil(t *testing.T) {
	if got := DetectRedFlags("", true); got != nil {
		t.Errorf("empty input returned %+v want nil", got)
	}
}

func TestDetectRedFlags_DeterministicOrder(t *testing.T) {
	text := "I'm confident.\nShould work.\nProbably fine."
	// Run twice and compare.
	a := DetectRedFlags(text, true)
	b := DetectRedFlags(text, true)
	if len(a) != len(b) {
		t.Fatalf("lengths differ: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Errorf("findings differ at %d: %+v vs %+v", i, a[i], b[i])
		}
	}
}
