package plan

import (
	"encoding/json"
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
		MaxTokens: 16000,
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

	// MaxTokens 32000: a refinement emits a complete replacement SOW
	// plus reasoning. For a ~50KB source SOW with 10 sessions, the
	// output can easily hit 50KB too (~12k tokens of SOW content),
	// and an extended-thinking model will burn another 4-8k on
	// reasoning on top. The previous 16k cap was producing truncated
	// refinements — the output stopped mid-sessions-array and
	// ValidateSOW later rejected it with "SOW has no sessions".
	resp, err := prov.Chat(provider.ChatRequest{
		Model:     model,
		MaxTokens: 32000,
		Messages:  []provider.ChatMessage{{Role: "user", Content: userContent}},
	})
	if err != nil {
		return nil, fmt.Errorf("refine chat: %w", err)
	}

	raw, diag := collectModelText(resp)
	if strings.TrimSpace(raw) == "" {
		dumpRespMu.Lock()
		_ = os.WriteFile("/tmp/stoke-refine-resp-debug.json", marshalRespOrEmpty(resp), 0o644)
		dumpRespMu.Unlock()
		return nil, fmt.Errorf("refine returned no usable content (stop_reason=%q, %d blocks; response saved to /tmp/stoke-refine-resp-debug.json)\n%s", resp.StopReason, len(resp.Content), diag)
	}
	// Always dump the raw refinement output so failures downstream
	// (extract, parse, validate) have something to inspect. Overwritten
	// per call. Cheap insurance.
	dumpRespMu.Lock()
	_ = os.WriteFile("/tmp/stoke-refine-raw.txt", []byte(raw), 0o644)
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
			return nil, fmt.Errorf("parse refined SOW: %w; repair attempt also failed: %v (raw saved to /tmp/stoke-refine-raw.txt, stop_reason=%q)", extractErr, repairErr, resp.StopReason)
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
	// Dependency cleanup: drop any task.Dependencies entry pointing at
	// a task ID that no longer exists in the refined SOW. Refinement
	// can collapse/rename tasks without updating every downstream
	// reference, which previously failed validation with 'session S1
	// task T26 depends on unknown task T13' even though the rest of
	// the refinement was usable.
	autoCleanTaskDeps(refined)
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
	if sow == nil {
		return
	}
	known := map[string]bool{}
	for _, s := range sow.Sessions {
		for _, t := range s.Tasks {
			known[t.ID] = true
		}
	}
	for si := range sow.Sessions {
		for ti := range sow.Sessions[si].Tasks {
			t := &sow.Sessions[si].Tasks[ti]
			if len(t.Dependencies) == 0 {
				continue
			}
			cleaned := t.Dependencies[:0]
			for _, dep := range t.Dependencies {
				if known[dep] {
					cleaned = append(cleaned, dep)
				}
			}
			t.Dependencies = cleaned
		}
	}
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
	prompt := `The following text is supposed to be a single JSON object but has one or more structural syntax errors (missing commas, missing opening braces, truncation mid-element, etc.). Fix the syntax ONLY — do not add, remove, or change any meaningful content. Preserve every id, description, command, file path, and structural field exactly as written. If the text was truncated mid-element, close the open containers in the natural place and drop the incomplete element rather than fabricating content. Output ONLY the fixed JSON object — no markdown fences, no prose, no explanation.

BROKEN JSON:
` + brokenRaw
	userContent, _ := json.Marshal([]map[string]interface{}{{"type": "text", "text": prompt}})
	resp, err := prov.Chat(provider.ChatRequest{
		Model:     model,
		MaxTokens: 32000,
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
	_ = os.WriteFile("/tmp/stoke-refine-repair-raw.txt", []byte(raw), 0o644)
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
		// the critic found. If RefineSOW itself fails on a reject, we
		// surface the original reject error so the caller knows the
		// SOW was unsalvageable rather than merely under-refined.
		refined, err := RefineSOW(current, crit, prov, model)
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
