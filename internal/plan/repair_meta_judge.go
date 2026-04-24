package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ericmacdougall/stoke/internal/jsonutil"
	"github.com/ericmacdougall/stoke/internal/provider"
)

// RepairStuckDiagnosis is the meta-judge's verdict on why a session's
// repair loop is plateauing. Produced by RunRepairMetaJudge between
// repair attempts when the trail shows stagnation.
type RepairStuckDiagnosis struct {
	// StuckKind classifies the plateau. One of:
	//   "wrong-root-cause"   — attempts fixing wrong layer
	//   "multi-bug"          — one attempt closes bug A but opens B
	//   "scope-too-broad"    — session needs to be split
	//   "genuine-hard"       — problem IS hard, let the next attempt try
	//   "done-enough"        — ACs won't close further; escalate cleanly
	StuckKind string `json:"stuck_kind"`
	// Reasoning is one paragraph citing specific file/AC evidence.
	Reasoning string `json:"reasoning"`
	// RecommendedDirective is the next concrete step. Always
	// populated, even for "done-enough" (in which case it reads
	// "escalate with a clear report rather than another attempt").
	RecommendedDirective string `json:"recommended_directive"`
	// Decompose is true when the recommended next move is NOT a
	// single directive but a split into sub-directives. When true,
	// caller should run DecomposeTaskGap on the trail's stuck gap.
	Decompose bool `json:"decompose"`
}

// repairMetaAnalystOutput is the shape each of the 4 analysts emits.
// Kept small so parse failures are rare.
type repairMetaAnalystOutput struct {
	Vote      string `json:"vote"`
	Reasoning string `json:"reasoning"`
}

// RunRepairMetaJudge runs a 4-analyst + 1-judge consensus pattern
// scoped to a single session's repair trail. Inputs: the trail,
// current failing ACs, and current codebase excerpts for the failing
// files. Output: a single verdict after analyst synthesis.
//
// Called between repair attempts (1→2 and 2→3) when the trail has
// ≥1 record with NetProgress ≤ 0. Returns nil when prov is nil
// or trail is empty.
func RunRepairMetaJudge(ctx context.Context, prov provider.Provider, model string, trail *RepairTrail, failingACs []AcceptanceCriterion, codeExcerpts map[string]string) (*RepairStuckDiagnosis, error) {
	if prov == nil || trail == nil || len(trail.Records) == 0 {
		return nil, nil
	}
	if model == "" {
		model = "claude-sonnet-4-6"
	}

	shared := buildRepairMetaSharedCtx(trail, failingACs, codeExcerpts)

	votes := map[string]repairMetaAnalystOutput{}

	wrong, err := runRepairMetaAnalyst(ctx, prov, model, repairMetaWrongLayerPrompt, shared, "wrong-layer")
	if err != nil {
		return nil, fmt.Errorf("repair-meta wrong-layer analyst: %w", err)
	}
	votes["wrong_layer"] = wrong

	multi, err := runRepairMetaAnalyst(ctx, prov, model, repairMetaMultiBugPrompt, shared, "multi-bug")
	if err != nil {
		return nil, fmt.Errorf("repair-meta multi-bug analyst: %w", err)
	}
	votes["multi_bug"] = multi

	scope, err := runRepairMetaAnalyst(ctx, prov, model, repairMetaScopeCreepPrompt, shared, "scope-creep")
	if err != nil {
		return nil, fmt.Errorf("repair-meta scope-creep analyst: %w", err)
	}
	votes["scope_creep"] = scope

	done, err := runRepairMetaAnalyst(ctx, prov, model, repairMetaDoneEnoughPrompt, shared, "done-enough")
	if err != nil {
		return nil, fmt.Errorf("repair-meta done-enough analyst: %w", err)
	}
	votes["done_enough"] = done

	diag, err := synthesizeRepairMetaDiagnosis(ctx, prov, model, shared, votes)
	if err != nil {
		return nil, fmt.Errorf("repair-meta synthesis: %w", err)
	}
	return diag, nil
}

func buildRepairMetaSharedCtx(trail *RepairTrail, failingACs []AcceptanceCriterion, codeExcerpts map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "SESSION: %s\n\n", trail.SessionID)
	b.WriteString("REPAIR TRAIL (most recent attempt last):\n")
	for _, rec := range trail.Records {
		fmt.Fprintf(&b, "  attempt %d: directive=%q\n", rec.Attempt, truncateForReasoning(rec.Directive, 400))
		if len(rec.FilesTouched) > 0 {
			fmt.Fprintf(&b, "    files_touched: %s\n", strings.Join(rec.FilesTouched, ", "))
		}
		if strings.TrimSpace(rec.DiffSummary) != "" {
			fmt.Fprintf(&b, "    diff_summary: %s\n", truncateForReasoning(rec.DiffSummary, 400))
		}
		fmt.Fprintf(&b, "    acs_failing_before: [%s]\n", strings.Join(rec.ACsFailingBefore, ", "))
		fmt.Fprintf(&b, "    acs_failing_after:  [%s]\n", strings.Join(rec.ACsFailingAfter, ", "))
		fmt.Fprintf(&b, "    net_progress: %d\n", rec.NetProgress)
	}
	b.WriteString("\nCURRENTLY FAILING ACCEPTANCE CRITERIA:\n")
	for _, ac := range failingACs {
		fmt.Fprintf(&b, "  - %s: %s\n", ac.ID, ac.Description)
		if ac.Command != "" {
			fmt.Fprintf(&b, "      command: %s\n", ac.Command)
		}
	}
	if len(codeExcerpts) > 0 {
		b.WriteString("\nCODE EXCERPTS (files the failing ACs probably touch):\n")
		for _, p := range sortedKeys(codeExcerpts) {
			fmt.Fprintf(&b, "\n--- %s ---\n", p)
			b.WriteString(truncateForReasoning(codeExcerpts[p], 2500))
			b.WriteString("\n")
		}
	}
	return b.String()
}

const repairMetaWrongLayerPrompt = `You are a software architect reviewing a stuck repair loop. Your ONLY job: decide whether the prior attempts have all been fixing the WRONG LAYER of the stack (e.g. touching barrel exports when the real problem is interface file contents; touching types when the real problem is a tsconfig reference; editing a script when the real problem is missing dep).

Look at: which files each attempt touched, whether net_progress was ≤ 0, and what the failing AC's command is actually testing.

Output JSON only, one object, no backticks:

{"vote": "wrong-layer" | "not-wrong-layer", "reasoning": "one paragraph citing specific files and ACs"}
`

const repairMetaMultiBugPrompt = `You are a debugger reviewing a stuck repair loop. Your ONLY job: decide whether the trail shows MULTI-BUG SYMPTOMS — i.e. an attempt closes one AC but a different AC opens, or the acs_failing_before/after sets shuffle without net progress.

Look at the acs_failing_before and acs_failing_after fields across attempts. If the set of failing ACs keeps rotating rather than shrinking, that's multi-bug.

Output JSON only, one object, no backticks:

{"vote": "multi-bug" | "not-multi-bug", "reasoning": "one paragraph citing the AC rotation pattern"}
`

const repairMetaScopeCreepPrompt = `You are a project supervisor reviewing a stuck repair loop. Your ONLY job: decide whether the session's scope is TOO BROAD for one repair worker — i.e. the failing ACs cover multiple distinct features/files that really should be separate sessions or sub-directives.

Output JSON only, one object, no backticks:

{"vote": "scope-too-broad" | "scope-ok", "reasoning": "one paragraph citing which ACs belong together and which don't"}
`

const repairMetaDoneEnoughPrompt = `You are a pragmatic engineer reviewing a stuck repair loop. Your ONLY job: decide whether the remaining ACs are realistically CLOSABLE at all, or whether the repair loop should escalate cleanly rather than burn another attempt.

An AC is "done-enough" when: the code genuinely implements the spec but the AC is measuring something unachievable (dev server startup, network), OR the gap is so tangential that further attempts will not converge.

Output JSON only, one object, no backticks:

{"vote": "done-enough" | "keep-trying", "reasoning": "one paragraph citing specific ACs"}
`

const repairMetaJudgePrompt = `You are a senior engineering supervisor. Four analysts have reviewed a stuck repair loop. Synthesize their votes and reasoning into ONE final diagnosis.

The five possible stuck_kind values:
  "wrong-root-cause" — attempts are fixing the wrong layer of the stack (use when wrong-layer analyst votes "wrong-layer")
  "multi-bug"        — attempts close one AC but open another (use when multi-bug analyst votes "multi-bug")
  "scope-too-broad"  — session needs to be split into sub-directives (use when scope-creep votes "scope-too-broad"). When you pick this, set decompose=true.
  "genuine-hard"     — problem is just hard, let the next attempt try a different angle
  "done-enough"      — remaining ACs can't realistically be closed; escalate cleanly

Priority when analysts disagree: wrong-root-cause beats multi-bug beats scope-too-broad beats done-enough beats genuine-hard. Pick the first one that clearly applies.

Set decompose=true if-and-only-if the best next move is splitting the remaining work into narrower sub-directives (almost always paired with stuck_kind="scope-too-broad"; may also pair with "multi-bug" when sub-problems are independent).

Always populate recommended_directive with one concrete next step, even when stuck_kind is "done-enough" (in which case it reads "escalate with a clear report rather than another attempt").

Output ONLY a single JSON object, no backticks, no prose:

{
  "stuck_kind": "wrong-root-cause|multi-bug|scope-too-broad|genuine-hard|done-enough",
  "reasoning": "one paragraph synthesis citing which analyst said what and why you picked this stuck_kind",
  "recommended_directive": "one concrete next step",
  "decompose": true|false
}
`

func runRepairMetaAnalyst(ctx context.Context, prov provider.Provider, model, systemPrompt, shared, label string) (repairMetaAnalystOutput, error) {
	full := systemPrompt + "\n\nCONTEXT:\n" + shared
	userContent, _ := json.Marshal([]map[string]interface{}{{"type": "text", "text": full}})
	resp, err := prov.Chat(provider.ChatRequest{
		Model:     model,
		MaxTokens: 4000,
		Messages:  []provider.ChatMessage{{Role: "user", Content: userContent}},
	})
	if err != nil {
		return repairMetaAnalystOutput{}, fmt.Errorf("analyst %s chat: %w", label, err)
	}
	raw, diag := collectModelText(resp)
	if strings.TrimSpace(raw) == "" {
		return repairMetaAnalystOutput{}, fmt.Errorf("analyst %s empty (stop_reason=%q)\n%s", label, resp.StopReason, diag)
	}
	// Fall back to raw text in reasoning so the judge can still
	// use it. Parse errors are a normal outcome for this analyst
	// path — the analyst may answer in prose rather than JSON,
	// and the judge handles "parse-error" votes explicitly.
	return extractRepairMetaAnalyst(raw), nil
}

// extractRepairMetaAnalyst parses the analyst's raw response into
// a repairMetaAnalystOutput. On JSON parse failure it returns a
// "parse-error" sentinel vote with the raw text as reasoning so
// the judge can still reason over the content. This helper keeps
// the parse-error fallback inside a function whose signature
// promises no error, which is the actual contract here.
func extractRepairMetaAnalyst(raw string) repairMetaAnalystOutput {
	var out repairMetaAnalystOutput
	if _, err := jsonutil.ExtractJSONInto(raw, &out); err != nil {
		return repairMetaAnalystOutput{Vote: "parse-error", Reasoning: raw}
	}
	return out
}

func synthesizeRepairMetaDiagnosis(ctx context.Context, prov provider.Provider, model, shared string, votes map[string]repairMetaAnalystOutput) (*RepairStuckDiagnosis, error) {
	var b strings.Builder
	b.WriteString(repairMetaJudgePrompt)
	b.WriteString("\nSHARED CONTEXT:\n")
	b.WriteString(shared)
	b.WriteString("\n\nANALYST VOTES:\n")
	for _, key := range []string{"wrong_layer", "multi_bug", "scope_creep", "done_enough"} {
		v := votes[key]
		fmt.Fprintf(&b, "\n=== %s ===\nvote: %s\nreasoning: %s\n", key, v.Vote, v.Reasoning)
	}
	b.WriteString("\nNow output the JSON diagnosis.")

	userContent, _ := json.Marshal([]map[string]interface{}{{"type": "text", "text": b.String()}})
	resp, err := prov.Chat(provider.ChatRequest{
		Model:     model,
		MaxTokens: 6000,
		Messages:  []provider.ChatMessage{{Role: "user", Content: userContent}},
	})
	if err != nil {
		return nil, fmt.Errorf("judge chat: %w", err)
	}
	raw, diag := collectModelText(resp)
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("judge empty (stop_reason=%q)\n%s", resp.StopReason, diag)
	}
	var d RepairStuckDiagnosis
	if _, err := jsonutil.ExtractJSONInto(raw, &d); err != nil {
		return nil, fmt.Errorf("parse judge verdict: %w", err)
	}
	d.StuckKind = strings.ToLower(strings.TrimSpace(d.StuckKind))
	switch d.StuckKind {
	case "wrong-root-cause", "multi-bug", "scope-too-broad", "genuine-hard", "done-enough":
	default:
		return nil, fmt.Errorf("judge verdict has unknown stuck_kind %q", d.StuckKind)
	}
	return &d, nil
}
