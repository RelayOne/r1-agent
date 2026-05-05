// System prompt for the ClarifyingQLobe Haiku call (spec item 23).
//
// The constant clarifySystemPrompt is copied verbatim from
// specs/cortex-concerns.md §5 ("ClarifyingQLobe — System prompt
// (verbatim, copy-paste-ready)"). The byte-equality test in
// prompt_test.go protects this constant against accidental edits;
// the spec text is the canonical source and any drift is a regression.
//
// Format note: the spec ships the prompt inside a 2-space-indented
// markdown bullet list with the prompt body framed by a ```text fence.
// We strip the indentation when copying because the indentation is a
// rendering artefact of the bullet list — the prompt itself begins
// at column 0 in copy-paste-ready form (a model would not benefit
// from a uniform 2-space prefix, and including it would burn cache
// space without affecting output quality).
package clarifyq

// clarifySystemPrompt is the verbatim system prompt sent to Haiku
// 4.5 on every triggered Run. Copied byte-for-byte from
// specs/cortex-concerns.md §5 lines 232–246.
const clarifySystemPrompt = `You are ClarifyingQLobe inside r1-agent's cortex. You watch the user's most recent message in context. Your only job is to detect actionable ambiguity and propose at most 3 clarifying questions via the queue_clarifying_question tool.

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

// clarifyModel is the Haiku model alias used in the LobePromptBuilder.
// Spec §5 (per spec §Stack & Versions) fixes LLM Lobes at
// "claude-haiku-4-5".
const clarifyModel = "claude-haiku-4-5"

// clarifyMaxTokens caps the Haiku output. The spec does not name an
// explicit token cap for ClarifyingQLobe; we mirror the LobePromptBuilder
// default of 500 (spec §LobePromptBuilder MaxTokens). Three tool_use
// blocks plus the "no_ambiguity" fallback message comfortably fit; this
// prevents a runaway stream on a misbehaving model since cancellation
// does not refund tokens (RT-CONCURRENT §4).
const clarifyMaxTokens = 500

// clarifyOutstandingCap is the spec-fixed maximum number of unresolved
// clarifying-question Notes the Lobe will publish at any one time. Tool
// calls beyond this cap are silently dropped per spec item 24
// ("cap outstanding clarify Notes at 3 (drop overflow tool calls
// silently)").
const clarifyOutstandingCap = 3
