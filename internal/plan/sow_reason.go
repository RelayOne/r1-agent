package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ericmacdougall/stoke/internal/jsonutil"
	"github.com/ericmacdougall/stoke/internal/provider"
)

// ReasoningVerdict is the structured decision returned by ReasonAboutFailure.
// It encodes the LLM's judgment about whether a stuck acceptance criterion
// represents a bug in the code, a bug in the criterion itself, both, or
// neither (acceptable as-is).
type ReasoningVerdict struct {
	// Category is one of: "code_bug", "ac_bug", "both", "acceptable_as_is".
	//
	//   code_bug        — the AC is well-defined and the code is wrong.
	//   ac_bug          — the AC is structurally wrong. The code is fine.
	//   both            — code gap AND the AC is over-strict.
	//   acceptable_as_is — false positive; approve an ignore.
	Category string `json:"category"`

	// Reasoning is the synthesis-pass explanation of how the judge
	// reached Category, drawing on the individual analyst outputs.
	Reasoning string `json:"reasoning"`

	// CodeFix is a concise directive for the repair loop: what file(s)
	// to edit, what to change, what to verify. Populated when Category
	// is "code_bug" or "both".
	CodeFix string `json:"code_fix,omitempty"`

	// ACRewrite, when non-empty, replaces the failing criterion's
	// Command with this value. Populated when Category is "ac_bug"
	// or "both".
	ACRewrite string `json:"ac_rewrite,omitempty"`

	// ApproveReason is the justification for marking the failure as
	// acceptable. Populated only when Category is "acceptable_as_is".
	ApproveReason string `json:"approve_reason,omitempty"`

	// AnalystNotes carries the raw outputs of the individual analyst
	// passes that the judge synthesized from. Always populated; the
	// operator can log them to see which analyst caught what.
	AnalystNotes map[string]string `json:"analyst_notes,omitempty"`
}

// ReasoningInput bundles everything ReasonAboutFailure needs.
type ReasoningInput struct {
	SessionID       string
	SessionTitle    string
	TaskDescription string
	Criterion       AcceptanceCriterion
	FailureOutput   string
	PriorAttempts   int
	CodeExcerpts    map[string]string
	RepoRoot        string
}

// ReasonAboutFailure runs a multi-analyst + judge reasoning loop on a
// stuck acceptance criterion. Instead of one big prompt asking the LLM
// to classify the failure, we run several SMALL focused prompts —
// each one asking a single specific question that requires reasoning
// output — and then feed all of their answers into a synthesis prompt
// that picks the final verdict. The multi-pass pattern avoids the
// "model ignores half the requirements because the prompt was too big"
// failure mode we kept seeing in the critique+refine path.
//
// Analysts (each is a separate LLM call):
//
//	A1 code-review    : "Given the task spec and the code, is the
//	                    code correct per the spec? Reason step by step."
//	A2 ac-hygiene     : "Given the AC command and the task spec, is
//	                    the AC well-formed and testing the right thing?"
//	A3 root-cause     : "Given the failure output, what is the likely
//	                    root cause — missing dep, wrong path, syntax
//	                    error, runtime behavior mismatch, something
//	                    else?"
//	A4 ac-rewrite     : "IF the AC is the wrong shape, propose a
//	                    replacement command that matches hygiene rules.
//	                    Otherwise say 'no rewrite needed'."
//
// Judge (one more LLM call):
//
//	J  synthesis      : "Here are 4 analyses. What's best for the
//	                    project? Favor quality, require scope
//	                    compliance, require functioning software.
//	                    Output the final verdict JSON."
//
// Total: 5 LLM calls per stuck criterion. Each is small (single
// question, bounded input), so they finish fast and don't trip the
// output-drift issues long prompts cause.
func ReasonAboutFailure(ctx context.Context, prov provider.Provider, model string, in ReasoningInput) (*ReasoningVerdict, error) {
	if prov == nil {
		return nil, fmt.Errorf("no provider for reasoning loop")
	}
	if model == "" {
		model = "claude-sonnet-4-6"
	}

	// Build the shared context block each analyst sees. This is the
	// same for all 4 analysts so the prompt-cache layer can reuse the
	// prefix across calls.
	sharedCtx := buildSharedReasoningCtx(in)

	// Run the 4 analysts. We run them sequentially to keep
	// bookkeeping simple and to let the prompt cache warm up. If
	// total latency becomes a problem, these can be made concurrent.
	analystNotes := map[string]string{}

	codeReview, err := runAnalyst(ctx, prov, model, analystCodeReviewPrompt, sharedCtx, "A1")
	if err != nil {
		return nil, fmt.Errorf("analyst A1 (code-review): %w", err)
	}
	analystNotes["code_review"] = codeReview

	acHygiene, err := runAnalyst(ctx, prov, model, analystACHygienePrompt, sharedCtx, "A2")
	if err != nil {
		return nil, fmt.Errorf("analyst A2 (ac-hygiene): %w", err)
	}
	analystNotes["ac_hygiene"] = acHygiene

	rootCause, err := runAnalyst(ctx, prov, model, analystRootCausePrompt, sharedCtx, "A3")
	if err != nil {
		return nil, fmt.Errorf("analyst A3 (root-cause): %w", err)
	}
	analystNotes["root_cause"] = rootCause

	acRewrite, err := runAnalyst(ctx, prov, model, analystACRewritePrompt, sharedCtx, "A4")
	if err != nil {
		return nil, fmt.Errorf("analyst A4 (ac-rewrite): %w", err)
	}
	analystNotes["ac_rewrite"] = acRewrite

	// Judge synthesis call.
	verdict, err := runJudgeSynthesis(ctx, prov, model, sharedCtx, analystNotes)
	if err != nil {
		return nil, fmt.Errorf("judge synthesis: %w", err)
	}
	verdict.AnalystNotes = analystNotes
	return verdict, nil
}

// --- Shared context builder -------------------------------------------------

func buildSharedReasoningCtx(in ReasoningInput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "SESSION: %s — %s\n\n", in.SessionID, in.SessionTitle)
	if strings.TrimSpace(in.TaskDescription) != "" {
		fmt.Fprintf(&b, "FEATURE BEING BUILT (from the task spec):\n%s\n\n", in.TaskDescription)
	}
	fmt.Fprintf(&b, "FAILING ACCEPTANCE CRITERION:\n  id: %s\n  description: %s\n", in.Criterion.ID, in.Criterion.Description)
	if in.Criterion.Command != "" {
		fmt.Fprintf(&b, "  command: %s\n", in.Criterion.Command)
	}
	if in.Criterion.FileExists != "" {
		fmt.Fprintf(&b, "  file_exists: %s\n", in.Criterion.FileExists)
	}
	if in.Criterion.ContentMatch != nil && in.Criterion.ContentMatch.File != "" {
		fmt.Fprintf(&b, "  content_match: file=%q pattern=%q\n", in.Criterion.ContentMatch.File, in.Criterion.ContentMatch.Pattern)
	}
	b.WriteString("\n")

	b.WriteString("FAILURE OUTPUT (what the criterion produced when it ran):\n")
	b.WriteString("----- BEGIN FAILURE OUTPUT -----\n")
	b.WriteString(truncateForReasoning(in.FailureOutput, 6000))
	b.WriteString("\n----- END FAILURE OUTPUT -----\n\n")

	if len(in.CodeExcerpts) > 0 {
		b.WriteString("RELEVANT CODE (files the agent wrote that this criterion probably touches):\n")
		paths := sortedKeys(in.CodeExcerpts)
		for _, p := range paths {
			fmt.Fprintf(&b, "\n--- %s ---\n", p)
			b.WriteString(truncateForReasoning(in.CodeExcerpts[p], 3000))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "PRIOR REPAIR ATTEMPTS: %d (the automated repair loop has tried and failed this many times on this specific criterion)\n", in.PriorAttempts)
	return b.String()
}

// --- Analyst prompts (each focused on ONE specific question) ---------------

const analystCodeReviewPrompt = `You are a senior code reviewer. You've been given ONE task spec, the code the build agent wrote, and the exact failing acceptance-criterion output. Your ONLY job is to answer one question: IS THE CODE CORRECT PER THE TASK SPEC?

Reason step by step:
  1. Read the task spec. What does it require the code to do?
  2. Read the code. Does every requirement have a corresponding implementation?
  3. Are there obvious bugs, missing files, missing imports, or incomplete logic?
  4. If the code looks right on the spec's own terms, say so. Don't invent reasons to fault it.

Output (plain text, not JSON):
  VERDICT: code_correct | code_incorrect | cannot_determine
  REASONING: one or two paragraphs explaining exactly what the code does right or wrong, with specific file/line references if applicable.
  GAPS: if code_incorrect, a short bullet list of what specifically is missing or wrong.

Be specific. Don't say "the code has issues" — say "apps/web/app/login/page.tsx does not handle the error response from /api/login, but the spec requires it".

`

const analystACHygienePrompt = `You are a test-engineering reviewer. You've been given the task spec, a single acceptance criterion, and the exact failure output it produced. Your ONLY job is to answer one question: IS THE ACCEPTANCE CRITERION WELL-FORMED AND TESTING THE RIGHT THING?

An AC is well-formed if:
  - The command is runnable (no unset env vars, no "|| echo ok" fallbacks, no "|| true", no git clone, no mktemp)
  - It actually tests what the task spec requires, not something incidental
  - It terminates in reasonable time
  - It uses the tools the stack declares (not global tools that aren't in any package.json)

Reason step by step:
  1. Read the AC command. Can it actually run in a workspace with node_modules on PATH?
  2. Does it test what the task spec claims to deliver? Or does it test an implementation detail the spec didn't mention?
  3. Is the check itself correct — would a properly-built feature actually pass this command?

Output (plain text, not JSON):
  VERDICT: ac_well_formed | ac_ill_formed | cannot_determine
  REASONING: one paragraph. If ill-formed, say exactly why: "uses $REPO_URL which is never set", "greps for a string that is a comment in the file", "tests for Next.js dev server startup which never terminates", etc.
  WHAT_WOULD_PASS: if ac_well_formed, describe in one sentence what kind of code change would make the criterion pass.

`

const analystRootCausePrompt = `You are a debugger. You've been given the failure output from a failed acceptance criterion. Your ONLY job is to answer: WHAT IS THE MOST LIKELY ROOT CAUSE?

Common root cause categories:
  - missing_dependency: "Cannot find module X" / "X: not found" — a library isn't installed or isn't declared in the relevant package.json
  - missing_script: "missing script: X" — the script is not declared in the package.json the command targets
  - wrong_path: the command references a file that doesn't exist where it's looking
  - stale_install: node_modules is out of sync with package.json
  - syntax_error: the code doesn't compile / has a syntax issue
  - runtime_behavior: the code compiles but behaves wrong at runtime
  - test_runner_not_configured: a test runner exists but has no config file / no glob match
  - brittle_grep: a grep-based check is looking for text that's semantically present but literally different
  - env_missing: a required env var is unset
  - other: anything else, name it specifically

Reason step by step:
  1. Read the failure output. What's the most specific error string?
  2. Cross-reference with the error categories above.
  3. If there are MULTIPLE causes, name the most fundamental one (e.g. "node_modules missing" is more fundamental than "tsc: not found" even though both are in the output).

Output (plain text, not JSON):
  CATEGORY: <one of the categories above>
  REASONING: two sentences max. Include the specific error string you relied on.
  FIX_SHAPE: one-sentence description of what the fix would look like (without writing the fix yet).

`

const analystACRewritePrompt = `You are an AC rewriter. You've been given a task spec and a single acceptance criterion. Your ONLY job is to: IF the criterion command is structurally wrong, propose a replacement command that hits the same testing intent but actually works. IF the criterion is fine, say so.

Hygiene rules the new command must follow:
  - Runs in the workspace cwd. No "cd $(mktemp -d)", no "git clone $REPO_URL".
  - No "|| echo ok" / "|| true" fallbacks.
  - Terminates in under 60 seconds. No long-running dev servers.
  - Prefers "pnpm --filter <pkg> <script>" or direct binary invocation (tsc, vitest, next, eslint) — node_modules/.bin is on PATH.
  - Tests what the task spec actually requires, not implementation details.
  - Uses only tools declared in the stack (package.json or stack.infra), not unspecified globals.

Reason step by step:
  1. Is the current command structurally wrong? If no, output "no rewrite needed".
  2. If yes, what's the SAME intent written correctly? Write out one runnable shell command.
  3. Does your rewrite still test what the task spec requires? If not, iterate.

Output (plain text, not JSON):
  REWRITE_NEEDED: yes | no
  REPLACEMENT_COMMAND: <the new command, one line> | <empty if no>
  REASONING: one paragraph explaining the before/after, or why no rewrite is needed.

`

// --- Judge synthesis -------------------------------------------------------

const judgeSynthesisPrompt = `You are a senior supervising engineer. You have just read four independent analyses of a single failing acceptance criterion:

  A1 code-review — is the code correct per the task spec?
  A2 ac-hygiene  — is the acceptance criterion well-formed?
  A3 root-cause  — what's the most likely root cause of the failure?
  A4 ac-rewrite  — if the AC needs rewriting, here's a proposed replacement.

Your job: synthesize these four analyses into ONE final verdict that the supervisor will act on. Favor quality. Require scope compliance — do not approve an ignore just to get past the failure; only approve when the code genuinely matches the task spec and the AC is measuring something outside that spec. Require functioning software — if the code has a real gap, the verdict must fix the code, not the AC.

The four possible categories:

  1. code_bug        — analysts agree the code is the issue. AC is fine. Fix the code.
  2. ac_bug          — analysts agree the AC is the issue. Code is fine. Rewrite the AC.
  3. both            — both code and AC have issues. Fix both.
  4. acceptable_as_is — the code matches the task spec, the AC is incidentally failing, and the right move is to approve an ignore so later runs don't re-fail. VERY RARE — only use when A1 says code_correct AND A2 says ac_ill_formed in a way where the fix is clearly out-of-scope for the current session.

Ground rules:
  - When A1 says code_incorrect, category must be code_bug or both.
  - When A2 says ac_ill_formed and A1 says code_correct, category must be ac_bug.
  - When both analysts say "incorrect" / "ill_formed", category must be both.
  - When A1 says code_correct and A2 says ac_well_formed, something else is wrong — look at A3's root cause and classify accordingly (usually code_bug in a subtle way the initial review missed).
  - Do NOT classify as acceptable_as_is just because the repair loop is stuck. Stuck does not mean acceptable.

Output ONLY a single JSON object — no prose, no backticks:

{
  "category": "code_bug|ac_bug|both|acceptable_as_is",
  "reasoning": "one paragraph synthesis explaining the verdict with specific references to which analyst said what",
  "code_fix": "only if category is code_bug or both — what to change, specific files and outcomes",
  "ac_rewrite": "only if category is ac_bug or both — the replacement command verbatim, one line, no commentary",
  "approve_reason": "only if category is acceptable_as_is — why this is a false positive"
}

`

// runAnalyst fires one analyst LLM call and returns the raw text.
func runAnalyst(ctx context.Context, prov provider.Provider, model, systemPrompt, sharedCtx, label string) (string, error) {
	full := systemPrompt + "\n\nCONTEXT:\n" + sharedCtx
	userContent, _ := json.Marshal([]map[string]interface{}{{"type": "text", "text": full}})
	resp, err := prov.Chat(provider.ChatRequest{
		Model:     model,
		MaxTokens: 2000,
		Messages:  []provider.ChatMessage{{Role: "user", Content: userContent}},
	})
	if err != nil {
		return "", fmt.Errorf("analyst %s chat: %w", label, err)
	}
	raw, diag := collectModelText(resp)
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("analyst %s returned no content (stop_reason=%q)\n%s", label, resp.StopReason, diag)
	}
	return raw, nil
}

// runJudgeSynthesis fires the judge LLM call with the 4 analyst
// outputs as input and returns the parsed verdict.
func runJudgeSynthesis(ctx context.Context, prov provider.Provider, model, sharedCtx string, notes map[string]string) (*ReasoningVerdict, error) {
	var b strings.Builder
	b.WriteString(judgeSynthesisPrompt)
	b.WriteString("\nSHARED CONTEXT:\n")
	b.WriteString(sharedCtx)
	b.WriteString("\n\nANALYST OUTPUTS:\n\n")
	b.WriteString("=== A1 code-review ===\n")
	b.WriteString(notes["code_review"])
	b.WriteString("\n\n=== A2 ac-hygiene ===\n")
	b.WriteString(notes["ac_hygiene"])
	b.WriteString("\n\n=== A3 root-cause ===\n")
	b.WriteString(notes["root_cause"])
	b.WriteString("\n\n=== A4 ac-rewrite ===\n")
	b.WriteString(notes["ac_rewrite"])
	b.WriteString("\n\nNow output the JSON verdict.")

	userContent, _ := json.Marshal([]map[string]interface{}{{"type": "text", "text": b.String()}})
	resp, err := prov.Chat(provider.ChatRequest{
		Model:     model,
		MaxTokens: 3000,
		Messages:  []provider.ChatMessage{{Role: "user", Content: userContent}},
	})
	if err != nil {
		return nil, fmt.Errorf("judge chat: %w", err)
	}
	raw, diag := collectModelText(resp)
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("judge returned no content (stop_reason=%q)\n%s", resp.StopReason, diag)
	}
	_ = os.WriteFile("/tmp/stoke-reasoning-raw.txt", []byte(raw), 0o644)

	var verdict ReasoningVerdict
	if _, err := jsonutil.ExtractJSONInto(raw, &verdict); err != nil {
		return nil, fmt.Errorf("parse judge verdict: %w (raw: /tmp/stoke-reasoning-raw.txt)", err)
	}
	if verdict.Category == "" {
		return nil, fmt.Errorf("judge verdict missing category (raw: /tmp/stoke-reasoning-raw.txt)")
	}
	verdict.Category = strings.ToLower(strings.TrimSpace(verdict.Category))
	switch verdict.Category {
	case "code_bug", "ac_bug", "both", "acceptable_as_is":
	default:
		return nil, fmt.Errorf("judge verdict has unknown category %q", verdict.Category)
	}
	return &verdict, nil
}

// --- Helpers ---------------------------------------------------------------

// CollectCodeExcerpts reads the contents of up to maxFiles files from
// the given paths. Files that don't exist or are larger than
// maxBytesPerFile are skipped / truncated.
func CollectCodeExcerpts(repoRoot string, paths []string, maxFiles, maxBytesPerFile int) map[string]string {
	if maxFiles <= 0 {
		maxFiles = 8
	}
	if maxBytesPerFile <= 0 {
		maxBytesPerFile = 6000
	}
	out := map[string]string{}
	for _, p := range paths {
		if len(out) >= maxFiles {
			break
		}
		if strings.TrimSpace(p) == "" {
			continue
		}
		abs := p
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(repoRoot, p)
		}
		info, err := os.Stat(abs)
		if err != nil || info.IsDir() {
			continue
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		if len(data) > maxBytesPerFile {
			data = append([]byte{}, data[:maxBytesPerFile]...)
			data = append(data, []byte("\n... (truncated)\n")...)
		}
		out[p] = string(data)
	}
	return out
}

func truncateForReasoning(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	half := (max - 32) / 2
	if half < 100 {
		return s[:max]
	}
	return s[:half] + "\n... (truncated in middle) ...\n" + s[len(s)-half:]
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Small in-place insertion sort; keeps the dependency footprint
	// minimal and is deterministic across runs.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	return keys
}
