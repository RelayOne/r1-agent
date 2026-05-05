// System prompt for the MemoryCuratorLobe Haiku call (spec item 28).
//
// The constant curatorSystemPrompt is copied verbatim from
// specs/cortex-concerns.md §6 ("MemoryCuratorLobe — System prompt
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
package memorycurator

// curatorSystemPrompt is the verbatim system prompt sent to Haiku
// 4.5 on every triggered Run. Copied byte-for-byte from
// specs/cortex-concerns.md §6 lines 307–333.
const curatorSystemPrompt = `You are MemoryCuratorLobe inside r1-agent's cortex. You read recent conversation segments and decide whether anything is worth durably remembering.

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

// curatorModel is the Haiku model alias used in the LobePromptBuilder.
// Spec §6 (per spec §Stack & Versions) fixes LLM Lobes at
// "claude-haiku-4-5".
const curatorModel = "claude-haiku-4-5"

// curatorMaxTokens caps the Haiku output. Spec §6 / TASK-29 fixes the
// per-call output cap at 600 tokens — enough for several remember() tool
// calls plus the "curated_<N>" trailing assistant message, while keeping
// the per-turn cost budget (cortex-concerns spec §G ≤30% of main thread
// output) intact across the 6 LLM Lobes.
const curatorMaxTokens = 600

// curatorRecentN is the size of the message-history tail the Lobe
// renders into the user-message preamble for each Haiku call. Fixed
// at 20 per spec TASK-29 ("Take last N=20 messages").
const curatorRecentN = 20

// curatorTurnInterval is the every-Nth-Run cadence that fires the
// haikuCall pipeline. Fixed at 5 per spec TASK-29 ("every 5 assistant
// turn boundaries"). The task.completed hub event additionally fires
// the pipeline outside this cadence (TASK-29).
const curatorTurnInterval = 5
