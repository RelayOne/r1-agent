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

	// Defensive sanitation: strip any prose that snuck into session
	// IDs even after the prompt's explicit rule. The same fix that
	// guards the coverage round applies here.
	for i := range refined.Sessions {
		clean := sanitizeSessionID(refined.Sessions[i].ID)
		if clean == "" {
			// Fall back to original ID if sanitizer rejects.
			clean = refined.Sessions[i].ID
		}
		refined.Sessions[i].ID = clean
	}

	// Conservation check: every original task ID must appear in the
	// refined SOW. If the refiner dropped tasks, treat as failure
	// and return the unmodified SOW (the caller will proceed with
	// the original; better than a lossy refine).
	originalTasks := map[string]bool{}
	for _, s := range sow.Sessions {
		for _, t := range s.Tasks {
			originalTasks[t.ID] = true
		}
	}
	refinedTasks := map[string]bool{}
	for _, s := range refined.Sessions {
		for _, t := range s.Tasks {
			refinedTasks[t.ID] = true
		}
	}
	var missing []string
	for tid := range originalTasks {
		if !refinedTasks[tid] {
			missing = append(missing, tid)
		}
	}
	if len(missing) > 0 {
		// Show up to 8 missing IDs in the error so the operator can
		// see the scope loss without flooding the log.
		preview := missing
		if len(preview) > 8 {
			preview = preview[:8]
		}
		return nil, fmt.Errorf("refine dropped %d task(s) (e.g. %s) — preserving original SOW",
			len(missing), strings.Join(preview, ", "))
	}

	// Preserve transient flag (refine output won't have it).
	refined.ChunkedConvertApproved = sow.ChunkedConvertApproved

	return &refined, nil
}
