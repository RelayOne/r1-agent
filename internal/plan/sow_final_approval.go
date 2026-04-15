// Package plan — sow_final_approval.go
//
// Final plan approval — the "CTO signs off on the sprint plan" step.
// Runs AFTER chunked convert + semantic merger + coverage loop. A
// holistic fidelity+feasibility review against the original prose,
// not just a gap-check.
//
// Coverage review (sow_convert_chunked.go) asks: "did any prose
// deliverable get missed?" — necessary but not sufficient. A SOW
// can have every deliverable listed as a session and still fail to
// deliver the user's ask if the sessions don't compose correctly,
// if the DAG has the wrong serialization, or if ACs don't actually
// verify delivery.
//
// FinalPlanApproval asks a senior-engineering-reviewer LLM:
// "reading the original prose AND this merged SOW, would a real
// team shipping this plan produce what the user asked for?" and
// returns a structured {approve | request_changes | reject} verdict.
// Caller gates dispatch on approve; request_changes loops back
// into the refine path with the rationale; reject halts with
// operator-facing output.

package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ericmacdougall/stoke/internal/jsonutil"
	"github.com/ericmacdougall/stoke/internal/provider"
)

// FinalApprovalVerdict is the CTO-role verdict.
type FinalApprovalVerdict struct {
	// Decision: approve | request_changes | reject.
	Decision string `json:"decision"`
	// Reasoning walks through why this verdict.
	Reasoning string `json:"reasoning"`
	// Concerns are specific issues the reviewer wants addressed.
	// Always present on request_changes / reject; may be empty on
	// approve (concerns-without-blocking = optional improvements).
	Concerns []FinalApprovalConcern `json:"concerns"`
	// FidelityScore 0-100: does the plan DELIVER what the prose
	// asked for? Counts coverage + correctness-of-delivery.
	FidelityScore int `json:"fidelity_score"`
	// FeasibilityScore 0-100: can a real team execute this plan
	// and produce working software?
	FeasibilityScore int `json:"feasibility_score"`
}

// FinalApprovalConcern is one concrete issue the reviewer raises.
type FinalApprovalConcern struct {
	Severity    string `json:"severity"` // blocking | major | minor
	SessionID   string `json:"session_id,omitempty"`
	Description string `json:"description"`
	Fix         string `json:"fix"`
}

// HasBlocking reports whether any concern is blocking (gates
// dispatch).
func (v *FinalApprovalVerdict) HasBlocking() bool {
	for _, c := range v.Concerns {
		if c.Severity == "blocking" {
			return true
		}
	}
	return false
}

const finalApprovalPrompt = `You are a senior engineering reviewer (VP-eng / CTO role) giving final sign-off on a sprint plan before the team starts building. You have access to the ORIGINAL PROSE the user wrote AND the MERGED SOW that's been assembled from it. Your job: decide whether this plan, if executed exactly, will deliver what the user asked for.

Output ONLY a JSON object below. No prose, no markdown fences. Start with '{' and end with '}'.

{
  "decision": "approve" | "request_changes" | "reject",
  "reasoning": "2-4 sentence explanation of the decision",
  "concerns": [
    {
      "severity": "blocking" | "major" | "minor",
      "session_id": "SN (optional — omit for SOW-wide concerns)",
      "description": "what's wrong",
      "fix": "concrete suggestion"
    }
  ],
  "fidelity_score": 0-100,
  "feasibility_score": 0-100
}

DECISION RULES:
  - approve: every prose requirement is covered, session deps serialize correctly, ACs actually verify delivery. Minor concerns are OK — they're optional improvements, not blockers.
  - request_changes: one or more MAJOR concerns (e.g. a deliverable is only partially covered, a session's ACs don't actually verify what the session produces, cross-session dep gaps that will cause the DAG to race). Concerns list what needs to change.
  - reject: BLOCKING concern present (e.g. a core deliverable from the prose is completely missing, the plan is structurally incoherent). Concerns explain why this SOW is unfit for execution.

FIDELITY audit — is the prose's ASK DELIVERED by the plan?
  - Does every deliverable in the prose map to one or more sessions that produce it?
  - Are the session ACs specific enough that "all ACs pass" = "user gets what they asked for"? Generic build-pass ACs on a session that was supposed to deliver a feature is a major concern.
  - Are UI surfaces (pages, screens, routes) scaffolded with per-route/per-screen tasks, or lumped into a single "build the app" task?
  - Are API endpoints, data models, auth flows, and integrations each traceable to concrete tasks?

FEASIBILITY audit — can the team EXECUTE this plan?
  - Is each session's scope bounded (≤ 30 tasks) and coherent (one deliverable, one package boundary)?
  - Do cross-session inputs/outputs form a DAG that serializes correctly? A session that needs a type from packages/types must run AFTER packages/types is built.
  - Are the acceptance criteria achievable in the current runtime (no browser E2E on Linux, no long-running servers, no unset env vars)?
  - Are the task descriptions specific enough that a worker agent can execute them without creative leaps?

Output the JSON verdict now.

ORIGINAL PROSE:
`

// FinalPlanApproval issues the CTO-role review. Returns a verdict
// with score breakdown + concerns; caller decides based on
// verdict.Decision. Best-effort — a transport error returns a
// conservative approve with empty concerns so the run can still
// proceed (the review is advisory by design; halting the run on
// network glitch would be worse than proceeding with the already-
// reviewed merged SOW).
func FinalPlanApproval(ctx context.Context, prose string, sow *SOW, prov provider.Provider, model string) (*FinalApprovalVerdict, error) {
	if sow == nil {
		return nil, fmt.Errorf("nil SOW")
	}
	if prov == nil {
		return nil, fmt.Errorf("no provider")
	}
	sowBlob, err := json.MarshalIndent(sow, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal sow: %w", err)
	}
	userText := finalApprovalPrompt + prose + "\n\nMERGED SOW:\n" + string(sowBlob)
	userContent, _ := json.Marshal([]map[string]interface{}{{"type": "text", "text": userText}})

	revCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	resp, err := callChatWithCtx(revCtx, prov, provider.ChatRequest{
		Model:     model,
		MaxTokens: 32000,
		Messages:  []provider.ChatMessage{{Role: "user", Content: userContent}},
	})
	if err != nil {
		return nil, err
	}
	raw, _ := collectModelText(resp)
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("final approval empty (stop_reason=%q)", resp.StopReason)
	}
	var verdict FinalApprovalVerdict
	if _, err := jsonutil.ExtractJSONInto(raw, &verdict); err != nil {
		return nil, fmt.Errorf("parse final approval: %w", err)
	}
	if verdict.Decision == "" {
		return nil, fmt.Errorf("final approval missing decision field")
	}
	return &verdict, nil
}

// FormatApprovalVerdict renders the verdict for operator display.
// Decision + scores + up to 5 concerns. Longer-form reasoning is
// captured in the returned struct for programmatic use.
func FormatApprovalVerdict(v *FinalApprovalVerdict) string {
	if v == nil {
		return "(no approval verdict)"
	}
	var b strings.Builder
	icon := "✔"
	switch v.Decision {
	case "request_changes":
		icon = "⚠"
	case "reject":
		icon = "⛔"
	}
	fmt.Fprintf(&b, "  %s final plan approval: %s (fidelity=%d feasibility=%d)\n",
		icon, v.Decision, v.FidelityScore, v.FeasibilityScore)
	if strings.TrimSpace(v.Reasoning) != "" {
		fmt.Fprintf(&b, "     reasoning: %s\n", v.Reasoning)
	}
	max := 5
	if len(v.Concerns) < max {
		max = len(v.Concerns)
	}
	for i := 0; i < max; i++ {
		c := v.Concerns[i]
		loc := ""
		if c.SessionID != "" {
			loc = "[" + c.SessionID + "] "
		}
		fmt.Fprintf(&b, "     %s %s%s\n", c.Severity, loc, c.Description)
		if strings.TrimSpace(c.Fix) != "" {
			fmt.Fprintf(&b, "           fix: %s\n", c.Fix)
		}
	}
	if len(v.Concerns) > max {
		fmt.Fprintf(&b, "     ... and %d more\n", len(v.Concerns)-max)
	}
	return b.String()
}
