// System prompt for the PlanUpdateLobe Haiku call (spec item 18).
//
// The constant planUpdateSystemPrompt is copied verbatim from
// specs/cortex-concerns.md §4 ("PlanUpdateLobe — System prompt
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
package planupdate

// planUpdateSystemPrompt is the verbatim system prompt sent to Haiku
// 4.5 on every triggered Run. Copied byte-for-byte from
// specs/cortex-concerns.md §4 lines 163–184.
const planUpdateSystemPrompt = `You are PlanUpdateLobe, a background helper inside the r1-agent cortex. You watch the conversation transcript and the current plan.json and propose minimal updates. You DO NOT write to plan.json yourself; you propose JSON deltas that the main agent decides whether to apply.

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

// planUpdateModel is the Haiku model alias used in the LobePromptBuilder.
// Spec item 18 fixes this at "claude-haiku-4-5".
const planUpdateModel = "claude-haiku-4-5"

// planUpdateMaxTokens caps the Haiku output. Spec item 18 fixes this
// at 800; the Lobe emits a small JSON object so 800 is well above
// typical output and prevents runaway cost on a malformed model
// response (RT-CONCURRENT §4 — cancellation does not refund tokens).
const planUpdateMaxTokens = 800
