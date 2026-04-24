package plan

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/ericmacdougall/stoke/internal/jsonutil"
	"github.com/ericmacdougall/stoke/internal/provider"
)

// sowCritiquePrompt asks the model to grade a candidate SOW and return a
// list of structured issues. The critic pass runs AFTER prose conversion
// so we can catch problems (vague criteria, missing foundation, bad
// decomposition) before any code is written.
const sowCritiquePrompt = `You are a senior engineering reviewer grading a draft Statement of Work (SOW) before it's handed to an autonomous build agent. Your goal: find concrete issues that would cause the build to fail or produce unmaintainable code.

Score the SOW 0-100 on each dimension:
  - foundation: does the first session establish the repo layout, dependencies, and config?
  - decomposition: are sessions appropriately sized (3-12 total; each runnable in one focused work block)?
  - criteria: is EVERY session's acceptance_criteria verifiable mechanically AND hygienic?
  - stack: is the stack inferred correctly from the prose (language, framework, monorepo, infra)?
  - dependencies: are task dependencies declared properly (no cycles, no missing refs)?
  - specificity: are task descriptions single-sentence specific statements (not bullet lists or vague goals)?

ACCEPTANCE CRITERION HYGIENE RULES (flag as BLOCKING if any are violated):
  1. No references to unset environment variables. Commands like
     "cd $(mktemp -d) && git clone $REPO_URL ." are ALWAYS wrong —
     the SOW runs against the current working directory, there is no
     remote clone. Rewrite to "pnpm install && pnpm build --filter=X"
     (or the equivalent for the stack) run directly at the repo root.
  2. No "|| echo ok" / "|| true" fallbacks that swallow real failures.
     A command that always exits 0 is not a verification; it is a lie.
  3. No commands that require binaries or services that aren't part
     of the stack (e.g. 'docker build' when no Dockerfile task exists
     yet; 'axe' when no axe-core dep is declared).
  4. Commands must be runnable by a Node workspace with
     node_modules/.bin on PATH after 'pnpm install' — stoke
     auto-installs the workspace before AC evaluation, so commands
     should assume node_modules EXISTS but should not assume any
     global toolchain beyond what the stack declares.
  5. Prefer 'pnpm <script>' or direct binary calls (tsc, vitest, next)
     over 'npx' / 'pnpm exec'. Local workspace binaries resolve
     without wrappers because stoke prepends node_modules/.bin to
     PATH.
  6. File-existence criteria are fine but SHOULD be paired with a
     content_match or command that verifies the file is not empty /
     is a real implementation. A file_exists check alone passes on
     any 0-byte file the model writes.
  7. No Playwright, Cypress, or browser-based E2E test commands.
     These require browser binaries and display servers that the
     build agent doesn't have. Flag any AC that runs playwright,
     cypress, or puppeteer as BLOCKING.

Output ONLY a JSON object. No prose, no markdown fences.

{
  "overall_score": int 0-100,
  "dimensions": {
    "foundation": int,
    "decomposition": int,
    "criteria": int,
    "stack": int,
    "dependencies": int,
    "specificity": int
  },
  "issues": [
    {
      "severity": "blocking|major|minor",
      "session_id": "optional — which session",
      "task_id": "optional — which task",
      "description": "what's wrong",
      "fix": "specific suggestion"
    }
  ],
  "verdict": "ship|refine|reject",
  "summary": "one-paragraph explanation of your verdict"
}

VERDICT RULES:
  - "ship": overall score >= 80, no blocking issues
  - "refine": any blocking issue OR overall score < 80 OR any dimension < 60
  - "reject": the SOW is fundamentally broken (no sessions, all criteria manual, etc.)

DRAFT SOW:
`

// sowRefinePrompt asks the model to produce a fixed SOW given a set of
// critique issues. The result must be a complete replacement SOW, not a
// patch.
const sowRefinePrompt = `You are refining a Statement of Work (SOW) based on specific reviewer issues. Your job: produce a complete, improved replacement SOW that addresses EVERY issue below while preserving the parts of the original that were correct.

Output ONLY a JSON SOW in the same schema as the original — no prose, no markdown fences, no commentary.

RULES:
1. Preserve the original SOW's id, name, and any sessions the review didn't flag.
2. Rewrite or replace only what's flagged.
3. Every session MUST have at least one mechanically-verifiable acceptance_criterion (command / file_exists / content_match).
4. Task IDs stay unique across the whole SOW.
5. Do NOT introduce new sessions just for the sake of it — fix what's broken.
6. ACCEPTANCE CRITERIA COMMANDS must be runnable against the current
   working directory. Do NOT emit commands that clone a remote repo,
   reference unset env vars ($REPO_URL, $CI, etc.), or use "|| true" /
   "|| echo ok" fallbacks that swallow failures. Assume stoke runs
   'pnpm install' before AC evaluation and that node_modules/.bin is
   on PATH, so prefer direct binary invocations (tsc, vitest, next)
   and workspace scripts (pnpm --filter X build) over npx wrappers.
7. When rewriting an acceptance command, keep it SMALL and focused on
   a single observable outcome. 'pnpm build --filter=@sentinel/types'
   is a valid command; 'cd $(mktemp -d) && git clone $REPO_URL . && ...'
   is not.

CRITIQUE ISSUES:
`

// SOWCritique is the structured output of a critic pass.
type SOWCritique struct {
	OverallScore int            `json:"overall_score"`
	Dimensions   map[string]int `json:"dimensions"`
	Issues       []CritiqueIssue `json:"issues"`
	Verdict      string         `json:"verdict"` // ship | refine | reject
	Summary      string         `json:"summary"`
}

// CritiqueIssue is a single concern the critic flagged.
type CritiqueIssue struct {
	Severity    string `json:"severity"` // blocking | major | minor
	SessionID   string `json:"session_id,omitempty"`
	TaskID      string `json:"task_id,omitempty"`
	Description string `json:"description"`
	Fix         string `json:"fix"`
}

// HasBlocking reports whether the critique includes any blocking issues.
func (c *SOWCritique) HasBlocking() bool {
	for _, i := range c.Issues {
		if i.Severity == "blocking" {
			return true
		}
	}
	return false
}

// CritiqueSOW runs the critic pass against a candidate SOW. Returns the
// structured critique or an error if the provider call fails / the
// response can't be parsed.
func CritiqueSOW(sow *SOW, prov provider.Provider, model string) (*SOWCritique, error) {
	if sow == nil {
		return nil, fmt.Errorf("nil SOW")
	}
	if prov == nil {
		return nil, fmt.Errorf("no provider configured")
	}
	sowBlob, err := json.MarshalIndent(sow, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal sow: %w", err)
	}
	if model == "" {
		model = "claude-sonnet-4-6"
	}
	userText := sowCritiquePrompt + string(sowBlob)
	userContent, _ := json.Marshal([]map[string]interface{}{{"type": "text", "text": userText}})

	resp, err := prov.Chat(provider.ChatRequest{
		Model: model,
		// 16k matches the convert/refine ceilings. Extended-thinking
		// models can burn 4-8k on reasoning before emitting the final
		// JSON, and the previous 8k cap was leaving them no room to
		// finish — the response came back with thinking-only or empty
		// content blocks, which collectModelText now also salvages.
		MaxTokens: 64000,
		Messages:  []provider.ChatMessage{{Role: "user", Content: userContent}},
	})
	if err != nil {
		return nil, fmt.Errorf("critic chat: %w", err)
	}
	raw, diag := collectModelText(resp)
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("critic returned no usable content (stop_reason=%q, %d blocks)\n%s", resp.StopReason, len(resp.Content), diag)
	}
	var crit SOWCritique
	if _, err := jsonutil.ExtractJSONInto(raw, &crit); err != nil {
		return nil, fmt.Errorf("parse critique: %w", err)
	}
	return &crit, nil
}

// RefineSOW rewrites a SOW to address the issues in a critique. Returns a
// new SOW that has been re-validated against the schema.
func RefineSOW(original *SOW, crit *SOWCritique, prov provider.Provider, model string) (*SOW, error) {
	if original == nil || crit == nil {
		return nil, fmt.Errorf("nil SOW or critique")
	}
	if prov == nil {
		return nil, fmt.Errorf("no provider configured")
	}
	if model == "" {
		model = "claude-sonnet-4-6"
	}

	origBlob, err := json.MarshalIndent(original, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal original: %w", err)
	}
	critBlob, err := json.MarshalIndent(crit, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal critique: %w", err)
	}

	userText := sowRefinePrompt + string(critBlob) + "\n\nORIGINAL SOW:\n" + string(origBlob)
	userContent, _ := json.Marshal([]map[string]interface{}{{"type": "text", "text": userText}})

	// MaxTokens 64000: matches ConvertProseToSOW. 32k was hitting
	// stop_reason=max_tokens on 20+ session SOWs — the truncated
	// output landed a session with zero ACs which then failed
	// validation. Papering over that with a synthetic stub AC is a
	// fake completion (session trivially passes with `true` command)
	// so the right fix is fitting the actual content. 64k is enough
	// for a 400-task SOW with extended-thinking reasoning overhead.
	resp, err := prov.Chat(provider.ChatRequest{
		Model:     model,
		MaxTokens: 64000,
		Messages:  []provider.ChatMessage{{Role: "user", Content: userContent}},
	})
	if err != nil {
		return nil, fmt.Errorf("refine chat: %w", err)
	}
	// Detect LLM truncation and fail loudly. The prior "truncate
	// then autofill stub ACs" path produced bogus always-passing
	// ACs; callers must know truncation happened so they can
	// increase budget / chunk / fall back to the pre-refine SOW
	// rather than silently shipping a lie.
	if resp.StopReason == "max_tokens" {
		return nil, fmt.Errorf("refine chat truncated at max_tokens — increase MaxTokens or chunk per-session (raw: /tmp/stoke-refine-raw.txt)")
	}

	raw, diag := collectModelText(resp)
	if strings.TrimSpace(raw) == "" {
		dumpRespMu.Lock()
		_ = os.WriteFile("/tmp/stoke-refine-resp-debug.json", marshalRespOrEmpty(resp), 0o644) // #nosec G306 -- plan/SOW artefact consumed by Stoke tooling; 0644 is appropriate.
		dumpRespMu.Unlock()
		return nil, fmt.Errorf("refine returned no usable content (stop_reason=%q, %d blocks; response saved to /tmp/stoke-refine-resp-debug.json)\n%s", resp.StopReason, len(resp.Content), diag)
	}
	// Always dump the raw refinement output so failures downstream
	// (extract, parse, validate) have something to inspect. Overwritten
	// per call. Cheap insurance.
	dumpRespMu.Lock()
	_ = os.WriteFile("/tmp/stoke-refine-raw.txt", []byte(raw), 0o644) // #nosec G306 -- plan/SOW artefact consumed by Stoke tooling; 0644 is appropriate.
	dumpRespMu.Unlock()

	blob, extractErr := jsonutil.ExtractJSONObject(raw)
	if extractErr != nil {
		// Last-ditch: send the broken JSON back to the model with a
		// narrow "fix the syntax only" prompt. LLMs emitting long
		// SOWs occasionally produce structurally invalid JSON
		// (missing commas, missing braces on array elements) that
		// no static repair pass can reliably fix. The repair call is
		// cheap because the input is only the broken blob plus a
		// one-paragraph directive.
		repaired, repairErr := repairJSONViaLLM(raw, prov, model)
		if repairErr != nil {
			return nil, errors.Join(
				fmt.Errorf("parse refined SOW: %w", extractErr),
				fmt.Errorf("repair attempt also failed: %w (raw saved to /tmp/stoke-refine-raw.txt, stop_reason=%q)", repairErr, resp.StopReason),
			)
		}
		blob = repaired
	}
	refined, err := ParseSOW(blob, "refined.json")
	if err != nil {
		return nil, fmt.Errorf("parse refined SOW: %w (raw: /tmp/stoke-refine-raw.txt, stop_reason=%q)", err, resp.StopReason)
	}
	// If the refinement came back with an empty sessions array — which
	// typically means the model truncated mid-output — and the original
	// had sessions, splice the original's sessions back in rather than
	// failing the entire refinement. The non-session fields (id, name,
	// stack, description) from the refinement are still useful, and
	// preserving original sessions is safer than returning no SOW at
	// all to the caller. This is a guard against extended-thinking
	// models that use most of their output budget on reasoning.
	if len(refined.Sessions) == 0 && len(original.Sessions) > 0 {
		refined.Sessions = original.Sessions
	}
	// Infra consistency fixup: if a session references infra that isn't
	// declared in stack.infra (e.g. the refiner added docker to a session
	// without adding docker to the stack), auto-add a stub infra entry
	// so ValidateSOW accepts it. This is non-destructive — the operator
	// still sees the infra is needed, and downstream env checks will
	// catch any required env vars. Without this fixup, a trivial
	// oversight in the refined output (single missing stack entry)
	// would nuke the entire refinement pass.
	autoAddMissingInfra(refined)
	// Task-ID uniqueness: refine passes occasionally emit the same
	// task ID in two sessions (counter reset bug in the refiner's
	// session-by-session rewrite). ValidateSOW rejects any SOW with
	// duplicates, which nuked the whole refinement pass. Deduplicate
	// BEFORE the dep-cleanup so intra-session dep references get
	// rewritten to the renamed IDs rather than dropped.
	autoDeduplicateTaskIDs(refined)
	// Dependency cleanup: drop any task.Dependencies entry pointing at
	// a task ID that no longer exists in the refined SOW. Refinement
	// can collapse/rename tasks without updating every downstream
	// reference, which previously failed validation with 'session S1
	// task T26 depends on unknown task T13' even though the rest of
	// the refinement was usable.
	autoCleanTaskDeps(refined)
	// autoFillMissingACFields synthesizes id/description on ACs that
	// the model emitted as partial structures — harmless, doesn't
	// invent verification from nothing. NOT auto-injecting stub ACs
	// for zero-AC sessions: that was a fake-completion vector (the
	// stub is an always-pass `true` command, making the session
	// trivially "succeed"). Max_tokens truncation is caught earlier
	// and surfaces as a hard error so callers can retry / chunk /
	// fall back.
	autoFillMissingACFields(refined)
	if errs := ValidateSOW(refined); len(errs) > 0 {
		return nil, fmt.Errorf("refined SOW failed validation: %s (raw: /tmp/stoke-refine-raw.txt, stop_reason=%q)", strings.Join(errs, "; "), resp.StopReason)
	}
	return refined, nil
}

// autoAddMissingInfra walks every session.InfraNeeded entry and, if any
// name is not already declared in sow.Stack.Infra, appends a stub infra
// entry for it. This fixes the common refinement failure where the
// model adds a new infra reference to a session (e.g. "docker") without
// also updating the top-level stack.infra list — a trivial oversight
// that previously nuked the entire refinement pass when ValidateSOW
// rejected it with "session S10 references unknown infra: docker".
//
// Mutation is in place on the passed SOW.
func autoAddMissingInfra(sow *SOW) {
	if sow == nil {
		return
	}
	declared := map[string]bool{}
	for _, inf := range sow.Stack.Infra {
		declared[inf.Name] = true
	}
	for _, s := range sow.Sessions {
		for _, needed := range s.InfraNeeded {
			if declared[needed] || needed == "" {
				continue
			}
			sow.Stack.Infra = append(sow.Stack.Infra, InfraRequirement{Name: needed})
			declared[needed] = true
		}
	}
}

// autoCleanTaskDeps drops any task.Dependencies entries that reference a
// task ID which doesn't exist anywhere in the SOW. Refinement sometimes
// collapses tasks or renames IDs without updating the dependency graph
// ('session S1 task T26 depends on unknown task T13'), and ValidateSOW
// would then reject the refined SOW even though the rest of it was
// usable. Pruning the orphaned dep is safer than requiring the refiner
// to emit a perfectly consistent graph: the worst case is a task runs
// before something it wanted to wait for, which the intra-session
// scheduler already handles via its own retry logic.
//
// Mutation is in place on the passed SOW.
func autoCleanTaskDeps(sow *SOW) {
	_ = CleanTaskDependencies(sow)
}

// autoDeduplicateTaskIDs ensures every task ID is unique across the
// whole SOW. The refiner occasionally emits the same counter-style ID
// (T398, T399, ...) in two sessions because its per-session rewrite
// loses track of the global namespace. ValidateSOW rejects any such
// duplicate outright, which previously nuked the entire refinement
// pass and left the planner stuck in a refine loop (run 41 symptom).
//
// Rename strategy:
//   - First occurrence of an ID wins (kept as-is).
//   - Subsequent occurrences are renamed to `<origID>-<sessionID>`
//     (session ID preserved verbatim — no case folding — so
//     rewritten tokens match the repo's conventional `T<n>-S<m>`
//     casing). E.g. T398 in S5 becomes T398-S5.
//   - If that suffix is also taken (very rare), append a numeric
//     disambiguator: T398-S5-2, T398-S5-3, etc.
//   - Intra-session task.Dependencies entries that pointed at the
//     old ID get rewritten to the new ID ONLY WHEN the dep target
//     was originally a task in the same session. Cross-session
//     references to the surviving first-occurrence copy stay
//     intact. Earlier versions either (a) always rewrote
//     (corrupting cross-session deps) or (b) always skipped
//     (breaking intra-session wiring via a tautological guard).
//     The per-session-originals track catches exactly the case
//     we want — a dep to "T10" inside session S5 where S5 had
//     its own T10 that got renamed is obviously a local ref;
//     a dep to "T10" inside session S5 where S5 never had a
//     T10 must have meant another session's T10.
//
// Mutation is in place on the passed SOW.
func autoDeduplicateTaskIDs(sow *SOW) {
	if sow == nil {
		return
	}
	seen := map[string]bool{}
	// Pre-compute each session's ORIGINAL task-ID set (the IDs
	// present before we start renaming). Used below to decide
	// whether a dep rewrite is local (safe) or cross-session
	// (ambiguous — leave alone).
	origBySession := make([]map[string]bool, len(sow.Sessions))
	for si, s := range sow.Sessions {
		ids := map[string]bool{}
		for _, t := range s.Tasks {
			id := strings.TrimSpace(t.ID)
			if id != "" {
				ids[id] = true
			}
		}
		origBySession[si] = ids
	}
	for si := range sow.Sessions {
		s := &sow.Sessions[si]
		sessLabel := strings.TrimSpace(s.ID)
		if sessLabel == "" {
			sessLabel = fmt.Sprintf("sess%d", si)
		}
		renames := map[string]string{}
		for ti := range s.Tasks {
			t := &s.Tasks[ti]
			orig := strings.TrimSpace(t.ID)
			if orig == "" {
				continue
			}
			if !seen[orig] {
				seen[orig] = true
				continue
			}
			candidate := orig + "-" + sessLabel
			for n := 2; seen[candidate]; n++ {
				candidate = fmt.Sprintf("%s-%s-%d", orig, sessLabel, n)
			}
			renames[orig] = candidate
			t.ID = candidate
			seen[candidate] = true
		}
		if len(renames) == 0 {
			continue
		}
		origs := origBySession[si]
		for ti := range s.Tasks {
			deps := s.Tasks[ti].Dependencies
			for i, d := range deps {
				target := strings.TrimSpace(d)
				nd, ok := renames[target]
				if !ok {
					continue
				}
				// Rewrite only when the dep target was a task in
				// THIS session before rename. A dep to "T10" from
				// a session whose originals never included "T10"
				// must have been a cross-session reference.
				if !origs[target] {
					continue
				}
				deps[i] = nd
			}
		}
	}
}

// DroppedDep describes one auto-repair action performed on a SOW:
// either an empty-ID/empty-description task dropped, or a dangling
// Dependencies entry dropped because its target doesn't resolve.
// Surfacing these lets the operator see silent repairs that previously
// only showed up as a quiet "plan warning" after dispatch began.
type DroppedDep struct {
	SessionID string
	TaskID    string
	Dropped   string
	Reason    string
}

// CleanTaskDependencies drops malformed tasks (empty ID or description)
// and removes any task.Dependencies entry whose target doesn't exist in
// the SOW. Returns a list of every dropped item so the caller can
// surface the repair to the operator. Idempotent.
//
// This is the authoritative pre-dispatch cleaner. Call it at every
// boundary where a new SOW shape could introduce dangling refs:
// initial prose conversion, critique/refine, session-sizer splits,
// and the main.go dispatch path right before ValidateSOW.
func CleanTaskDependencies(sow *SOW) []DroppedDep {
	if sow == nil {
		return nil
	}
	var drops []DroppedDep
	// Drop empty-task entries BEFORE computing the known set. LLM
	// output occasionally emits an object with empty ID or empty
	// description (truncation mid-element, schema drift). Dropping
	// the malformed entry is safer than halting on a validation
	// error like "session S15 task[3] has no ID" — the session
	// scheduler + integration reviewer will catch any missing
	// downstream coverage.
	for si := range sow.Sessions {
		sid := sow.Sessions[si].ID
		in := sow.Sessions[si].Tasks
		out := in[:0]
		for _, t := range in {
			if strings.TrimSpace(t.ID) == "" || strings.TrimSpace(t.Description) == "" {
				drops = append(drops, DroppedDep{
					SessionID: sid,
					TaskID:    t.ID,
					Dropped:   "(entire task)",
					Reason:    "empty ID or empty description",
				})
				continue
			}
			out = append(out, t)
		}
		sow.Sessions[si].Tasks = out
	}
	known := map[string]bool{}
	for _, s := range sow.Sessions {
		for _, t := range s.Tasks {
			known[t.ID] = true
		}
	}
	for si := range sow.Sessions {
		sid := sow.Sessions[si].ID
		for ti := range sow.Sessions[si].Tasks {
			t := &sow.Sessions[si].Tasks[ti]
			if len(t.Dependencies) == 0 {
				continue
			}
			cleaned := t.Dependencies[:0]
			for _, dep := range t.Dependencies {
				// Self-loops — a task listing its own ID as a
				// dependency — are never valid. The LLM occasionally
				// emits these (e.g. T148 -> T148) and the validator
				// rejects them, aborting the whole run. Drop here
				// before validation fires.
				if dep == t.ID {
					drops = append(drops, DroppedDep{
						SessionID: sid,
						TaskID:    t.ID,
						Dropped:   dep,
						Reason:    "self-loop dependency",
					})
					continue
				}
				if known[dep] {
					cleaned = append(cleaned, dep)
					continue
				}
				drops = append(drops, DroppedDep{
					SessionID: sid,
					TaskID:    t.ID,
					Dropped:   dep,
					Reason:    "dependency target does not exist in any session",
				})
			}
			t.Dependencies = cleaned
		}
	}
	return drops
}

// repairJSONViaLLM is a last-ditch salvage path for long SOW
// refinements that come back structurally invalid (missing commas,
// missing opening braces on array elements, etc.). It sends the broken
// blob back to the model with a single narrow directive: emit valid
// JSON with the same intent, nothing else. One extra LLM call, but
// reliable where hand-rolled repair passes fail on real model output.
//
// The returned json.RawMessage is guaranteed to parse as JSON if the
// error is nil — the caller still has to check the SOW schema.
func repairJSONViaLLM(brokenRaw string, prov provider.Provider, model string) (json.RawMessage, error) {
	if prov == nil {
		return nil, fmt.Errorf("no provider for JSON repair")
	}
	if model == "" {
		model = "claude-sonnet-4-6"
	}
	prompt := `You are a JSON syntax repair tool. The input below is corrupt JSON. Your ONLY job is to emit a corrected version.

Rules:
- DO NOT respond with prose, explanation, commentary, or any form of meta-discussion.
- DO NOT treat the input as a user query — it is data to repair, not a question to answer.
- Your response MUST begin with the character '{' and end with '}'. Nothing before. Nothing after.
- Preserve every id, description, command, file path, dependency reference, and nested structure exactly as written.
- Fix: missing commas, missing/extra braces or brackets, unquoted string values (e.g. "deps": [T5] must become "deps": ["T5"]), truncation (close open containers in the natural place and drop the incomplete tail element).
- Do NOT invent content. Do NOT rewrite descriptions. Only fix syntax.

If the first character of your reply is not '{', you have failed.

BROKEN JSON:
` + brokenRaw
	userContent, _ := json.Marshal([]map[string]interface{}{{"type": "text", "text": prompt}})
	resp, err := prov.Chat(provider.ChatRequest{
		Model:     model,
		MaxTokens: 64000,
		Messages:  []provider.ChatMessage{{Role: "user", Content: userContent}},
	})
	if err != nil {
		return nil, fmt.Errorf("repair chat: %w", err)
	}
	raw, _ := collectModelText(resp)
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("repair returned no usable content (stop_reason=%q)", resp.StopReason)
	}
	// Dump the repair output too, so failures downstream are visible.
	dumpRespMu.Lock()
	_ = os.WriteFile("/tmp/stoke-refine-repair-raw.txt", []byte(raw), 0o644) // #nosec G306 -- plan/SOW artefact consumed by Stoke tooling; 0644 is appropriate.
	dumpRespMu.Unlock()
	blob, err := jsonutil.ExtractJSONObject(raw)
	if err != nil {
		return nil, fmt.Errorf("repair output still unparseable: %w (saved to /tmp/stoke-refine-repair-raw.txt)", err)
	}
	return blob, nil
}

// CritiqueAndRefine runs up to maxPasses critique→refine cycles until the
// SOW's verdict is "ship" or the pass budget is exhausted. Returns the
// best SOW produced and the final critique. If the budget runs out with
// issues remaining, the last refined SOW is still returned (caller can
// choose to proceed or abort).
//
// This is the "smart" entry point called from sowCmd after prose
// conversion: turn a rough LLM-generated SOW into something the build
// agent can actually execute against.
//
// Verdict handling:
//   - "ship" + no blocking issues → accept and return immediately
//   - "refine" → call RefineSOW, loop with the refined SOW
//   - "reject" → ALSO call RefineSOW. A reject verdict means "this SOW
//     is too broken to ship as-is", which is the strongest possible
//     signal that we should rewrite it. The previous behavior — return
//     an error and let the caller proceed with the buggy SOW — defeated
//     the entire point of the critique pass: it became informational
//     only at exactly the moment it mattered most. If refinement also
//     fails, THEN we surface the error.
// sessionRefinePrompt is the per-session refine prompt used by
// RefineSOWChunked. It's narrower than sowRefinePrompt: instead of
// "rewrite the whole SOW", the LLM is asked to emit a single
// refined session JSON object given that session's slice of the
// original SOW + the critique issues targeting it.
const sessionRefinePrompt = `You are refining ONE session of a larger Statement of Work (SOW). The rest of the SOW is unchanged; your job is to emit the refined session JSON object addressing every reviewer issue below.

Output ONLY a JSON session object matching the Session schema — no surrounding prose, no markdown fences, no commentary, no top-level SOW wrapper. Start with '{' and end with '}'.

Required shape:
{
  "id": "same ID as original session",
  "phase": "optional phase label",
  "title": "string",
  "description": "string",
  "tasks": [ { "id": "TN", "description": "...", "files": [...], "dependencies": [...] } ],
  "acceptance_criteria": [ { "id": "ACN", "description": "...", "command": "..." } ],
  "inputs": ["artifact names"],
  "outputs": ["artifact names"]
}

RULES:
1. Keep the session.id EXACTLY as provided; the reassembler matches on it.
2. Task IDs inside this session should stay stable unless a reviewer issue requires renaming; if you rename, every dependency reference inside THIS session must be updated.
3. Do NOT reference tasks in OTHER sessions — the reassembler will keep cross-session deps from the original wherever you preserve the original task IDs.
4. Every session MUST have at least one mechanically-verifiable acceptance_criterion.
5. Acceptance commands must be runnable in the current working directory — no remote clones, no unset env vars, no '|| true' fallbacks, no long-running dev servers.
6. Node workspaces have node_modules/.bin on PATH after pnpm install; prefer direct binaries ('tsc', 'vitest run') over 'npx'.
7. Do NOT invent toolchains not in the SOW stack (no docker build without a Dockerfile task, no axe without axe-core dep).

ORIGINAL SESSION:
`

// RefineSOWChunked runs refinement per-session instead of as one
// monolithic LLM call. This avoids the max_tokens truncation that
// hits large SOWs: a 20-session refine can easily exceed 32k output
// tokens. Chunking keeps each call to ~2-5k output per session.
//
// Flow:
//   1. Group critique issues by session_id. Global issues (no
//      session_id) surface to EVERY flagged session so the refiner
//      sees cross-cutting concerns. Non-flagged sessions pass
//      through unchanged.
//   2. For each flagged session, call the LLM with just that
//      session's JSON + its issues.
//   3. Reassemble: replace the original session with the refined
//      one by ID; sessions not touched by refine retain their
//      original definitions.
//   4. Run the same post-refine cleanup (infra, deps, AC fields) +
//      ValidateSOW the monolithic path uses.
//
// Returns the refined SOW or an error if any per-session call
// fails / max_tokens truncates. Per-session failure surfaces as
// a hard error (not swallowed) so callers can retry or fall back.
func RefineSOWChunked(original *SOW, crit *SOWCritique, prov provider.Provider, model string) (*SOW, error) {
	if original == nil || crit == nil {
		return nil, fmt.Errorf("nil SOW or critique")
	}
	if prov == nil {
		return nil, fmt.Errorf("no provider configured")
	}
	if model == "" {
		model = "claude-sonnet-4-6"
	}

	// Bucket issues by session_id. Empty session_id = global.
	bySession := map[string][]CritiqueIssue{}
	var global []CritiqueIssue
	for _, iss := range crit.Issues {
		if strings.TrimSpace(iss.SessionID) == "" {
			global = append(global, iss)
			continue
		}
		bySession[iss.SessionID] = append(bySession[iss.SessionID], iss)
	}

	// Deep-copy the original so we don't mutate caller state if a
	// per-session call fails midway. The outer Sessions slice is
	// copied, and every NESTED slice (Tasks, AcceptanceCriteria,
	// Inputs, Outputs, InfraNeeded, per-Task Dependencies/Files/
	// Verification) is also copied so downstream cleanup passes
	// that mutate refined.Sessions[i].Tasks[j].Dependencies don't
	// reach into the caller's original SOW.
	refined := *original
	refined.Sessions = make([]Session, len(original.Sessions))
	for i, s := range original.Sessions {
		cs := s
		cs.Tasks = make([]Task, len(s.Tasks))
		for j, t := range s.Tasks {
			ct := t
			ct.Dependencies = append([]string(nil), t.Dependencies...)
			ct.Files = append([]string(nil), t.Files...)
			ct.Verification = append([]string(nil), t.Verification...)
			cs.Tasks[j] = ct
		}
		cs.AcceptanceCriteria = append([]AcceptanceCriterion(nil), s.AcceptanceCriteria...)
		cs.Inputs = append([]string(nil), s.Inputs...)
		cs.Outputs = append([]string(nil), s.Outputs...)
		cs.InfraNeeded = append([]string(nil), s.InfraNeeded...)
		refined.Sessions[i] = cs
	}

	// Identify sessions that need refine: those with session-specific
	// issues. When there are ZERO session-specific issues but global
	// issues exist, refine every session (rare case).
	targets := map[string]bool{}
	for sid := range bySession {
		targets[sid] = true
	}
	if len(targets) == 0 && len(global) > 0 {
		for _, s := range original.Sessions {
			targets[s.ID] = true
		}
	}

	for i, sess := range refined.Sessions {
		if !targets[sess.ID] {
			continue
		}
		issues := append([]CritiqueIssue(nil), bySession[sess.ID]...)
		issues = append(issues, global...)
		newSess, err := refineOneSession(sess, issues, prov, model)
		if err != nil {
			return nil, fmt.Errorf("refine session %s: %w", sess.ID, err)
		}
		if strings.TrimSpace(newSess.ID) == "" {
			newSess.ID = sess.ID
		}
		refined.Sessions[i] = newSess
	}

	// Post-refine cleanups — identical to the monolith's pre-
	// ValidateSOW chain so chunked + monolith produce the same
	// downstream shape.
	autoAddMissingInfra(&refined)
	autoDeduplicateTaskIDs(&refined)
	autoCleanTaskDeps(&refined)
	autoFillMissingACFields(&refined)
	if errs := ValidateSOW(&refined); len(errs) > 0 {
		return nil, fmt.Errorf("chunked refine failed validation: %s", strings.Join(errs, "; "))
	}
	return &refined, nil
}

// refineOneSession issues the per-session LLM call + parses the
// response into a plan.Session. Used internally by
// RefineSOWChunked.
func refineOneSession(sess Session, issues []CritiqueIssue, prov provider.Provider, model string) (Session, error) {
	sessBlob, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return Session{}, fmt.Errorf("marshal session: %w", err)
	}
	var issuesBuf strings.Builder
	issuesBuf.WriteString("REVIEWER ISSUES TARGETING THIS SESSION:\n")
	if len(issues) == 0 {
		issuesBuf.WriteString("(none; but refine for consistency with cross-cutting concerns)\n")
	} else {
		for _, iss := range issues {
			loc := ""
			if iss.TaskID != "" {
				loc = " task " + iss.TaskID
			}
			fmt.Fprintf(&issuesBuf, "  - [%s]%s %s (fix: %s)\n", iss.Severity, loc, iss.Description, iss.Fix)
		}
	}
	userText := sessionRefinePrompt + string(sessBlob) + "\n\n" + issuesBuf.String()
	userContent, _ := json.Marshal([]map[string]interface{}{{"type": "text", "text": userText}})

	// Per-session budget: 16k is ample for a single session (even a
	// 30-task session with 5 ACs fits in ~5k tokens of JSON).
	resp, err := prov.Chat(provider.ChatRequest{
		Model:     model,
		MaxTokens: 64000,
		Messages:  []provider.ChatMessage{{Role: "user", Content: userContent}},
	})
	if err != nil {
		return Session{}, fmt.Errorf("chat: %w", err)
	}
	if resp.StopReason == "max_tokens" {
		return Session{}, fmt.Errorf("per-session refine truncated at max_tokens (session=%s) — session likely too large; split it", sess.ID)
	}
	raw, _ := collectModelText(resp)
	if strings.TrimSpace(raw) == "" {
		return Session{}, fmt.Errorf("empty response (stop_reason=%s)", resp.StopReason)
	}

	var out Session
	if _, err := jsonutil.ExtractJSONInto(raw, &out); err != nil {
		return Session{}, fmt.Errorf("parse session JSON: %w", err)
	}
	return out, nil
}

func CritiqueAndRefine(sow *SOW, prov provider.Provider, model string, maxPasses int) (*SOW, *SOWCritique, error) {
	if maxPasses < 1 {
		maxPasses = 2
	}
	current := sow
	var lastCrit *SOWCritique
	for pass := 1; pass <= maxPasses; pass++ {
		crit, err := CritiqueSOW(current, prov, model)
		if err != nil {
			return current, lastCrit, fmt.Errorf("critique pass %d: %w", pass, err)
		}
		lastCrit = crit
		if crit.Verdict == "ship" && !crit.HasBlocking() {
			return current, crit, nil
		}
		// Both "refine" and "reject" trigger a refinement attempt. The
		// difference is severity, not action: we always try to fix what
		// the critic found. Prefer chunked per-session refine (avoids
		// max_tokens truncation that was landing zero-AC sessions on
		// 20+ session SOWs); fall back to the monolithic refine when
		// chunked errors out for a reason that re-running the whole
		// SOW might fix (e.g. transient LLM glitch on one session).
		refined, err := RefineSOWChunked(current, crit, prov, model)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  critique note: chunked refine pass %d failed (%v) — retrying with monolith\n", pass, err)
			refined, err = RefineSOW(current, crit, prov, model)
		}
		if err != nil {
			if crit.Verdict == "reject" {
				return current, crit, fmt.Errorf("refine pass %d failed AND critic rejected SOW: %s; refine error: %w", pass, crit.Summary, err)
			}
			// "refine" verdict + refine failed: return the current SOW
			// with the critique so the caller can decide.
			return current, crit, fmt.Errorf("refine pass %d: %w", pass, err)
		}
		current = refined
	}
	return current, lastCrit, nil
}
