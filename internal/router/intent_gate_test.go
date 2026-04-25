package router

import (
	"context"
	"errors"
	"testing"

	harnessTools "github.com/RelayOne/r1-agent/internal/harness/tools"
)

// TestClassifyIntentDeterministic covers the regex table from the
// executor-foundation spec: implement verbs, diagnose verbs, the
// both-match-wins-implement rule, and the empty / unclear fallbacks.
func TestClassifyIntentDeterministic(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		want   Intent
		wantOK bool
	}{
		{"implement verb", "implement a retry", IntentImplement, true},
		{"fix verb", "fix the nil deref in parse.go", IntentImplement, true},
		{"deploy verb", "deploy the new build", IntentImplement, true},
		{"ship verb", "ship this change", IntentImplement, true},
		{"diagnose verb", "diagnose the slow boot", IntentDiagnose, true},
		{"why verb", "why is the build red", IntentDiagnose, true},
		{"analyze verb", "analyze the memory spike", IntentDiagnose, true},
		{"both-match-implement-wins", "investigate and fix the leak", IntentImplement, true},
		{"neither", "hmm do the thing", IntentAmbiguous, false},
		{"empty", "", IntentAmbiguous, false},
		{"whitespace", "  \t\n  ", IntentAmbiguous, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ClassifyIntentDeterministic(tc.input)
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("ClassifyIntentDeterministic(%q) = (%v,%v), want (%v,%v)",
					tc.input, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// TestClassifyIntentLLMFallback exercises the wrapper's escalation
// path: deterministic miss -> call the injected LLM stub.
func TestClassifyIntentLLMFallback(t *testing.T) {
	calls := 0
	llm := func(_ context.Context, in string) (Intent, error) {
		calls++
		if in == "" {
			t.Fatalf("LLM should never see empty input")
		}
		return IntentDiagnose, nil
	}
	// Deterministic hit — LLM not consulted.
	got, err := ClassifyIntent(context.Background(), "implement retries", llm)
	if err != nil || got != IntentImplement {
		t.Fatalf("hit path: got (%v,%v), want (implement,nil)", got, err)
	}
	if calls != 0 {
		t.Fatalf("LLM consulted on deterministic hit; calls=%d", calls)
	}
	// Deterministic miss — LLM consulted, stub verdict returned.
	got, err = ClassifyIntent(context.Background(), "hmm the thing", llm)
	if err != nil || got != IntentDiagnose {
		t.Fatalf("miss path: got (%v,%v), want (diagnose,nil)", got, err)
	}
	if calls != 1 {
		t.Fatalf("LLM not consulted on miss; calls=%d", calls)
	}
}

// TestClassifyIntentLLMError asserts the stub error path returns
// IntentAmbiguous (safe default) rather than propagating the error
// as a classification.
func TestClassifyIntentLLMError(t *testing.T) {
	boom := errors.New("haiku timeout")
	llm := func(_ context.Context, _ string) (Intent, error) {
		return IntentUnknown, boom
	}
	got, err := ClassifyIntent(context.Background(), "hmm the thing", llm)
	if !errors.Is(err, boom) {
		t.Errorf("error not surfaced; got %v want wrapping %v", err, boom)
	}
	if got != IntentAmbiguous {
		t.Errorf("intent on LLM error = %v, want ambiguous", got)
	}
}

// TestClassifyIntentLLMNil asserts the deterministic-miss + no-LLM
// path returns the safe default without error.
func TestClassifyIntentLLMNil(t *testing.T) {
	got, err := ClassifyIntent(context.Background(), "hmm the thing", nil)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got != IntentAmbiguous {
		t.Errorf("intent = %v, want ambiguous", got)
	}
}

// TestClampReadOnly checks the write-capable tool filter. The input
// blends read + write tools; the output keeps only reads.
func TestClampReadOnly(t *testing.T) {
	in := []harnessTools.ToolName{
		harnessTools.ToolFileRead,
		harnessTools.ToolFileWrite,
		harnessTools.ToolCodeRun,
		harnessTools.ToolWebSearch,
		harnessTools.ToolLedgerQuery,
		harnessTools.ToolLedgerWrite,
		harnessTools.ToolEnvExec,
	}
	out := ClampReadOnly(in)
	want := map[harnessTools.ToolName]bool{
		harnessTools.ToolFileRead:    true,
		harnessTools.ToolWebSearch:   true,
		harnessTools.ToolLedgerQuery: true,
	}
	if len(out) != len(want) {
		t.Fatalf("ClampReadOnly len = %d, want %d (%v)", len(out), len(want), out)
	}
	for _, t2 := range out {
		if !want[t2] {
			t.Errorf("unexpected tool survived clamp: %v", t2)
		}
	}
	// Empty input -> nil output.
	if got := ClampReadOnly(nil); got != nil {
		t.Errorf("ClampReadOnly(nil) = %v, want nil", got)
	}
}

// TestGateImplementPassThrough: IMPLEMENT inputs receive the full
// tool set, unclamped.
func TestGateImplementPassThrough(t *testing.T) {
	tools := []harnessTools.ToolName{
		harnessTools.ToolFileRead,
		harnessTools.ToolFileWrite,
		harnessTools.ToolCodeRun,
	}
	res, err := Gate(context.Background(), "implement the retry logic", tools, nil)
	if err != nil {
		t.Fatalf("Gate err = %v, want nil", err)
	}
	if res.Intent != IntentImplement {
		t.Errorf("intent = %v, want implement", res.Intent)
	}
	if res.Clamped {
		t.Errorf("Clamped = true, want false on IMPLEMENT")
	}
	if len(res.Tools) != len(tools) {
		t.Errorf("Tools len = %d, want %d", len(res.Tools), len(tools))
	}
}

// TestGateDiagnoseClamps: DIAGNOSE inputs strip write tools.
func TestGateDiagnoseClamps(t *testing.T) {
	tools := []harnessTools.ToolName{
		harnessTools.ToolFileRead,
		harnessTools.ToolFileWrite,
	}
	res, err := Gate(context.Background(), "why is the build red", tools, nil)
	if err != nil {
		t.Fatalf("Gate err = %v, want nil", err)
	}
	if res.Intent != IntentDiagnose {
		t.Errorf("intent = %v, want diagnose", res.Intent)
	}
	if !res.Clamped {
		t.Errorf("Clamped = false, want true when a write tool was dropped")
	}
	for _, tn := range res.Tools {
		if tn == harnessTools.ToolFileWrite {
			t.Errorf("ToolFileWrite leaked through diagnose clamp")
		}
	}
}

// TestGateAmbiguousClamps: AMBIGUOUS inputs also clamp (safe
// default).
func TestGateAmbiguousClamps(t *testing.T) {
	tools := []harnessTools.ToolName{
		harnessTools.ToolFileRead,
		harnessTools.ToolFileWrite,
	}
	res, err := Gate(context.Background(), "hmm the thing", tools, nil)
	if err != nil {
		t.Fatalf("Gate err = %v, want nil", err)
	}
	if res.Intent != IntentAmbiguous {
		t.Errorf("intent = %v, want ambiguous", res.Intent)
	}
	if !res.Clamped {
		t.Errorf("Clamped = false, want true on AMBIGUOUS")
	}
}

// TestGateEmptyInput asserts the sentinel error path.
func TestGateEmptyInput(t *testing.T) {
	tools := []harnessTools.ToolName{harnessTools.ToolFileRead}
	_, err := Gate(context.Background(), "  ", tools, nil)
	if !errors.Is(err, ErrEmptyIntentInput) {
		t.Fatalf("err = %v, want ErrEmptyIntentInput", err)
	}
}

// TestIntentString guards the label stability — other code uses
// these for bus-event `intent` fields.
func TestIntentString(t *testing.T) {
	cases := map[Intent]string{
		IntentImplement: "implement",
		IntentDiagnose:  "diagnose",
		IntentAmbiguous: "ambiguous",
		IntentUnknown:   "unknown",
	}
	for in, want := range cases {
		if got := in.String(); got != want {
			t.Errorf("Intent(%d).String() = %q, want %q", in, got, want)
		}
	}
}
