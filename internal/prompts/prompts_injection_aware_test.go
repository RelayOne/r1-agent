package prompts

import (
	"strings"
	"testing"
)

// TestReviewerPrompt_ContainsInjectionAwarenessSection covers spec §T4
// item 17: the rendered reviewer prompt includes the awareness
// briefing + the <injection_corpus_signatures> block + at least 8
// signature names (one per baked-in pattern).
func TestReviewerPrompt_ContainsInjectionAwarenessSection(t *testing.T) {
	rendered := BuildVerifyPromptWithInjectionAwareness("a-task", []string{"ck1"}, "a.go", "b.go")

	mustHave := []string{
		"Prompt-Injection Awareness",
		"<injection_corpus_signatures>",
		"</injection_corpus_signatures>",
		"PRIMA-FACIE",
		"promptguard_note",
	}
	for _, m := range mustHave {
		if !strings.Contains(rendered, m) {
			t.Errorf("rendered prompt missing %q", m)
		}
	}

	// At least 8 signature lines (8 builtin patterns + leetspeak).
	sigLines := strings.Count(rendered, "\n- ")
	if sigLines < 8 {
		t.Errorf("want >=8 signature lines (one per builtin pattern), got %d", sigLines)
	}
}

// TestBuildInjectionAwarenessSection_StandaloneShape covers the
// standalone-section accessor so callers that build a custom reviewer
// prompt template can pull the section without going through the
// full BuildVerifyPromptWithInjectionAwareness wrapper.
func TestBuildInjectionAwarenessSection_StandaloneShape(t *testing.T) {
	got := BuildInjectionAwarenessSection()
	if !strings.Contains(got, "<injection_corpus_signatures>") {
		t.Error("missing signature block")
	}
	if !strings.Contains(got, "Prompt-Injection Awareness") {
		t.Error("missing section header")
	}
	if len(got) > 4096 {
		t.Errorf("section too large for cache-aligned segment: %d bytes", len(got))
	}
}
