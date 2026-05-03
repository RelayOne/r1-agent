package memorycurator

import (
	"strings"
	"testing"
)

// expectedSystemPrompt is the verbatim system prompt from
// specs/cortex-concerns.md §6 (lines 307–333). The test asserts
// byte-for-byte equality so any drift in either the spec or the
// constant is a CI gate failure.
//
// IMPORTANT: edit BOTH the spec and this fixture together. The fixture
// is the authoritative byte-equality reference for CI; the spec is the
// authoritative source of truth for human review. The two are kept in
// lockstep by this test.
const expectedSystemPrompt = `You are MemoryCuratorLobe inside r1-agent's cortex. You read recent conversation segments and decide whether anything is worth durably remembering.

WRITE only when ALL of the following hold:
1. The fact is project-scoped (codebase facts, build commands, conventions, deploy targets, naming patterns) — not personal, not transient, not session-specific.
2. The user or the agent has already validated the fact in this session (it is stated, not hypothesized).
3. The fact would still be useful 30 days from now.
4. The fact is not already in memory (you will see existing memory excerpts in the user-message preamble).

REFUSE TO WRITE when:
- The source message is tagged "private" or contains personal/identifying details.
- The fact is a one-off bug that has already been fixed.
- The fact is the user's mood, tone, or social preference (e.g. "user prefers terse replies"). Those are not codebase facts.
- You are uncertain — bias toward silence.

Output: zero or more remember() tool calls. After tool calls, output the assistant message "curated_<N>" where N is the count, or "curated_0" if none.

Categories — pick exactly one per call:
- fact: codebase facts (file layout, command-to-run, deploy target).
- gotcha: a footgun that bit somebody and would bite again.
- pattern: a recurring solution the project uses.
- preference: explicit user preference about how code should be written.
- anti_pattern: an approach the project explicitly avoids.
- fix: a specific repair pattern proven to work for a class of bug.

Default category: fact. The harness only auto-applies "fact" without asking the user; other categories will be queued for confirmation.`

// TestMemoryCuratorLobe_SystemPromptByteEqual covers TASK-28.
//
// Asserts byte-for-byte equality between the curatorSystemPrompt
// constant (sent to Haiku 4.5) and the expected fixture above. The
// fixture mirrors specs/cortex-concerns.md §6 verbatim. Drift in
// either direction is a regression — even a single trailing space
// busts the cache key on every Lobe call across every session.
func TestMemoryCuratorLobe_SystemPromptByteEqual(t *testing.T) {
	t.Parallel()

	if curatorSystemPrompt != expectedSystemPrompt {
		t.Errorf("curatorSystemPrompt drifted from spec §6 verbatim text\n"+
			"want length %d, got length %d\n"+
			"first diff at byte %d",
			len(expectedSystemPrompt), len(curatorSystemPrompt),
			firstDiffByte(curatorSystemPrompt, expectedSystemPrompt))
	}

	// Sanity checks beyond the byte-equality so a future spec edit
	// has a richer error trail. Each phrase is load-bearing per the
	// spec rationale.
	mustContain := []string{
		"You are MemoryCuratorLobe",
		"remember() tool calls",
		"curated_<N>",
		"curated_0",
		"REFUSE TO WRITE",
		"project-scoped",
		"Default category: fact",
	}
	for _, p := range mustContain {
		if !strings.Contains(curatorSystemPrompt, p) {
			t.Errorf("system prompt missing required phrase %q", p)
		}
	}
}

// TestMemoryCuratorLobe_HaikuConstants pins the model alias and Lobe
// constants so the spec contract is enforced at the type-system level.
func TestMemoryCuratorLobe_HaikuConstants(t *testing.T) {
	t.Parallel()
	if got, want := curatorModel, "claude-haiku-4-5"; got != want {
		t.Errorf("curatorModel = %q, want %q", got, want)
	}
	if got, want := curatorMaxTokens, 600; got != want {
		t.Errorf("curatorMaxTokens = %d, want %d (spec TASK-29)", got, want)
	}
	if got, want := curatorRecentN, 20; got != want {
		t.Errorf("curatorRecentN = %d, want %d (spec TASK-29)", got, want)
	}
	if got, want := curatorTurnInterval, 5; got != want {
		t.Errorf("curatorTurnInterval = %d, want %d (spec TASK-29)", got, want)
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
