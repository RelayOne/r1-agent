// Package plan — session_sizer.go
//
// The session sizer is an agentic pre-dispatch judgment pass that
// looks at a session's task list BEFORE any workers are spawned
// and decides whether the session is too broad to converge in a
// single integration pass. When a human or an LLM refiner wrote
// a session like "Shared Packages Implementation" covering four
// disjoint packages with 13 tasks and 50+ files, the downstream
// integration reviewer will time out, ACs will plateau, and
// repair attempts will cycle without making progress.
//
// The sizer's job is strictly scope triage: it does NOT plan,
// dispatch, or execute. It looks at the session's shape — task
// count, file spread, whether tasks cluster along natural
// boundaries — and either:
//
//  1. Declines to split (cohesion beats raw size); OR
//  2. Proposes a decomposition into sub-sessions, each tight
//     enough to converge independently, dispatched in order.
//
// The proposed split is then materialized into concrete Session
// values via ApplySessionSplit and substituted into the SOW's
// session list before the scheduler iterates it. Every task
// from the parent session appears in exactly one sub-session —
// no drops, no duplicates. Parent ACs are distributed to the
// sub-sessions whose tasks they apply to, with unallocated ACs
// carried by the LAST sub-session so they still gate the final
// state.
//
// Noop paths: nil provider OR fewer than 6 tasks in the session.
// Splitting a small session isn't worth the LLM call, and running
// without a reasoning provider means we fall through to the
// original (unsplit) dispatch behavior.
package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/RelayOne/r1/internal/jsonutil"
	"github.com/RelayOne/r1/internal/provider"
)

// sessionSizerTaskFloor is the minimum task count below which the
// sizer short-circuits without consulting the LLM. Sessions this
// small almost never benefit from splitting.
const sessionSizerTaskFloor = 6

// SessionSplit is a proposed decomposition of an oversized session
// into smaller sub-sessions. Produced by JudgeSessionSize when the
// judge determines a split is warranted.
type SessionSplit struct {
	// Reasoning is the judge's one-paragraph explanation for why
	// the split is worth doing (task count, file spread, scope
	// diversity). Always populated, even when ShouldSplit is false.
	Reasoning string `json:"reasoning"`

	// ShouldSplit is true when the judge recommends splitting.
	// When false, the caller runs the session as originally defined.
	ShouldSplit bool `json:"should_split"`

	// SubSessions is the list of proposed sub-sessions. Each gets
	// a unique suffix on the original session ID (e.g. S2-types,
	// S2-api-client). Ordered by suggested dispatch sequence —
	// caller should respect the order because dependencies flow
	// between them.
	SubSessions []SessionSplitPart `json:"sub_sessions"`
}

// SessionSplitPart is one sub-session within a SessionSplit.
type SessionSplitPart struct {
	// SuffixID is appended to the parent session ID (e.g. "types"
	// → "S2-types"). Short, kebab-case, derived from the natural
	// boundary (package name, concern, file cluster).
	SuffixID string `json:"suffix_id"`

	// Title is a human-readable title for this sub-session.
	Title string `json:"title"`

	// TaskIDs is the list of original task IDs from the parent
	// session that belong to this sub-session. Every task in the
	// parent session must appear in exactly one sub-session —
	// the judge must not drop tasks or duplicate them.
	TaskIDs []string `json:"task_ids"`

	// AcceptanceCriteriaIDs is the subset of the parent's ACs
	// that should gate this sub-session. Parent ACs that don't
	// apply to any sub-session's task set are carried by the
	// LAST sub-session so they still gate the final state.
	AcceptanceCriteriaIDs []string `json:"acceptance_criteria_ids"`
}

// SessionSizerInput bundles what the judge needs.
type SessionSizerInput struct {
	// Session is the session being judged.
	Session Session

	// SOWSpec is the relevant spec excerpt covering this session.
	SOWSpec string

	// TotalExpectedFiles is the sum of all Task.Files entries in
	// the session — used as a rough size signal.
	TotalExpectedFiles int

	// UniversalPromptBlock carries the universal coding-standards +
	// known-gotchas block. When non-empty it is appended to the
	// sizer's system prompt.
	UniversalPromptBlock string
}

// JudgeSessionSize consults an LLM to decide whether a session
// should be split into sub-sessions before dispatch. Returns a
// SessionSplit indicating the decision and (when ShouldSplit is
// true) the proposed sub-session layout.
//
// Heuristics the judge is prompted with:
//   - Tasks ≥ 10 OR expected files ≥ 40 is a strong split signal
//   - Multiple unrelated package boundaries in one session is a
//     split signal (e.g. "Shared Packages" spanning types/
//     api-client/utils/i18n — each is its own natural boundary)
//   - A session that's already bounded to ONE package/feature
//     does NOT split even if file count is high — cohesion beats
//     size
//
// Returns nil + nil when prov is nil (noop) OR when the session
// has fewer than 6 tasks (below the split-threshold floor; not
// worth the LLM call).
func JudgeSessionSize(ctx context.Context, prov provider.Provider, model string, in SessionSizerInput) (*SessionSplit, error) {
	if prov == nil {
		return nil, nil
	}
	if len(in.Session.Tasks) < sessionSizerTaskFloor {
		return nil, nil
	}
	if model == "" {
		model = "claude-sonnet-4-6"
	}

	prompt := buildSessionSizerPrompt(in)
	userContent, _ := json.Marshal([]map[string]interface{}{{"type": "text", "text": prompt}})
	sysPrompt := sessionSizerSystemPrompt
	if strings.TrimSpace(in.UniversalPromptBlock) != "" {
		sysPrompt += "\n\n" + in.UniversalPromptBlock
	}
	resp, err := prov.Chat(provider.ChatRequest{
		Model:     model,
		System:    sysPrompt,
		MaxTokens: 6000,
		Messages:  []provider.ChatMessage{{Role: "user", Content: userContent}},
	})
	if err != nil {
		return nil, fmt.Errorf("session sizer chat: %w", err)
	}
	raw, _ := collectModelText(resp)
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("session sizer returned no content")
	}

	var split SessionSplit
	if _, err := jsonutil.ExtractJSONInto(raw, &split); err != nil {
		return nil, fmt.Errorf("parse session sizer verdict: %w", err)
	}
	// Validate: if the judge said ShouldSplit but produced <2
	// sub-sessions, treat the verdict as "don't split" — a single
	// sub-session is a no-op wrapped in ceremony.
	if split.ShouldSplit && len(split.SubSessions) < 2 {
		split.ShouldSplit = false
	}
	return &split, nil
}

// ApplySessionSplit materializes a SessionSplit into a slice of
// concrete Session values the caller can dispatch in order.
// Each sub-session's Tasks and AcceptanceCriteria are filtered
// from the parent per the SplitPart specification. Returns an
// error if the split is malformed (tasks missing, duplicates,
// invalid references).
func ApplySessionSplit(parent Session, split SessionSplit) ([]Session, error) {
	if !split.ShouldSplit || len(split.SubSessions) == 0 {
		return nil, fmt.Errorf("ApplySessionSplit: split not actionable (ShouldSplit=%v parts=%d)", split.ShouldSplit, len(split.SubSessions))
	}

	// Index parent tasks and ACs by ID for fast lookup.
	taskByID := make(map[string]Task, len(parent.Tasks))
	for _, t := range parent.Tasks {
		taskByID[t.ID] = t
	}
	acByID := make(map[string]AcceptanceCriterion, len(parent.AcceptanceCriteria))
	for _, ac := range parent.AcceptanceCriteria {
		acByID[ac.ID] = ac
	}

	// Track assignments for exactly-once semantics.
	taskAssigned := make(map[string]int, len(parent.Tasks)) // taskID -> subIdx
	acAssigned := make(map[string]int, len(parent.AcceptanceCriteria))
	seenSuffix := make(map[string]bool, len(split.SubSessions))

	out := make([]Session, 0, len(split.SubSessions))
	for i, part := range split.SubSessions {
		suffix := strings.TrimSpace(part.SuffixID)
		if suffix == "" {
			return nil, fmt.Errorf("ApplySessionSplit: sub-session[%d] has empty suffix_id", i)
		}
		if seenSuffix[suffix] {
			return nil, fmt.Errorf("ApplySessionSplit: duplicate suffix_id %q", suffix)
		}
		seenSuffix[suffix] = true

		sub := Session{
			ID:          parent.ID + "-" + suffix,
			Phase:       parent.Phase,
			PhaseNumber: parent.PhaseNumber,
			Title:       strings.TrimSpace(part.Title),
			Description: parent.Description,
			Inputs:      parent.Inputs,
			Outputs:     parent.Outputs,
			InfraNeeded: parent.InfraNeeded,
		}
		if sub.Title == "" {
			sub.Title = parent.Title + " — " + suffix
		}

		for _, tid := range part.TaskIDs {
			t, ok := taskByID[tid]
			if !ok {
				return nil, fmt.Errorf("ApplySessionSplit: sub-session %q references unknown task %q", suffix, tid)
			}
			if prev, dup := taskAssigned[tid]; dup {
				return nil, fmt.Errorf("ApplySessionSplit: task %q duplicated across sub-sessions (first in[%d], again in[%d])", tid, prev, i)
			}
			taskAssigned[tid] = i
			sub.Tasks = append(sub.Tasks, t)
		}

		for _, aid := range part.AcceptanceCriteriaIDs {
			ac, ok := acByID[aid]
			if !ok {
				return nil, fmt.Errorf("ApplySessionSplit: sub-session %q references unknown AC %q", suffix, aid)
			}
			if prev, dup := acAssigned[aid]; dup {
				// ACs are allowed to appear in multiple sub-sessions
				// only if the judge explicitly re-gates them; we take
				// the first assignment and ignore subsequent ones
				// rather than erroring, since duplicating a command
				// AC is cheap.
				_ = prev
				continue
			}
			acAssigned[aid] = i
			sub.AcceptanceCriteria = append(sub.AcceptanceCriteria, ac)
		}

		out = append(out, sub)
	}

	// Every parent task must be assigned somewhere.
	var missing []string
	for _, t := range parent.Tasks {
		if _, ok := taskAssigned[t.ID]; !ok {
			missing = append(missing, t.ID)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("ApplySessionSplit: tasks missing from split: %s", strings.Join(missing, ", "))
	}

	// Carry unallocated ACs on the LAST sub-session so they still
	// gate the final state.
	last := &out[len(out)-1]
	for _, ac := range parent.AcceptanceCriteria {
		if _, ok := acAssigned[ac.ID]; !ok {
			last.AcceptanceCriteria = append(last.AcceptanceCriteria, ac)
			acAssigned[ac.ID] = len(out) - 1
		}
	}

	return out, nil
}

// buildSessionSizerPrompt renders the user-message half of the
// sizer judge request. System prompt is constant; everything the
// judge sees about THIS session goes here.
func buildSessionSizerPrompt(in SessionSizerInput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "SESSION %s — %s\n", in.Session.ID, in.Session.Title)
	if strings.TrimSpace(in.Session.Description) != "" {
		fmt.Fprintf(&b, "  description: %s\n", in.Session.Description)
	}
	fmt.Fprintf(&b, "  tasks: %d\n", len(in.Session.Tasks))
	fmt.Fprintf(&b, "  total expected files across tasks: %d\n", in.TotalExpectedFiles)
	b.WriteString("\n")

	b.WriteString("TASK LIST:\n")
	for _, t := range in.Session.Tasks {
		fmt.Fprintf(&b, "  - %s: %s\n", t.ID, t.Description)
		if len(t.Files) > 0 {
			fmt.Fprintf(&b, "      files: %s\n", strings.Join(t.Files, ", "))
		}
	}
	b.WriteString("\n")

	if len(in.Session.AcceptanceCriteria) > 0 {
		b.WriteString("ACCEPTANCE CRITERIA:\n")
		for _, ac := range in.Session.AcceptanceCriteria {
			fmt.Fprintf(&b, "  - [%s] %s\n", ac.ID, ac.Description)
		}
		b.WriteString("\n")
	}

	if strings.TrimSpace(in.SOWSpec) != "" {
		b.WriteString("SOW SPEC EXCERPT:\n")
		spec := in.SOWSpec
		if len(spec) > 5000 {
			spec = spec[:5000] + "\n...[truncated]"
		}
		b.WriteString(spec)
		b.WriteString("\n\n")
	}

	b.WriteString("Decide whether to split. Output ONLY the JSON verdict described in the system prompt.\n")
	return b.String()
}

// sessionSizerSystemPrompt drives the sizer judge. It's written
// for an LLM that will see ONE session at a time and must return
// a machine-parseable verdict.
const sessionSizerSystemPrompt = `You are a tech lead looking at a session's task list and deciding if it's too broad to converge in one pass. Split only when you see MULTIPLE disjoint concerns that could each be built and verified independently — not when tasks share a concern even if there are many of them.

HEURISTICS:
  - Tasks >= 10 OR total expected files >= 40 is a strong split signal — but cohesion still wins. A 12-task session all inside one package's src/ should stay whole.
  - Multiple unrelated package boundaries in one session is the strongest split signal. "Shared Packages" spanning packages/types, packages/api-client, packages/utils, packages/i18n: four disjoint boundaries, split into four sub-sessions.
  - A session bounded to ONE package, ONE feature, or ONE end-to-end flow does NOT split even if file count is high — you cannot verify half a flow.
  - When splitting, make sub-sessions dispatch-ordered: if B depends on A, list A first. Dependencies flow forward in the list.
  - Every task in the parent session appears in EXACTLY ONE sub-session — no drops, no duplicates.
  - Acceptance criteria go to the sub-session whose tasks they gate. If an AC covers the whole session (e.g. "monorepo builds"), assign it to the LAST sub-session so it gates the final state.

DO NOT SPLIT:
  - Sessions with <10 tasks and <40 files unless the concerns are clearly disjoint
  - Sessions where the tasks form a single pipeline (parse -> validate -> emit)
  - Sessions whose ACs are all whole-session gates
  - Sessions that are already tight to one package/feature

SuffixID rules: short, kebab-case, matches the natural boundary. Package names, feature names, or layer names. Not "part-1".

OUTPUT: a single JSON object matching this shape — no prose outside the JSON, no markdown fences:

{
  "reasoning": "one paragraph citing task count, file spread, and whether disjoint concerns are present",
  "should_split": true,
  "sub_sessions": [
    {"suffix_id": "types", "title": "Shared Types Package", "task_ids": ["t1", "t2"], "acceptance_criteria_ids": ["ac1"]},
    {"suffix_id": "api-client", "title": "API Client Package", "task_ids": ["t3", "t4", "t5"], "acceptance_criteria_ids": ["ac2"]}
  ]
}

When declining to split, set should_split=false and sub_sessions=[]; still populate reasoning.`
