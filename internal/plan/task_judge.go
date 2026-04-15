package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ericmacdougall/stoke/internal/jsonutil"
	"github.com/ericmacdougall/stoke/internal/provider"
)

// TaskWorkVerdict is the LLM reviewer's ruling on a worker's task output.
type TaskWorkVerdict struct {
	// Complete is true when the reviewer judges the task's
	// requirements are actually met by the code the worker produced.
	Complete bool `json:"complete"`

	// Reasoning is the reviewer's explanation. Always populated.
	Reasoning string `json:"reasoning"`

	// GapsFound is a list of concrete gaps the reviewer identified
	// when Complete is false. Each entry describes ONE missing or
	// incorrect thing the worker must address.
	GapsFound []string `json:"gaps_found,omitempty"`

	// FollowupDirective, when Complete is false, contains a focused
	// directive for a follow-up worker. The caller can use this to
	// spawn a repair task BEFORE the session's ACs run, catching
	// gaps early.
	FollowupDirective string `json:"followup_directive,omitempty"`
}

// TaskReviewInput bundles what the reviewer needs.
type TaskReviewInput struct {
	// Task is the task the worker was assigned.
	Task Task

	// SOWSpec is the relevant spec excerpt covering this task.
	SOWSpec string

	// SessionAcceptance is the session's acceptance criteria so
	// the reviewer knows what downstream verification will check.
	SessionAcceptance []AcceptanceCriterion

	// CodeExcerpts is map path -> content of files the worker wrote
	// or was expected to modify.
	CodeExcerpts map[string]string

	// WorkerSummary is what the worker claimed it did (the final
	// agent message). Reviewer sanity-checks this against the code.
	WorkerSummary string

	// PriorAttempts counts how many follow-up attempts have already
	// tried to close gaps on this task's scope. Enables the reviewer
	// to reason about whether continued follow-ups are productive or
	// whether the remaining gap needs further decomposition into
	// smaller pieces.
	PriorAttempts int

	// PriorGaps is the list of gaps previous reviews flagged, so
	// the current review can see what's been tried and avoid
	// re-flagging things already attempted.
	PriorGaps []string

	// LiveCompileErrors is the authoritative list of compile errors
	// currently present in files this task touched. Supplied by the
	// live BuildWatcher (tsc / go / cargo / pyright). Treated as
	// ground truth — the reviewer must NOT second-guess whether these
	// are real errors. Errors in files this task owns are in-scope
	// gaps even if the reviewer would otherwise apply scope
	// discipline and skip them.
	LiveCompileErrors []CompileError

	// UniversalPromptBlock is the rendered universal-context block
	// (coding-standards + known-gotchas) built by
	// skill.LoadUniversalContext(...).PromptBlock(). Empty is fine —
	// the injection code skips blank blocks. See internal/skill for
	// the source of truth.
	UniversalPromptBlock string
}

// DecomposeInput asks the decomposer to split a single stuck gap into
// smaller sub-problems that can be fixed independently. Used when a
// follow-up itself produces code the reviewer STILL rejects — instead
// of another identical follow-up, decompose the remaining work.
type DecomposeInput struct {
	// OriginalTask is the task scope we're still trying to complete.
	OriginalTask Task

	// StuckGap is the gap the reviewer keeps flagging across
	// multiple follow-up attempts.
	StuckGap string

	// PriorDirectives is the list of follow-up directives that have
	// already been tried and failed to close this gap.
	PriorDirectives []string

	// CodeState is the current on-disk state of files relevant to
	// the gap.
	CodeState map[string]string

	// SOWSpec is the task-relevant spec excerpt.
	SOWSpec string

	// UniversalPromptBlock carries the universal coding-standards +
	// known-gotchas block for injection into the decomposer prompt.
	UniversalPromptBlock string
}

// DecomposeVerdict is the decomposer's output: a list of narrower
// sub-problems, each phrased as a concrete follow-up directive.
type DecomposeVerdict struct {
	// Reasoning is why the decomposer chose this split.
	Reasoning string `json:"reasoning"`

	// SubDirectives is the list of narrower follow-up directives.
	// Each should be independently fixable and together they should
	// close the stuck gap.
	SubDirectives []string `json:"sub_directives"`

	// Abandon is true when the decomposer judges the gap
	// structurally unfixable with the current task's scope. Caller
	// should escalate rather than spawn more workers.
	Abandon bool `json:"abandon"`

	// AbandonReason explains why, when Abandon is true.
	AbandonReason string `json:"abandon_reason,omitempty"`
}

// ReviewTaskWork consults the LLM to judge whether a worker's task
// completion actually satisfies the task's requirements. Called after
// every task completes, before the session's ACs fire, so problems
// are caught at the narrowest possible scope.
//
// Returns nil verdict + nil error when no provider is configured.
func ReviewTaskWork(ctx context.Context, prov provider.Provider, model string, in TaskReviewInput) (*TaskWorkVerdict, error) {
	if prov == nil {
		return nil, nil
	}
	if model == "" {
		model = "claude-sonnet-4-6"
	}

	var b strings.Builder
	b.WriteString(taskReviewPrompt)
	if strings.TrimSpace(in.UniversalPromptBlock) != "" {
		b.WriteString("\n\n")
		b.WriteString(in.UniversalPromptBlock)
	}
	b.WriteString("\n\n")

	fmt.Fprintf(&b, "TASK %s: %s\n", in.Task.ID, in.Task.Description)
	if len(in.Task.Files) > 0 {
		fmt.Fprintf(&b, "  expected files: %s\n", strings.Join(in.Task.Files, ", "))
	}
	b.WriteString("\n")

	if strings.TrimSpace(in.SOWSpec) != "" {
		b.WriteString("SOW SPEC EXCERPT:\n")
		b.WriteString(truncateForReasoning(in.SOWSpec, 5000))
		b.WriteString("\n\n")
	}

	if len(in.SessionAcceptance) > 0 {
		b.WriteString("SESSION ACCEPTANCE CRITERIA (what the session as a whole must pass):\n")
		for _, ac := range in.SessionAcceptance {
			fmt.Fprintf(&b, "  - [%s] %s\n", ac.ID, ac.Description)
		}
		b.WriteString("\n")
	}

	if strings.TrimSpace(in.WorkerSummary) != "" {
		b.WriteString("WORKER'S FINAL SUMMARY (what the agent said it did):\n")
		b.WriteString(truncateForReasoning(in.WorkerSummary, 2000))
		b.WriteString("\n\n")
	}

	if len(in.CodeExcerpts) > 0 {
		b.WriteString("CODE THE WORKER PRODUCED:\n")
		paths := sortedKeys(in.CodeExcerpts)
		for _, p := range paths {
			fmt.Fprintf(&b, "\n--- %s ---\n", p)
			b.WriteString(truncateForReasoning(in.CodeExcerpts[p], 3000))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if len(in.LiveCompileErrors) > 0 {
		b.WriteString("LIVE COMPILE ERRORS THIS TASK TOUCHED (authoritative — emitted by the compiler in watch mode; do NOT second-guess whether they are real):\n")
		for _, e := range in.LiveCompileErrors {
			code := ""
			if e.Code != "" {
				code = " [" + e.Code + "]"
			}
			if e.Line > 0 && e.Column > 0 {
				fmt.Fprintf(&b, "  - %s:%d:%d%s %s\n", e.File, e.Line, e.Column, code, e.Message)
			} else if e.Line > 0 {
				fmt.Fprintf(&b, "  - %s:%d%s %s\n", e.File, e.Line, code, e.Message)
			} else {
				fmt.Fprintf(&b, "  - %s%s %s\n", e.File, code, e.Message)
			}
		}
		b.WriteString("\nSCOPE EXCEPTION: compile errors listed above are in files this task touched, so they ARE the task's responsibility — mark the task incomplete and emit a followup_directive that resolves every error in the list. Do NOT apply scope discipline to these.\n\n")
	}

	b.WriteString("Output the JSON verdict described in the system prompt.")

	userContent, _ := json.Marshal([]map[string]interface{}{{"type": "text", "text": b.String()}})
	resp, err := prov.Chat(provider.ChatRequest{
		Model:     model,
		MaxTokens: 6000,
		Messages:  []provider.ChatMessage{{Role: "user", Content: userContent}},
	})
	if err != nil {
		return nil, fmt.Errorf("task reviewer chat: %w", err)
	}
	raw, _ := collectModelText(resp)
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("task reviewer returned no content")
	}

	var verdict TaskWorkVerdict
	if _, err := jsonutil.ExtractJSONInto(raw, &verdict); err != nil {
		return nil, fmt.Errorf("parse task reviewer verdict: %w", err)
	}
	return &verdict, nil
}

// DecomposeTaskGap consults the LLM to split a stuck gap into smaller
// sub-problems. Called when prior follow-up directives failed to close
// the gap — rather than another identical directive, recursively
// decompose into narrower pieces that a worker CAN fix.
//
// Returns nil + nil when no provider is configured.
func DecomposeTaskGap(ctx context.Context, prov provider.Provider, model string, in DecomposeInput) (*DecomposeVerdict, error) {
	if prov == nil {
		return nil, nil
	}
	if model == "" {
		model = "claude-sonnet-4-6"
	}

	var b strings.Builder
	b.WriteString(decomposePrompt)
	if strings.TrimSpace(in.UniversalPromptBlock) != "" {
		b.WriteString("\n\n")
		b.WriteString(in.UniversalPromptBlock)
	}
	b.WriteString("\n\n")

	fmt.Fprintf(&b, "ORIGINAL TASK %s: %s\n", in.OriginalTask.ID, in.OriginalTask.Description)
	if len(in.OriginalTask.Files) > 0 {
		fmt.Fprintf(&b, "  files: %s\n", strings.Join(in.OriginalTask.Files, ", "))
	}
	b.WriteString("\n")

	b.WriteString("STUCK GAP (reviewer keeps flagging this, multiple follow-ups haven't closed it):\n")
	b.WriteString(in.StuckGap)
	b.WriteString("\n\n")

	if len(in.PriorDirectives) > 0 {
		b.WriteString("PRIOR FOLLOW-UP DIRECTIVES THAT ALREADY FAILED:\n")
		for i, d := range in.PriorDirectives {
			fmt.Fprintf(&b, "  %d. %s\n", i+1, d)
		}
		b.WriteString("\n")
	}

	if strings.TrimSpace(in.SOWSpec) != "" {
		b.WriteString("SPEC EXCERPT:\n")
		b.WriteString(truncateForReasoning(in.SOWSpec, 3000))
		b.WriteString("\n\n")
	}

	if len(in.CodeState) > 0 {
		b.WriteString("CURRENT CODE STATE:\n")
		paths := sortedKeys(in.CodeState)
		for _, p := range paths {
			fmt.Fprintf(&b, "\n--- %s ---\n", p)
			b.WriteString(truncateForReasoning(in.CodeState[p], 2500))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("Output the JSON verdict described in the system prompt.")

	userContent, _ := json.Marshal([]map[string]interface{}{{"type": "text", "text": b.String()}})
	resp, err := prov.Chat(provider.ChatRequest{
		Model:     model,
		MaxTokens: 6000,
		Messages:  []provider.ChatMessage{{Role: "user", Content: userContent}},
	})
	if err != nil {
		return nil, fmt.Errorf("decomposer chat: %w", err)
	}
	raw, _ := collectModelText(resp)
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("decomposer returned no content")
	}

	var verdict DecomposeVerdict
	if _, err := jsonutil.ExtractJSONInto(raw, &verdict); err != nil {
		return nil, fmt.Errorf("parse decomposer verdict: %w", err)
	}
	return &verdict, nil
}

const decomposePrompt = `You are a tech lead decomposing a stuck work item. Prior follow-up directives have failed to close a gap on a specific task. Your job: split the remaining work into narrower, independently-achievable sub-problems that a worker CAN actually complete, OR determine that the gap is structurally unfixable.

When to decompose into sub-directives:
  - The stuck gap is genuinely multi-part ('fix the auth middleware AND add the rate limiter AND wire the test suite')
  - Prior directives asked for too much at once and the worker kept missing pieces
  - The gap touches multiple files where each file has its own fix

When to abandon:
  - The gap is about something the task's scope doesn't actually own (e.g. an imported package needs to change — that's a different task's responsibility)
  - The gap requires external services, credentials, or manual steps
  - Prior directives have already been narrow AND the worker still can't produce code that satisfies the reviewer — suggests the gap is not mechanically fixable

When decomposing, each sub-directive must be:
  - ONE concrete action on ONE file (or at most two files)
  - Scoped small enough that a worker can complete it in a few tool calls
  - Independently verifiable: completing just this sub-directive produces observable progress
  - Non-overlapping with other sub-directives

Be honest about abandonment. It's better to escalate gracefully than to keep spawning workers that can't make progress.

Output ONLY a single JSON object — no prose, no backticks:

{
  "reasoning": "one paragraph explaining the split or the abandon decision",
  "sub_directives": [
    "Open apps/web/middleware.ts and add the JWT verification call using jose.jwtVerify",
    "Open apps/web/lib/auth.ts and export a validateToken helper that wraps the jose call"
  ],
  "abandon": false,
  "abandon_reason": "only when abandon is true"
}

`

const taskReviewPrompt = `You are a senior code reviewer checking a single task's completion BEFORE the session's acceptance criteria run. The worker agent just finished writing code for this task. Your job: decide whether the task is actually COMPLETE per the TASK DESCRIPTION — not per an ideal implementation.

SCOPE DISCIPLINE (the most important rule):

  Only flag gaps for things the TASK DESCRIPTION explicitly required, OR things that would cause the SESSION'S declared ACCEPTANCE CRITERIA to fail. Do NOT flag:

    - Missing fields that could be added later (e.g. "you should also add a BuildingUpdate type")
    - Additional error handling beyond what the task asked for
    - Extra tests that weren't requested
    - Documentation that wasn't part of the task
    - Features from the SOW that belong to OTHER tasks in the session

  If a "gap" is out-of-scope polish that another task or future session will handle, mark complete: true with a note like "could also do X but that's out of scope for this task — mentioning for awareness".

  SCOPE EXCEPTION — LIVE COMPILE ERRORS: when the user message below includes a "LIVE COMPILE ERRORS THIS TASK TOUCHED" block, every error listed there is IN SCOPE for this task regardless of the scope-discipline rules above. Those errors come directly from the compiler / type-checker, not from LLM judgement — they are ground truth. Mark the task incomplete and emit a followup_directive that resolves them.

A task is COMPLETE when:
  - Every requirement IN THE TASK DESCRIPTION has concrete code supporting it
  - Every file listed in "expected files" exists with real content (not empty stubs)
  - The code implements the BEHAVIOR the task asked for
  - The code won't cause the session's listed acceptance criteria to fail
  - The task's contribution to the session as a whole is self-contained and won't block downstream tasks

A task is NOT COMPLETE when:
  - Expected files are missing or contain only stub content (e.g. one-line re-exports, empty functions)
  - The worker's summary claims something that isn't actually in the code
  - A requirement the TASK DESCRIPTION stated has no corresponding code
  - Imports reference identifiers that don't exist
  - The code won't compile OR will definitely fail a session AC
  - The code contains unfinished-work comment markers

Bias HEAVILY toward "complete" when the core requirement is met. One narrow, concrete gap that will cause a session AC failure is worth flagging. A vague concern about "could be more comprehensive" is NOT.

When gaps ARE worth flagging:
  - List each gap as ONE sentence describing what's missing or wrong
  - Emit a followup_directive that tells the next worker exactly what to do, citing the session AC it would unblock:
    "Open apps/web/hooks/useAuth.ts and add the useAuth hook that wraps AuthContext with useContext — needed to pass AC4 'auth middleware includes JWT validation'."

When the task IS complete, explain briefly why you're confident (cite specific file + identifier). Don't manufacture reasons to flag it as incomplete just because the file could be richer.

Output ONLY a single JSON object — no prose, no backticks:

{
  "complete": true | false,
  "reasoning": "one paragraph explaining your verdict with specific file/identifier references",
  "gaps_found": ["gap 1", "gap 2"],
  "followup_directive": "concrete instruction for the next worker (only when complete is false)"
}

`
