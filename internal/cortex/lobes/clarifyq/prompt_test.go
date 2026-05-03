package clarifyq

import (
	"strings"
	"testing"
)

// expectedSystemPrompt is the verbatim system prompt from
// specs/cortex-concerns.md §5 (lines 232–246). The test asserts
// byte-for-byte equality so any drift in either the spec or the
// constant is a CI gate failure.
//
// IMPORTANT: edit BOTH the spec and this fixture together. The fixture
// is the authoritative byte-equality reference for CI; the spec is the
// authoritative source of truth for human review. The two are kept in
// lockstep by this test.
const expectedSystemPrompt = `You are ClarifyingQLobe inside r1-agent's cortex. You watch the user's most recent message in context. Your only job is to detect actionable ambiguity and propose at most 3 clarifying questions via the queue_clarifying_question tool.

Definition of actionable ambiguity:
- The user gave an instruction the agent cannot execute correctly without one missing fact.
- Examples: "deploy it" (where? prod?), "make it faster" (what metric? what target?), "fix the test" (which test? what failure?).
- NOT ambiguity: stylistic preferences the agent can reasonably default, follow-ups already implied by recent context, polite chit-chat.

Hard constraints:
- Maximum 3 tool calls. If you have 0 questions, output no tool calls and the assistant message "no_ambiguity".
- Each question must be answerable in ≤2 sentences.
- Never ask "are you sure?" — that is not clarification.
- Never repeat a question already pending in the workspace; you will be told which are pending in the user-message preamble.
- When the conversation contains technical jargon you do not understand, ask only if it changes the action — not for your own education.

Surface order: blocking questions first; then "scope"/"constraint"; then everything else.`

// TestClarifyingQLobe_SystemPromptByteEqual covers TASK-23.
//
// Asserts byte-for-byte equality between the clarifySystemPrompt
// constant (sent to Haiku 4.5) and the expected fixture above. The
// fixture mirrors specs/cortex-concerns.md §5 verbatim. Drift in
// either direction is a regression — even a single trailing space
// busts the cache key on every Lobe call across every session.
func TestClarifyingQLobe_SystemPromptByteEqual(t *testing.T) {
	t.Parallel()

	if clarifySystemPrompt != expectedSystemPrompt {
		t.Errorf("clarifySystemPrompt drifted from spec §5 verbatim text\n"+
			"want length %d, got length %d\n"+
			"first diff at byte %d",
			len(expectedSystemPrompt), len(clarifySystemPrompt),
			firstDiffByte(clarifySystemPrompt, expectedSystemPrompt))
	}

	// Sanity checks beyond the byte-equality so a future spec edit
	// has a richer error trail. Each phrase is load-bearing per the
	// spec rationale.
	mustContain := []string{
		"You are ClarifyingQLobe",
		"queue_clarifying_question",
		"actionable ambiguity",
		"Maximum 3 tool calls",
		"\"no_ambiguity\"",
		"blocking questions first",
	}
	for _, p := range mustContain {
		if !strings.Contains(clarifySystemPrompt, p) {
			t.Errorf("system prompt missing required phrase %q", p)
		}
	}
}

// TestClarifyingQLobe_HaikuConstants pins the model alias and outstanding
// cap so the spec contract is enforced at the type-system level.
func TestClarifyingQLobe_HaikuConstants(t *testing.T) {
	t.Parallel()
	if got, want := clarifyModel, "claude-haiku-4-5"; got != want {
		t.Errorf("clarifyModel = %q, want %q", got, want)
	}
	if got, want := clarifyOutstandingCap, 3; got != want {
		t.Errorf("clarifyOutstandingCap = %d, want %d (spec §5)", got, want)
	}
	if clarifyMaxTokens <= 0 {
		t.Errorf("clarifyMaxTokens = %d, want > 0", clarifyMaxTokens)
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
