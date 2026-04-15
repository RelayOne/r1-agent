// Package plan — sow_refine_concerns.go
//
// Structured refinement pass driven by the CTO reviewer's concerns.
// Closes the previous gap where a request_changes verdict only
// printed concerns and proceeded with the unmodified SOW.
//
// Input:  the current SOW + the list of FinalApprovalConcern items
//         from the agentic reviewer.
// Output: a refined SOW that addresses each concern. Tasks may
//         move between sessions, sessions may be split or merged,
//         and per-concern fix instructions are applied verbatim
//         where the model can interpret them.
//
// Strategy: one focused LLM call. The reviewer already articulated
// what's wrong AND what to do about it (Concern.Fix); the refiner
// is mostly a transcription job — apply each fix to the SOW JSON
// without inventing new scope.
//
// The result re-enters the CTO approval loop in
// ConvertProseToSOWChunked so the verdict can confirm the fixes
// landed (and surface any side effects the refiner introduced).
// Two rounds is the cap by default.

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

const refineFromConcernsPrompt = `You are revising a Statement of Work to address specific concerns from a CTO-level review. Apply each concern's fix to the SOW WITHOUT changing scope, deleting deliverables, or inventing new requirements.

Output ONLY the revised SOW as a JSON object. No prose, no markdown fences. Start with '{' and end with '}'. The shape MUST match the input SOW exactly — same top-level keys (id, name, description, stack, sessions), same Session shape (id, phase, title, description, tasks, acceptance_criteria, inputs, outputs, etc.).

REVISION RULES:
1. Read each concern's "fix" field. That IS the directive — apply it as written. Do not water it down.
2. When a concern says "Decide which session owns X", pick the session the fix names; remove duplicate ownership from the other session(s) and add the artifact to the loser's Inputs so the DAG serializes the work.
3. When a concern names a duplicate file path across sessions, keep the file in exactly one session's tasks.files; remove it from the others. Update those other sessions' Inputs to consume the relevant Output instead.
4. When a concern flags a malformed session ID, replace it with a clean canonical id matching ^S\d+(-[a-z0-9-]+)?$.
5. When a concern flags a missing Input producer, either (a) rename the Input to match an existing Output exactly, or (b) add a new Output to the producer session named to match. Prefer (a).
6. When a concern flags an inverted Input/Output relationship, swap them so production flows downstream.
7. When a concern flags an unbounded session (>30 tasks), split into two sessions with clean Inputs/Outputs handoff.
8. Preserve EVERY existing task. If you split a session, distribute its tasks across the new sessions; do not drop any.
9. Preserve EVERY acceptance criterion. Reassign to the session that now owns its scope.
10. Renumber session IDs only when necessary (split / merge / malformed). Keep stable IDs otherwise.

OUTPUT REQUIREMENTS:
- Same top-level shape as input.
- Every original task ID appears in the output exactly once.
- Every original AC ID appears in the output exactly once.
- Session IDs match ^S\d+(-[a-z0-9-]+)?$ — no prose, no parentheticals, no whitespace.

INPUT SOW:
`

// RefineSOWFromConcerns applies the reviewer's concerns to the SOW
// and returns the revised SOW. On any failure (parse, validation,
// round-trip) it returns a non-nil error and the caller should
// proceed with the unmodified SOW.
func RefineSOWFromConcerns(ctx context.Context, prose string, sow *SOW, concerns []FinalApprovalConcern, prov provider.Provider, model string) (*SOW, error) {
	if sow == nil {
		return nil, fmt.Errorf("nil SOW")
	}
	if len(concerns) == 0 {
		return sow, nil
	}
	if prov == nil {
		return nil, fmt.Errorf("no provider")
	}

	sowBlob, err := json.MarshalIndent(sow, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal sow: %w", err)
	}

	// Concerns block: numbered, each with severity + scope + the
	// reviewer's fix directive.
	var concernsBuf strings.Builder
	for i, c := range concerns {
		loc := ""
		if c.SessionID != "" {
			loc = fmt.Sprintf(" [%s]", c.SessionID)
		}
		fmt.Fprintf(&concernsBuf, "%d. (%s)%s %s\n   FIX: %s\n",
			i+1, c.Severity, loc, c.Description, c.Fix)
	}

	userText := refineFromConcernsPrompt + string(sowBlob) +
		"\n\nCONCERNS TO ADDRESS:\n" + concernsBuf.String() +
		"\n\nPROSE (for reference; do not rewrite scope):\n" + prose
	userContent, _ := json.Marshal([]map[string]interface{}{{"type": "text", "text": userText}})

	// 12-min timeout: the refiner needs to emit a full SOW JSON
	// (~30k tokens for Sentinel-class) so it's a substantial call.
	revCtx, cancel := context.WithTimeout(ctx, 12*time.Minute)
	defer cancel()
	resp, err := callChatWithCtx(revCtx, prov, provider.ChatRequest{
		Model:     model,
		MaxTokens: 64000,
		Messages:  []provider.ChatMessage{{Role: "user", Content: userContent}},
	})
	if err != nil {
		return nil, err
	}
	raw, _ := collectModelText(resp)
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("refine response empty (stop_reason=%q)", resp.StopReason)
	}

	var refined SOW
	if _, err := jsonutil.ExtractJSONInto(raw, &refined); err != nil {
		return nil, fmt.Errorf("parse refined SOW: %w", err)
	}

	// Defensive sanitation with collision avoidance. The refiner
	// often labels split sessions like "S7 (api)" / "S7 (ui)" —
	// raw sanitize would map both to "S7" and one would clobber
	// the other in BuildSessionDAG (which keys by ID). When a
	// sanitized ID collides with one already taken, append the
	// next available suffix so each session keeps a distinct id.
	used := map[string]bool{}
	for i := range refined.Sessions {
		clean := sanitizeSessionID(refined.Sessions[i].ID)
		if clean == "" {
			clean = refined.Sessions[i].ID
		}
		if used[clean] {
			// Find an unused suffix. Start from -2 (the original
			// keeps the bare id, the next gets -2, -3, etc.).
			n := 2
			candidate := fmt.Sprintf("%s-%d", clean, n)
			for used[candidate] {
				n++
				candidate = fmt.Sprintf("%s-%d", clean, n)
			}
			clean = candidate
		}
		used[clean] = true
		refined.Sessions[i].ID = clean
	}

	// Conservation: every original task AND acceptance criterion ID
	// must appear in the refined SOW. Verifying tasks alone lets a
	// refiner silently drop ACs while still leaving each session
	// with at least one — the dispatcher would then run weakened
	// gates on exactly the plans serious enough to need refinement.
	originalTasks, originalACs := collectIDs(sow)
	refinedTasks, refinedACs := collectIDs(&refined)
	missingT := diff(originalTasks, refinedTasks)
	missingA := diff(originalACs, refinedACs)
	if len(missingT) > 0 || len(missingA) > 0 {
		var details []string
		if len(missingT) > 0 {
			preview := missingT
			if len(preview) > 8 {
				preview = preview[:8]
			}
			details = append(details, fmt.Sprintf("%d task(s) (e.g. %s)", len(missingT), strings.Join(preview, ", ")))
		}
		if len(missingA) > 0 {
			preview := missingA
			if len(preview) > 8 {
				preview = preview[:8]
			}
			details = append(details, fmt.Sprintf("%d acceptance criterion (e.g. %s)", len(missingA), strings.Join(preview, ", ")))
		}
		return nil, fmt.Errorf("refine dropped %s — preserving original SOW", strings.Join(details, " and "))
	}

	// Preserve transient flag (refine output won't have it).
	refined.ChunkedConvertApproved = sow.ChunkedConvertApproved

	return &refined, nil
}

// collectIDs returns (taskIDs, acIDs) for every session in the SOW.
// Used by the conservation check so dropped tasks AND dropped ACs
// are both detected — earlier versions only checked tasks. Empty
// IDs are filtered out: they aren't valid identifiers and would
// otherwise cause the diff to misclassify newly-IDed criteria as
// drops (an AC repaired from id="" to id="AC42" would show up as
// a drop of the "" key).
func collectIDs(sow *SOW) (map[string]bool, map[string]bool) {
	tasks := map[string]bool{}
	acs := map[string]bool{}
	for _, s := range sow.Sessions {
		for _, t := range s.Tasks {
			if t.ID != "" {
				tasks[t.ID] = true
			}
		}
		for _, a := range s.AcceptanceCriteria {
			if a.ID != "" {
				acs[a.ID] = true
			}
		}
	}
	return tasks, acs
}

// refineGateRegressions returns "" when the refined SOW preserves
// every original AC's verifier (description + at least one of
// command/file_exists/content_match), and a non-empty reason string
// otherwise. Catches the case where the refiner kept an AC's id but
// cleared its description or verifier — autoFillMissingACFields
// would otherwise turn that into a pass-by-default manual check.
func refineGateRegressions(original, refined *SOW) string {
	type acGate struct {
		desc       string
		hasGate    bool
	}
	gateOf := func(ac AcceptanceCriterion) acGate {
		hg := strings.TrimSpace(ac.Command) != "" ||
			strings.TrimSpace(ac.FileExists) != "" ||
			ac.ContentMatch != nil
		return acGate{desc: strings.TrimSpace(ac.Description), hasGate: hg}
	}
	originalACs := map[string]acGate{}
	for _, s := range original.Sessions {
		for _, a := range s.AcceptanceCriteria {
			if a.ID == "" {
				continue
			}
			originalACs[a.ID] = gateOf(a)
		}
	}
	for _, s := range refined.Sessions {
		for _, a := range s.AcceptanceCriteria {
			before, ok := originalACs[a.ID]
			if !ok {
				continue // newly added AC; allowed
			}
			after := gateOf(a)
			if before.desc != "" && after.desc == "" {
				return fmt.Sprintf("AC %s lost its description", a.ID)
			}
			if before.hasGate && !after.hasGate {
				return fmt.Sprintf("AC %s lost its verifier (command/file_exists/content_match)", a.ID)
			}
		}
	}
	return ""
}

// diff returns the keys present in a but missing from b.
func diff(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	return out
}
