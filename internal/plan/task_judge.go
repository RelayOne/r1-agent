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

	b.WriteString("Output the JSON verdict described in the system prompt.")

	userContent, _ := json.Marshal([]map[string]interface{}{{"type": "text", "text": b.String()}})
	resp, err := prov.Chat(provider.ChatRequest{
		Model:     model,
		MaxTokens: 2500,
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

const taskReviewPrompt = `You are a senior code reviewer checking a single task's completion BEFORE the session's acceptance criteria run. The worker agent just finished writing code for this task. Your job: decide whether the task is actually COMPLETE per the TASK DESCRIPTION — not per an ideal implementation.

SCOPE DISCIPLINE (the most important rule):

  Only flag gaps for things the TASK DESCRIPTION explicitly required, OR things that would cause the SESSION'S declared ACCEPTANCE CRITERIA to fail. Do NOT flag:

    - Missing fields that could be added later (e.g. "you should also add a BuildingUpdate type")
    - Additional error handling beyond what the task asked for
    - Extra tests that weren't requested
    - Documentation that wasn't part of the task
    - Features from the SOW that belong to OTHER tasks in the session

  If a "gap" is out-of-scope polish that another task or future session will handle, mark complete: true with a note like "could also do X but that's out of scope for this task — mentioning for awareness".

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
