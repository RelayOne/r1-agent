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
	// must appear in the refined SOW. Previous behavior hard-errored
	// on any drop, which trapped the planner in a refine loop when
	// the refiner legitimately improved other parts of the SOW but
	// accidentally forgot one task (run 42 symptom: refiner dropped
	// T290 while fixing 4 blocking concerns, whole refine rejected).
	//
	// New behavior: splice dropped tasks/ACs back into their original
	// sessions (preserving all user-authored content) AND keep the
	// refiner's improvements elsewhere. If the splice creates a
	// duplicate task ID (e.g., refiner renamed T290 → T290-new and
	// we re-add T290 from the original), autoDeduplicateTaskIDs
	// further upstream will resolve the collision. Operators see a
	// "spliced N dropped ID(s)" warning so silent repairs aren't
	// hidden.
	originalTasks, originalACs := collectIDs(sow)
	refinedTasks, refinedACs := collectIDs(&refined)
	missingT := diff(originalTasks, refinedTasks)
	missingA := diff(originalACs, refinedACs)
	if len(missingT) > 0 || len(missingA) > 0 {
		spliceDroppedIDs(sow, &refined, missingT, missingA)
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
		fmt.Printf("  ⚠ refine dropped %s — spliced back into original sessions to preserve work\n",
			strings.Join(details, " and "))
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

// spliceDroppedIDs restores tasks/ACs the refiner omitted by
// copying them back from the original SOW into whichever
// session they came from. Preserves work while letting the
// refiner's structural improvements elsewhere stand.
//
// If the original session no longer exists in the refined
// SOW (refiner renamed or removed it), the dropped entries
// are appended to the first session in the refined SOW — a
// last-resort landing pad so the content isn't lost.
// Operators see the warning banner printed by the caller and
// can re-run refine with explicit preserve directives if the
// placement is wrong.
func spliceDroppedIDs(original, refined *SOW, missingTasks, missingACs []string) {
	if original == nil || refined == nil {
		return
	}
	if len(refined.Sessions) == 0 {
		// Refiner left no sessions to splice into; nothing to do.
		return
	}
	missingT := map[string]bool{}
	for _, id := range missingTasks {
		missingT[id] = true
	}
	missingA := map[string]bool{}
	for _, id := range missingACs {
		missingA[id] = true
	}
	// Build refined-session index by ID so we can target splices.
	idx := map[string]int{}
	for i, s := range refined.Sessions {
		idx[s.ID] = i
	}
	// Walk original sessions; if a task/AC is in the missing
	// set AND its original session is still present in refined,
	// append it there. Otherwise fall back to refined.Sessions[0].
	for _, origSess := range original.Sessions {
		target := 0
		if ri, ok := idx[origSess.ID]; ok {
			target = ri
		}
		for _, t := range origSess.Tasks {
			if missingT[t.ID] {
				refined.Sessions[target].Tasks = append(refined.Sessions[target].Tasks, t)
			}
		}
		for _, a := range origSess.AcceptanceCriteria {
			if missingA[a.ID] {
				refined.Sessions[target].AcceptanceCriteria = append(refined.Sessions[target].AcceptanceCriteria, a)
			}
		}
	}
}

// cmHasContent reports whether a ContentMatchCriterion is meaningful
// enough to lock against refinement. The File field is the gate's
// semantic anchor (which file we're scanning); when it's empty, the
// rest of the pipeline (critiqueAcceptanceCriteria) treats the AC
// as malformed and skips it. Locking pattern-only or fully-empty
// payloads would block legitimate refine repairs of exactly the
// malformed shapes the loop is supposed to fix.
//
// Lock when File is non-empty regardless of Pattern: the file
// targets the verifier's intent. Refine must not retarget which
// file an AC checks. The Pattern can be repaired (e.g. tightening
// "*" → "specific-string") but only via same-File payloads, which
// the tuple-equality check above enforces.
func cmHasContent(cm *ContentMatchCriterion) bool {
	if cm == nil {
		return false
	}
	return strings.TrimSpace(cm.File) != ""
}

// refineGateRegressions returns "" when the refined SOW does not
// weaken any original AC's verifier, and a non-empty reason string
// otherwise. The rules below are the result of iterated review and
// are deliberately conservative on the brittle (non-command) verifier
// kinds.
//
// Per-kind rules:
//
//   - Command ACs: the command text MAY change (this is the most
//     common legitimate reviewer-driven refine — fixing a generic,
//     unrunnable, or malformed command). Within-kind weakenings
//     like `npm test` → `true` are caught by the existing AC-command
//     scrubber in cmd/stoke that strips fake / bypass patterns
//     pre-dispatch. The kind itself, however, must NOT change to a
//     weaker verifier (no command → file_exists, no command → desc).
//
//   - file_exists ACs: the path IS the gate's semantic intent
//     ("migrations/001.sql exists"). Refining cannot change the
//     path — that retargets what the gate proves. Switching to a
//     command of equal/stronger nominal kind is also blocked
//     because the new command may not actually verify the same
//     artifact.
//
//   - content_match ACs: the (file, pattern) tuple IS the intent.
//     Same lock as file_exists — full body equality required.
//     Substituting a command that "looks similar" doesn't preserve
//     the original verifier's semantics.
//
//   - Description-only ACs (no verifier): the description text IS
//     the manual-check intent. It cannot be cleared, and the AC
//     cannot be downgraded (it's already at the floor).
//
// Adding a verifier where none existed (description-only → command,
// etc.) is allowed — that's a strengthening.
func refineGateRegressions(original, refined *SOW) string {
	type acState struct {
		hasCommand    bool
		fileExists    string
		contentMatch  string // compact JSON or "" when nil
		description   string
		hasAnyGate    bool
	}
	stateOf := func(ac AcceptanceCriterion) acState {
		st := acState{
			hasCommand:  strings.TrimSpace(ac.Command) != "",
			fileExists:  strings.TrimSpace(ac.FileExists),
			description: strings.TrimSpace(ac.Description),
		}
		if ac.ContentMatch != nil {
			// Only lock content_match payloads that are MEANINGFUL.
			// The tolerated string-form parse leaves a non-nil
			// zero-value struct (no file, no pattern) — that's a
			// known-malformed shape the refiner SHOULD repair,
			// not something we should preserve as immutable.
			if cmHasContent(ac.ContentMatch) {
				b, _ := json.Marshal(ac.ContentMatch)
				st.contentMatch = string(b)
			}
		}
		st.hasAnyGate = st.hasCommand || st.fileExists != "" || st.contentMatch != ""
		return st
	}

	originalACs := map[string]acState{}
	for _, s := range original.Sessions {
		for _, a := range s.AcceptanceCriteria {
			if a.ID == "" {
				continue
			}
			originalACs[a.ID] = stateOf(a)
		}
	}
	for _, s := range refined.Sessions {
		for _, a := range s.AcceptanceCriteria {
			before, ok := originalACs[a.ID]
			if !ok {
				continue // newly added AC; allowed
			}
			after := stateOf(a)

			// command kind — can rewrite text but cannot drop kind.
			if before.hasCommand && !after.hasCommand {
				return fmt.Sprintf("AC %s lost its command verifier", a.ID)
			}
			// file_exists — locked: path IS the intent. Also block
			// shadowing: checkOneCriterion runs Command first, so
			// adding a Command to a previously file_exists-only AC
			// silently changes what the gate proves.
			if before.fileExists != "" {
				if before.fileExists != after.fileExists {
					return fmt.Sprintf("AC %s file_exists path changed (was %q, now %q) — refine cannot retarget what a file_exists gate proves",
						a.ID, before.fileExists, after.fileExists)
				}
				if !before.hasCommand && after.hasCommand {
					return fmt.Sprintf("AC %s added a command that shadows the locked file_exists gate", a.ID)
				}
			}
			// content_match — locked: (file, pattern) IS the intent.
			// Same shadowing concern: adding command or file_exists
			// to a content_match-only AC bypasses the original.
			if before.contentMatch != "" {
				if before.contentMatch != after.contentMatch {
					return fmt.Sprintf("AC %s content_match payload changed — refine cannot retarget what a content_match gate proves", a.ID)
				}
				if !before.hasCommand && after.hasCommand {
					return fmt.Sprintf("AC %s added a command that shadows the locked content_match gate", a.ID)
				}
				if before.fileExists == "" && after.fileExists != "" {
					return fmt.Sprintf("AC %s added a file_exists that shadows the locked content_match gate", a.ID)
				}
			}
			// description-only AC — cannot lose its description.
			if !before.hasAnyGate && before.description != "" && after.description == "" {
				return fmt.Sprintf("AC %s lost its description (description-only AC)", a.ID)
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
