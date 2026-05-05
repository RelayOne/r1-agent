package planupdate

import (
	"strings"
	"testing"
)

// expectedSystemPrompt is the verbatim system prompt from
// specs/cortex-concerns.md §4 (lines 163–184). The test asserts
// byte-for-byte equality so any drift in either the spec or the
// constant is a compile-time review trigger.
//
// IMPORTANT: edit BOTH the spec and this fixture together. The fixture
// is the authoritative byte-equality reference for CI; the spec is the
// authoritative source of truth for human review. The two are kept in
// lockstep by this test.
const expectedSystemPrompt = `You are PlanUpdateLobe, a background helper inside the r1-agent cortex. You watch the conversation transcript and the current plan.json and propose minimal updates. You DO NOT write to plan.json yourself; you propose JSON deltas that the main agent decides whether to apply.

Goals (in priority order):
1. Detect newly-introduced tasks the user mentioned but the plan does not yet contain.
2. Detect newly-implied dependencies between existing tasks.
3. Detect plan items that the conversation has rendered obsolete (cancelled, completed out-of-band, scope-cut).
4. NEVER invent tasks the user did not actually request. Bias toward silence.

Output format — return ONLY this JSON object, no prose:
{
  "additions":   [{"id":"<short-slug>","title":"...","deps":["<existing-id>",...]}],
  "removals":    [{"id":"<existing-id>","reason":"..."}],
  "edits":       [{"id":"<existing-id>","field":"title|deps|priority","new":"..."}],
  "confidence":  0.0-1.0,
  "rationale":   "one sentence per non-trivial proposal"
}

Rules:
- If confidence < 0.6, return empty arrays. Silence > noise.
- Edits to existing items will auto-apply. Additions and removals require user confirmation — phrase them tentatively.
- Use existing task IDs verbatim. Never renumber.
- Do NOT include conversational text outside the JSON object.`

// TestPlanUpdateLobe_SystemPromptByteEqual covers TASK-18.
//
// Asserts byte-for-byte equality between the planUpdateSystemPrompt
// constant (sent to Haiku 4.5) and the expected fixture above. The
// fixture mirrors specs/cortex-concerns.md §4 verbatim. Drift in
// either direction is a regression — even a single trailing space
// busts the cache key on every Lobe call across every session.
func TestPlanUpdateLobe_SystemPromptByteEqual(t *testing.T) {
	t.Parallel()

	if planUpdateSystemPrompt != expectedSystemPrompt {
		t.Errorf("planUpdateSystemPrompt drifted from spec §4 verbatim text\n"+
			"want length %d, got length %d\n"+
			"first diff at byte %d",
			len(expectedSystemPrompt), len(planUpdateSystemPrompt),
			firstDiffByte(planUpdateSystemPrompt, expectedSystemPrompt))
	}

	// Sanity checks beyond the byte-equality so a future spec edit
	// has a richer error trail. Each phrase is load-bearing per the
	// spec rationale.
	mustContain := []string{
		"You are PlanUpdateLobe",
		"plan.json",
		"You DO NOT write to plan.json yourself",
		"\"additions\":",
		"\"removals\":",
		"\"edits\":",
		"confidence < 0.6",
		"Silence > noise.",
		"Use existing task IDs verbatim",
	}
	for _, p := range mustContain {
		if !strings.Contains(planUpdateSystemPrompt, p) {
			t.Errorf("system prompt missing required phrase %q", p)
		}
	}
}

// TestPlanUpdateLobe_HaikuConstants covers the model + max-tokens
// invariants from spec item 18 (Model="claude-haiku-4-5", MaxTokens=800).
func TestPlanUpdateLobe_HaikuConstants(t *testing.T) {
	t.Parallel()
	if got, want := planUpdateModel, "claude-haiku-4-5"; got != want {
		t.Errorf("planUpdateModel = %q, want %q", got, want)
	}
	if got, want := planUpdateMaxTokens, 800; got != want {
		t.Errorf("planUpdateMaxTokens = %d, want %d", got, want)
	}
}

// firstDiffByte returns the byte offset of the first difference between
// a and b, or -1 if equal. Used in the byte-equality test to point at
// drift quickly.
func firstDiffByte(a, b string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	if len(a) != len(b) {
		return n
	}
	return -1
}
