// Package plan — lead-dev briefing phase.
//
// This file implements the "lead dev / tech lead" step that runs BEFORE a
// session's tasks dispatch to workers. The lead dev reads the task list,
// queries the current codebase (repomap + TF-IDF + API surface), and
// produces per-task briefings that carry the exact context each worker
// needs — "here's the current shape of the code you're about to touch,
// here's what already exists, here's what's missing, here's the exact
// identifier names you need to use".
//
// The motivation is the observation that context continuity across tasks
// inside a session is only part of the story. When the session starts,
// task T1 is written based on the SOW spec alone — it doesn't know what
// the current codebase looks like because the codebase hasn't been built
// yet. But tasks T2..TN run AFTER earlier tasks have written real files,
// and those tasks WOULD benefit from a fresh look at the current code
// state. The briefing phase runs per-wave inside a session, re-reads the
// current state, and produces per-task briefings that reflect what's on
// disk NOW — not what the SOW assumed would be there.

package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ericmacdougall/stoke/internal/jsonutil"
	"github.com/ericmacdougall/stoke/internal/provider"
)

// TaskBriefing is what the lead dev hands to a worker before dispatch. It
// augments the bare task.Description with context pulled from the current
// codebase state.
type TaskBriefing struct {
	// TaskID is the task this briefing belongs to.
	TaskID string `json:"task_id"`

	// CurrentState is a plain-language summary of what relevant files
	// currently look like on disk — what exists, what's empty, what's
	// been written by earlier tasks in this session.
	CurrentState string `json:"current_state"`

	// WhatIsMissing is a list of concrete things the task must create
	// or change. Derived by comparing task.Files + session.Outputs
	// against what's actually on disk.
	WhatIsMissing []string `json:"what_is_missing"`

	// Identifiers is a list of existing type/function/constant names
	// this task should reference verbatim. Pulled from the API
	// surface of files the task will likely read.
	Identifiers []string `json:"identifiers,omitempty"`

	// Pitfalls is a free-form list of "things to watch out for" that
	// the lead dev identified. Examples: "earlier task T3 wrote
	// packages/types/src/index.ts with a different export name than
	// the spec — use the actual export name, not the spec's name".
	Pitfalls []string `json:"pitfalls,omitempty"`

	// SuggestedSteps is an ordered, concrete list of the implementation
	// steps the worker should follow. Not a substitute for the
	// worker's own reasoning, but a checklist that catches the
	// "missed a step" failure mode.
	SuggestedSteps []string `json:"suggested_steps,omitempty"`

	// RelevantSkills is a list of skill names the lead dev identified
	// as applicable to this task. Workers should read the gotchas
	// from these skills and follow the conventions they describe.
	// Populated by the lead-dev LLM from the skill references
	// injected into its prompt.
	RelevantSkills []string `json:"relevant_skills,omitempty"`
}

// SessionBriefingInput is everything the lead dev needs to produce
// briefings for one wave of tasks.
type SessionBriefingInput struct {
	// SessionID and SessionTitle are for log context.
	SessionID    string
	SessionTitle string

	// Tasks is the wave of tasks about to dispatch. Each one gets a
	// briefing.
	Tasks []Task

	// AcceptanceCriteria is the session's full AC set so the lead
	// dev knows what the session must ultimately pass. Briefings
	// point each task at the ACs it needs to help satisfy.
	AcceptanceCriteria []AcceptanceCriterion

	// RepoRoot is the workspace root so the lead dev can look at
	// actual file state.
	RepoRoot string

	// APISurface is the pre-computed API-surface text for the
	// current workspace state. Caller should pass whatever its
	// sowAPISurface helper produces; passing empty is fine but
	// reduces briefing quality.
	APISurface string

	// RepoMap is a pre-rendered repo map snippet relevant to the
	// task files. Optional.
	RepoMap string

	// RawSOW is the original SOW text so the lead dev can
	// cross-reference the task against what the spec actually
	// said — catches "task description is abstract, spec has the
	// exact requirements".
	RawSOW string

	// SkillReferences is a pre-formatted block listing the skills
	// most relevant to this session's tasks. The lead dev reads
	// this and includes applicable skill names in each task's
	// RelevantSkills field so workers know which conventions to
	// follow.
	SkillReferences string
}

// BriefingRunner produces task briefings via the LLM.
type BriefingRunner struct {
	Provider provider.Provider
	Model    string
}

// Brief runs the lead-dev pass and returns one briefing per task. The
// returned map is keyed on task.ID. Tasks with no briefing in the map
// can be dispatched without augmentation (equivalent to the pre-
// briefing behavior).
//
// Failure handling: if the provider call fails or the model returns
// invalid JSON, Brief returns an empty map with no error — briefings
// are best-effort context. The worker dispatch still happens, just
// without the extra context. Callers log any returned error for
// visibility.
func (b *BriefingRunner) Brief(ctx context.Context, in SessionBriefingInput) (map[string]*TaskBriefing, error) {
	if b == nil || b.Provider == nil {
		return map[string]*TaskBriefing{}, nil
	}
	if len(in.Tasks) == 0 {
		return map[string]*TaskBriefing{}, nil
	}
	model := b.Model
	if model == "" {
		model = "claude-sonnet-4-6"
	}

	// Build the prompt. Structure: role + decomposition context +
	// task list + current state + output schema.
	var pb strings.Builder
	pb.WriteString(leadDevSystemPrompt)
	pb.WriteString("\n\n")

	fmt.Fprintf(&pb, "SESSION: %s — %s\n\n", in.SessionID, in.SessionTitle)

	if len(in.AcceptanceCriteria) > 0 {
		pb.WriteString("SESSION ACCEPTANCE CRITERIA (what every task in this wave must help satisfy):\n")
		for _, ac := range in.AcceptanceCriteria {
			switch {
			case ac.Command != "":
				fmt.Fprintf(&pb, "  - [%s] %s — verified by: $ %s\n", ac.ID, ac.Description, ac.Command)
			case ac.FileExists != "":
				fmt.Fprintf(&pb, "  - [%s] %s — file must exist: %s\n", ac.ID, ac.Description, ac.FileExists)
			case ac.ContentMatch != nil && ac.ContentMatch.File != "":
				fmt.Fprintf(&pb, "  - [%s] %s — %s must contain: %s\n", ac.ID, ac.Description, ac.ContentMatch.File, ac.ContentMatch.Pattern)
			default:
				fmt.Fprintf(&pb, "  - [%s] %s\n", ac.ID, ac.Description)
			}
		}
		pb.WriteString("\n")
	}

	pb.WriteString("TASKS IN THIS WAVE (brief ONE of these per output entry):\n")
	for _, t := range in.Tasks {
		fmt.Fprintf(&pb, "\n  task %s: %s\n", t.ID, t.Description)
		if len(t.Files) > 0 {
			fmt.Fprintf(&pb, "    expected files: %s\n", strings.Join(t.Files, ", "))
		}
		if len(t.Dependencies) > 0 {
			fmt.Fprintf(&pb, "    depends on: %s\n", strings.Join(t.Dependencies, ", "))
		}
	}
	pb.WriteString("\n")

	if in.APISurface != "" {
		pb.WriteString("CURRENT CODEBASE API SURFACE (read-only view of what already exists):\n")
		// Bound to keep the prompt manageable — the full surface
		// can exceed 30k chars on a medium project.
		pb.WriteString(truncateForBrief(in.APISurface, 16000))
		pb.WriteString("\n\n")
	}

	if in.RepoMap != "" {
		pb.WriteString("RELEVANT REPO MAP (files most likely to matter for these tasks):\n")
		pb.WriteString(truncateForBrief(in.RepoMap, 4000))
		pb.WriteString("\n\n")
	}

	if in.RawSOW != "" {
		// Truncate hard — raw SOW can be huge and the briefing pass
		// only needs enough of it to cross-reference task names and
		// identifiers.
		pb.WriteString("ORIGINAL SPEC (verbatim, for exact-identifier cross-reference):\n")
		pb.WriteString(truncateForBrief(in.RawSOW, 12000))
		pb.WriteString("\n\n")
	}

	if in.SkillReferences != "" {
		pb.WriteString(in.SkillReferences)
		pb.WriteString("\n")
	}

	pb.WriteString(leadDevOutputInstructions)

	userContent, _ := json.Marshal([]map[string]interface{}{{"type": "text", "text": pb.String()}})
	resp, err := b.Provider.Chat(provider.ChatRequest{
		Model: model,
		// Briefings for a wave of 5-10 tasks fit comfortably in
		// 6000 output tokens. Extended-thinking budget is on top.
		MaxTokens: 8000,
		Messages:  []provider.ChatMessage{{Role: "user", Content: userContent}},
	})
	if err != nil {
		return map[string]*TaskBriefing{}, fmt.Errorf("briefing chat: %w", err)
	}
	raw, _ := collectModelText(resp)
	if strings.TrimSpace(raw) == "" {
		return map[string]*TaskBriefing{}, fmt.Errorf("briefing returned no content")
	}

	// The model emits a JSON object with a "briefings" array.
	var envelope struct {
		Briefings []*TaskBriefing `json:"briefings"`
	}
	if _, err := jsonutil.ExtractJSONInto(raw, &envelope); err != nil {
		return map[string]*TaskBriefing{}, fmt.Errorf("parse briefing: %w", err)
	}
	out := map[string]*TaskBriefing{}
	for _, tb := range envelope.Briefings {
		if tb == nil || tb.TaskID == "" {
			continue
		}
		out[tb.TaskID] = tb
	}
	return out, nil
}

// Format renders a briefing as a prompt block suitable for injecting
// into a worker's user message. Plain text, bounded so long briefings
// don't eat the cache budget.
func (tb *TaskBriefing) Format() string {
	if tb == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("LEAD-DEV BRIEFING (current codebase context for this task):\n")
	if tb.CurrentState != "" {
		b.WriteString("CURRENT STATE: ")
		b.WriteString(tb.CurrentState)
		b.WriteString("\n")
	}
	if len(tb.WhatIsMissing) > 0 {
		b.WriteString("MISSING (you must create or change these):\n")
		for _, m := range tb.WhatIsMissing {
			fmt.Fprintf(&b, "  - %s\n", m)
		}
	}
	if len(tb.Identifiers) > 0 {
		b.WriteString("EXISTING IDENTIFIERS (use these verbatim — do not invent alternatives):\n")
		for _, id := range tb.Identifiers {
			fmt.Fprintf(&b, "  - %s\n", id)
		}
	}
	if len(tb.Pitfalls) > 0 {
		b.WriteString("WATCH OUT FOR:\n")
		for _, p := range tb.Pitfalls {
			fmt.Fprintf(&b, "  - %s\n", p)
		}
	}
	if len(tb.SuggestedSteps) > 0 {
		b.WriteString("SUGGESTED STEP ORDER:\n")
		for i, s := range tb.SuggestedSteps {
			fmt.Fprintf(&b, "  %d. %s\n", i+1, s)
		}
	}
	if len(tb.RelevantSkills) > 0 {
		b.WriteString("FOLLOW THESE SKILLS (read the gotchas before starting):\n")
		for _, s := range tb.RelevantSkills {
			fmt.Fprintf(&b, "  - %s\n", s)
		}
	}
	b.WriteString("\n")
	return b.String()
}

const leadDevSystemPrompt = `You are the LEAD DEV / TECH LEAD on an autonomous build team. You have just read the project spec, the current acceptance criteria for this session, and the current state of the codebase. Your job: produce a BRIEFING for each task in this wave that tells the worker agent what the current codebase actually looks like and exactly what this task needs to do.

A good briefing:

  1. Describes the CURRENT state of the relevant files (what already exists, what's empty, what earlier tasks in this session wrote). Use the API surface and repo map above to ground this in reality — do NOT guess what exists.

  2. Lists WHAT IS MISSING — the concrete files, functions, or content this task needs to add or change. Be specific: not "add auth" but "add apps/web/app/login/page.tsx with a form that POSTs to /api/auth/login".

  3. Lists EXISTING IDENTIFIERS the task should use verbatim. If session-earlier task T3 exported 'ConcernField' from packages/types/src/index.ts, this task must import 'ConcernField' — not a made-up alternative. Pull these from the API surface above.

  4. Flags PITFALLS — things the worker will probably get wrong if not warned. Example: "earlier task created packages/types/package.json but forgot to add zod as a dependency; if your code imports zod, you must add it to that package.json before ending".

  5. Suggests a SHORT ORDERED STEP LIST for the worker. 3-6 steps max. Not a substitute for the worker's reasoning — a checklist for the "missed a step" failure mode.

Ground rules:

  - Briefings MUST be grounded in the current codebase context shown above, not in what the SOW spec assumed would be there. If the spec says "packages/types exists with ConcernField" but the API surface shows no such package, your briefing must flag that.

  - Be specific. "Use the correct import path" is not actionable. "Import { ConcernField } from '@sentinel/types'" is.

  - Do NOT restate the task description verbatim. The worker already has it. Briefings ADD context that the task description omits.

  - If a task is already trivially executable from the spec alone (no extra context needed), emit a minimal briefing that just says "CURRENT STATE: no relevant files exist yet; create them per the spec." — don't fabricate pitfalls.

  - Output ONE briefing per task in this wave. If a wave has 5 tasks, your output has 5 briefings.

  - If RELEVANT SKILLS are shown below, read them and include the most applicable skill names in each task's "relevant_skills" list. Workers will use these to look up conventions and gotchas. Only include skills that are genuinely relevant to that specific task — don't spray every skill onto every task.

`

const leadDevOutputInstructions = `Output ONLY a single JSON object in this schema — no prose, no backticks, no markdown fences:

{
  "briefings": [
    {
      "task_id": "T1",
      "current_state": "one-sentence description of what relevant files currently look like",
      "what_is_missing": ["concrete item 1", "concrete item 2"],
      "identifiers": ["ExistingIdent1", "ExistingIdent2"],
      "pitfalls": ["specific thing to avoid"],
      "suggested_steps": ["step 1", "step 2", "step 3"],
      "relevant_skills": ["pnpm-monorepo-discipline", "node-test-runner-triad"]
    }
  ]
}

Include one entry per task in the wave. Order doesn't matter; the runner matches on task_id.
`

// truncateForBrief is a shared helper for the briefing prompt builder.
// Middle-truncates long strings so both the beginning and end are
// visible.
func truncateForBrief(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	half := (max - 32) / 2
	if half < 100 {
		return s[:max]
	}
	return s[:half] + "\n... (truncated in middle) ...\n" + s[len(s)-half:]
}
